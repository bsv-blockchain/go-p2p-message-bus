// Package p2p provides a peer-to-peer messaging client built on libp2p.
// It supports topic-based publish/subscribe messaging with automatic peer discovery,
// NAT traversal, and relay functionality.
package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-kad-dht/records"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
	madns "github.com/multiformats/go-multiaddr-dns"
)

const (
	// peerAddressRequestProtocol is the original peer-address request protocol.
	// The requester writes an encoded peer ID in a single call and does not
	// half-close, so the responder has to treat one read as the whole request.
	peerAddressRequestProtocol = "/p2p-msg-bus/peer-addr-request/1.0.0"

	// peerAddressRequestProtocolV2 differs from 1.0.0 only in framing: the
	// requester half-closes after writing the peer ID, so the responder reads
	// to EOF and reassembles a fragmented request correctly. Requesters offer
	// both and fall back to 1.0.0 against older peers.
	peerAddressRequestProtocolV2 = "/p2p-msg-bus/peer-addr-request/1.1.0"

	// peerAddressStreamTimeout bounds how long either side of a peer-address
	// exchange waits on the stream. Without it a peer that opens a stream and
	// never writes pins a handler goroutine and a stream for the life of the
	// connection, and a requester wedges on a peer that never answers.
	peerAddressStreamTimeout = 10 * time.Second

	// maxPeerIDRequestBytes bounds the encoded peer ID a requester may send.
	// Encoded peer IDs are well under 128 bytes.
	maxPeerIDRequestBytes = 128

	// minPeerIDBytes is the shortest decoded peer ID libp2p produces: a sha256
	// multihash (34 bytes); identity multihashes of supported keys are longer.
	// Anything shorter is a prefix that happened to decode, not a real peer.
	minPeerIDBytes = 34

	// maxSharedPeerAddresses caps the addresses exchanged for one peer in either
	// direction, so a responder cannot stuff a requester's peerstore and a
	// responder never emits a reply its peers would reject as oversized.
	maxSharedPeerAddresses = 32

	// maxPeerAddressResponseBytes bounds the JSON address list a responder may
	// send; maxSharedPeerAddresses multiaddrs fit comfortably.
	maxPeerAddressResponseBytes = 8 * 1024

	// peerAddressRetryInterval is how long a failed address lookup for a topic
	// peer is remembered before it is attempted again.
	peerAddressRetryInterval = 5 * time.Minute

	// peerAddressRequestWindow and peerAddressRequestsPerWindow bound how many
	// inbound address requests a single peer may open; excess streams are reset.
	// A legitimate peer asks each neighbour once per unknown topic peer per 15s
	// scan, so a cold-starting node on a large topic can burst several hundred.
	peerAddressRequestWindow     = time.Minute
	peerAddressRequestsPerWindow = 512
)

var (
	// ErrNameRequired is returned when Config.Name is not provided.
	ErrNameRequired = errors.New("config.Name is required")
	// ErrPrivateKeyRequired is returned when Config.PrivateKey is not provided.
	ErrPrivateKeyRequired = errors.New("config.PrivateKey is required")

	errPeerIDRequestTooLarge       = errors.New("peer address request exceeds size limit")
	errPeerIDRequestInvalid        = errors.New("peer address request is not a valid peer ID")
	errPeerAddressResponseTooLarge = errors.New("peer address response exceeds size limit")
)

// Compile-time check to ensure client implements Client interface
var _ Client = (*client)(nil)

// client represents a P2P messaging client implementation.
type client struct {
	config Config
	host   host.Host
	dht    *dht.IpfsDHT
	pubsub *pubsub.PubSub
	topics map[string]*pubsub.Topic
	subs   map[string]*pubsub.Subscription
	mu     sync.RWMutex
	// readers tracks every Subscribe goroutine. Each one owns its message
	// channel and closes it on exit; Close cancels the context, then waits
	// here before tearing down the host, so no reader can ever send on a
	// channel that has already been closed under it.
	readers sync.WaitGroup
	// closed is set under mu by Close before it waits on readers, so a
	// concurrent Subscribe cannot add a reader to a draining WaitGroup.
	closed           bool
	peerTracker      *peerTracker
	ctx              context.Context //nolint:containedctx // Client manages its own lifecycle
	cancel           context.CancelFunc
	mdnsService      mdns.Service
	logger           logger
	routingDiscovery *drouting.RoutingDiscovery
	discoverNow      chan struct{}

	// peerAddrStreamTimeout is the per-stream deadline for the peer-address
	// exchange. Defaults to peerAddressStreamTimeout; tests shorten it.
	peerAddrStreamTimeout time.Duration
	// peerAddrLimiter enforces the per-peer inbound peer-address request budget.
	peerAddrLimiter *peerAddressLimiter
	// addrLookupsInFlight holds the targets currently being looked up so the
	// retry loop never runs two lookups for one target concurrently.
	addrLookupsMu       sync.Mutex
	addrLookupsInFlight map[peer.ID]struct{}
}

// NewClient creates and initializes a new P2P client.
// It automatically starts the client and begins peer discovery.
//
//nolint:gocyclo // Initialization complexity is acceptable for setup function
func NewClient(config Config) (Client, error) {
	if config.Name == "" {
		return nil, ErrNameRequired
	}

	// Use provided logger or default
	clientLogger := getLogger(config.Logger)

	streamTimeout := peerAddressExchangeSettings(config, clientLogger)
	ctx, cancel := context.WithCancel(context.Background())

	// Validate private key (required)
	if config.PrivateKey == nil {
		cancel()
		return nil, ErrPrivateKeyRequired
	}

	// Build host options
	hostOpts, err := buildHostOptions(config, clientLogger, cancel)
	if err != nil {
		return nil, err
	}

	bootstrapPeers, relayPeers := getBootstrapAndRelayPeers(config, clientLogger)

	// Create and setup libp2p host
	h, err := createHost(ctx, hostOpts, config, relayPeers, clientLogger, cancel)
	if err != nil {
		return nil, err
	}

	// Set up DHT (unless mode is "off")
	var kadDHT *dht.IpfsDHT
	if config.DHTMode == "off" {
		clientLogger.Infof("DHT mode: off (topic-only network, no DHT peer discovery)")
	} else {
		var dhtErr error
		kadDHT, dhtErr = setupDHT(ctx, h, config, bootstrapPeers, clientLogger, cancel)
		if dhtErr != nil {
			return nil, dhtErr
		}
	}

	// Connect to bootstrap peers (which are also used as relay peers).
	connectToManagedPeers(ctx, h, bootstrapPeerKind, bootstrapPeers, clientLogger)

	// Static peers are dialed independently of bootstrap. Unlike bootstrap
	// peers they aren't fed into the DHT or used as relays - they're just
	// persistent direct connections we want to keep alive.
	staticPeers := parsePeerMultiaddrs(config.StaticPeers, clientLogger)
	if len(staticPeers) > 0 {
		clientLogger.Infof("Configured %d static peer(s)", len(staticPeers))
		connectToManagedPeers(ctx, h, staticPeerKind, staticPeers, clientLogger)
	}

	loadAndConnectCachedPeers(ctx, h, config, clientLogger)

	// Create pubsub. Peer exchange is on by default; peer scoring (Sybil defence)
	// is applied when configured. See buildPubSubOptions / EnablePeerScoring.
	psOpts, err := buildPubSubOptions(config, clientLogger)
	if err != nil {
		_ = h.Close()
		cancel()
		return nil, fmt.Errorf("invalid pubsub configuration: %w", err)
	}

	ps, err := pubsub.NewGossipSub(ctx, h, psOpts...)
	if err != nil {
		_ = h.Close()
		cancel()
		return nil, fmt.Errorf("failed to create pubsub: %w", err)
	}

	// Set up mDNS discovery (only if explicitly enabled)
	var mdnsService mdns.Service
	if config.EnableMDNS {
		mdnsService = mdns.NewMdnsService(h, "", &discoveryNotifee{h: h, ctx: ctx, logger: clientLogger})
		if err := mdnsService.Start(); err != nil {
			clientLogger.Errorf("mDNS failed to start: %v", err)
		} else {
			clientLogger.Infof("mDNS discovery started")
		}
	} else {
		clientLogger.Infof("mDNS discovery disabled (production safe default)")
	}

	var routingDiscovery *drouting.RoutingDiscovery
	if kadDHT != nil {
		routingDiscovery = drouting.NewRoutingDiscovery(kadDHT)
	}

	c := &client{
		config:           config,
		host:             h,
		dht:              kadDHT,
		pubsub:           ps,
		topics:           make(map[string]*pubsub.Topic),
		subs:             make(map[string]*pubsub.Subscription),
		peerTracker:      newPeerTracker(),
		ctx:              ctx,
		cancel:           cancel,
		mdnsService:      mdnsService,
		logger:           clientLogger,
		routingDiscovery: routingDiscovery,
		discoverNow:      make(chan struct{}, 1),

		peerAddrStreamTimeout: streamTimeout,
		peerAddrLimiter:       newPeerAddressLimiter(),
		addrLookupsInFlight:   make(map[peer.ID]struct{}),
	}

	// Set up peer address request/response protocol handler
	// This allows other peers to request addresses from us (useful for DHT-off clients)
	h.SetStreamHandler(protocol.ID(peerAddressRequestProtocol), c.handlePeerAddressRequest)
	h.SetStreamHandler(protocol.ID(peerAddressRequestProtocolV2), c.handlePeerAddressRequest)
	clientLogger.Infof("Peer address request protocol handler installed")

	// Always maintain bootstrap peer connections - ensures reconnection after
	// simultaneous restarts where the initial one-shot bootstrap dial fails.
	go c.maintainPeerSet(ctx, bootstrapPeerKind, bootstrapPeers)

	// Same idea for static peers, when configured.
	if len(staticPeers) > 0 {
		go c.maintainPeerSet(ctx, staticPeerKind, staticPeers)
	}

	// Start DHT discovery (only if DHT is enabled)
	if kadDHT != nil {
		go c.waitForDHTAndAdvertise(ctx, routingDiscovery)
		go c.discoverPeers(ctx, routingDiscovery, true)
	} else {
		clientLogger.Infof("Skipping DHT discovery - peer discovery will happen via GossipSub topic mesh only")
		clientLogger.Infof("Topic peer exchange enabled - will attempt direct connections to discovered peers")
		// Periodically check for new peer addresses learned via PX
		go c.attemptDirectConnectionsToTopicPeers(ctx)
	}

	return c, nil
}

func getBootstrapAndRelayPeers(config Config, clientLogger logger) ([]peer.AddrInfo, []peer.AddrInfo) {
	// Get bootstrap peers based on environment and configuration
	var bootstrapPeers []peer.AddrInfo

	if testing.Testing() {
		// Test mode: empty list for fast, isolated tests
		bootstrapPeers = []peer.AddrInfo{}
		clientLogger.Infof("Test mode detected - using no bootstrap peers (isolated mode)")
	} else if len(config.BootstrapPeers) > 0 {
		// Custom bootstrap peers provided - use them
		bootstrapPeers = parsePeerMultiaddrs(config.BootstrapPeers, clientLogger)
		clientLogger.Infof("Using %d custom bootstrap peer(s)", len(bootstrapPeers))
	} else {
		// No custom peers - use default IPFS bootstrap peers
		bootstrapPeers = dht.GetDefaultBootstrapPeerAddrInfos()
		clientLogger.Infof("Using %d default IPFS bootstrap peers", len(bootstrapPeers))
	}

	// Use the same bootstrap peers as relay peers
	clientLogger.Infof("Using bootstrap peers as relay peers")
	return bootstrapPeers, bootstrapPeers
}

// Helper functions for NewClient

// peerAddressExchangeSettings warns when the configured name would be altered
// by receivers and returns the peer-address stream deadline to use.
func peerAddressExchangeSettings(config Config, log logger) time.Duration {
	if seen := sanitizePeerName(config.Name); seen != config.Name {
		log.Warnf("Config.Name %q will be seen by peers as %q (names are limited to %d printable bytes)", config.Name, seen, maxPeerNameLen)
	}
	if config.peerAddressStreamTimeout > 0 {
		return config.peerAddressStreamTimeout
	}
	return peerAddressStreamTimeout
}

func getLogger(configLogger logger) logger {
	if configLogger == nil {
		l := &DefaultLogger{}
		l.Debugf("Using default logger")
		return l
	}
	return configLogger
}

// createPrivateIPConnectionGater creates a ConnectionGater that blocks private IP ranges.
// Returns a configured BasicConnectionGater that prevents connections to/from:
// - RFC1918 private networks (10.x, 172.16-31.x, 192.168.x)
// - Link-local addresses (169.254.x, fe80::)
// - Loopback addresses (127.x, ::1)
// - Shared address space (100.64.x)
// - IPv6 unique local addresses (fc00::)
func createPrivateIPConnectionGater(log logger, cancel context.CancelFunc) (*conngater.BasicConnectionGater, error) {
	ipFilter, err := conngater.NewBasicConnectionGater(nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create connection gater: %w", err)
	}

	// Standard private IP ranges to block
	privateRanges := []string{
		"10.0.0.0/8",     // RFC1918 private network
		"172.16.0.0/12",  // RFC1918 private network
		"192.168.0.0/16", // RFC1918 private network
		"127.0.0.0/8",    // Loopback
		"169.254.0.0/16", // Link-local
		"100.64.0.0/10",  // Shared Address Space (RFC6598)
		"fc00::/7",       // IPv6 Unique Local Addresses
		"fe80::/10",      // IPv6 Link-Local Addresses
		"::1/128",        // IPv6 Loopback
	}

	for _, cidr := range privateRanges {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to parse CIDR %s: %w", cidr, err)
		}
		if err := ipFilter.BlockSubnet(ipnet); err != nil {
			log.Warnf("Failed to block subnet %s: %v", cidr, err)
		}
	}

	return ipFilter, nil
}

func buildHostOptions(config Config, log logger, cancel context.CancelFunc) ([]libp2p.Option, error) {
	hostOpts := []libp2p.Option{libp2p.Identity(config.PrivateKey)}

	// Explicitly configure only TCP transport to prevent WebRTC's mDNS usage
	// WebRTC transport uses mDNS (port 5353) for ICE candidate discovery
	// By only enabling TCP, we avoid any unwanted mDNS traffic
	hostOpts = append(hostOpts, libp2p.Transport(tcp.NewTCPTransport))
	log.Infof("Configured TCP-only transport (WebRTC disabled to prevent mDNS)")

	// Configure connection manager to limit total connections
	maxConns := config.MaxConnections
	if maxConns == 0 {
		maxConns = 35 // Default high water mark
	}
	minConns := config.MinConnections
	if minConns == 0 {
		minConns = 25 // Default low water mark
	}
	gracePeriod := config.ConnectionGracePeriod
	if gracePeriod == 0 {
		gracePeriod = 20 * time.Second // Default grace period
	}

	connMgr, err := connmgr.NewConnManager(
		minConns,
		maxConns,
		connmgr.WithGracePeriod(gracePeriod),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create connection manager: %w", err)
	}

	hostOpts = append(hostOpts, libp2p.ConnectionManager(connMgr))
	log.Infof("Connection manager configured: min=%d, max=%d, grace=%v", minConns, maxConns, gracePeriod)

	// Add connection gater to block private IPs if AllowPrivateIPs is false (default)
	if !config.AllowPrivateIPs {
		ipFilter, err := createPrivateIPConnectionGater(log, cancel)
		if err != nil {
			return nil, err
		}

		hostOpts = append(hostOpts, libp2p.ConnectionGater(ipFilter))
		log.Infof("Private IP connection gater enabled (blocking RFC1918 and local addresses)")
	}

	// Configure announce addresses if provided (useful for K8s)
	if len(config.AnnounceAddrs) > 0 {
		announceAddrs := make([]multiaddr.Multiaddr, 0, len(config.AnnounceAddrs))
		for _, addrStr := range config.AnnounceAddrs {
			maddr, err := multiaddr.NewMultiaddr(addrStr)
			if err != nil {
				cancel()
				return nil, fmt.Errorf("invalid announce address %s: %w", addrStr, err)
			}
			announceAddrs = append(announceAddrs, maddr)
		}

		hostOpts = append(hostOpts, libp2p.AddrsFactory(func([]multiaddr.Multiaddr) []multiaddr.Multiaddr {
			return announceAddrs
		}))
		log.Infof("Using custom announce addresses: %v", config.AnnounceAddrs)
	}

	return hostOpts, nil
}

func createHost(_ context.Context, hostOpts []libp2p.Option, config Config, relayPeers []peer.AddrInfo, log logger, cancel context.CancelFunc) (host.Host, error) {
	hostOpts = append(
		hostOpts,
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", config.Port),
			fmt.Sprintf("/ip6/::/tcp/%d", config.Port),
		),
	)

	// Enable NAT features only if explicitly enabled
	// UPnP/NAT-PMP scans the local gateway which triggers network scanning alerts
	if config.EnableNAT {
		hostOpts = append(
			hostOpts,
			libp2p.NATPortMap(),
			libp2p.EnableNATService(),
			libp2p.EnableHolePunching(),
		)
		log.Infof("UPnP/NAT-PMP enabled (will scan local gateway for port mapping)")
	} else {
		log.Infof("UPnP/NAT-PMP disabled (production safe default)")
	}

	hostOpts = append(
		hostOpts,
		libp2p.EnableRelay(),
		libp2p.EnableAutoRelayWithStaticRelays(relayPeers),
	)

	if config.ProtocolVersion != "" {
		hostOpts = append(hostOpts, libp2p.ProtocolVersion(config.ProtocolVersion))
	}

	h, err := libp2p.New(hostOpts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create host: %w", err)
	}

	log.Infof("P2P Client created. ID: %s", h.ID())
	log.Infof("Listening on: %v", h.Addrs())
	return h, nil
}

func setupDHT(ctx context.Context, h host.Host, config Config, bootstrapPeers []peer.AddrInfo, log logger, cancel context.CancelFunc) (*dht.IpfsDHT, error) {
	// Determine DHT mode (default to server)
	mode := dht.ModeServer
	if config.DHTMode == "client" {
		mode = dht.ModeClient
		log.Infof("DHT mode: client (query-only, no provider storage)")
	} else {
		log.Infof("DHT mode: server (will advertise and store provider records)")
	}

	// Build DHT options
	dhtOpts := []dht.Option{
		dht.Mode(mode),
		dht.BootstrapPeers(bootstrapPeers...),
	}

	// If server mode and custom cleanup interval specified, configure the
	// built-in provider manager's cleanup interval. Custom ProviderStore
	// implementations can no longer be injected; the DHT always runs the
	// built-in provider manager, configured via ProviderManagerOpts.
	if mode == dht.ModeServer && config.DHTCleanupInterval > 0 {
		log.Infof("Configuring DHT cleanup interval: %v", config.DHTCleanupInterval)
		dhtOpts = append(dhtOpts, dht.ProviderManagerOpts(records.CleanupInterval(config.DHTCleanupInterval)))
	}

	kadDHT, err := dht.New(h, dhtOpts...)
	if err != nil {
		_ = h.Close()
		cancel()
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}

	if bootstrapErr := kadDHT.Bootstrap(ctx); bootstrapErr != nil {
		_ = h.Close()
		cancel()
		return nil, fmt.Errorf("failed to bootstrap DHT: %w", bootstrapErr)
	}

	return kadDHT, nil
}

// parsePeerMultiaddrs is a shared helper to parse bootstrap peer multiaddr strings.
// Supports /dnsaddr/ multiaddrs which are resolved via DNS TXT records at _dnsaddr.<domain>.
func parsePeerMultiaddrs(peerConfigs []string, log logger) []peer.AddrInfo {
	if len(peerConfigs) == 0 {
		return nil
	}

	peers := make([]peer.AddrInfo, 0, len(peerConfigs))
	for _, peerStr := range peerConfigs {
		maddr, err := multiaddr.NewMultiaddr(peerStr)
		if err != nil {
			log.Errorf("Invalid bootstrap address %s: %v (hint: use /dns4/ for hostnames, /ip4/ for IP addresses, /dnsaddr/ for DNS-based discovery)", peerStr, err)
			continue
		}

		// Resolve /dnsaddr/ multiaddrs via DNS TXT records
		if isDNSAddr(maddr) {
			resolved, resolveErr := madns.DefaultResolver.Resolve(context.Background(), maddr)
			if resolveErr != nil {
				log.Errorf("Failed to resolve dnsaddr %s: %v", peerStr, resolveErr)
				continue
			}
			log.Infof("Resolved dnsaddr %s to %d peer(s)", peerStr, len(resolved))
			for _, raddr := range resolved {
				addrInfo, addrErr := peer.AddrInfoFromP2pAddr(raddr)
				if addrErr != nil {
					log.Errorf("Invalid resolved peer address %s: %v", raddr, addrErr)
					continue
				}
				peers = append(peers, *addrInfo)
			}
			continue
		}

		addrInfo, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			log.Errorf("Invalid bootstrap peer info %s: %v", peerStr, err)
			continue
		}
		peers = append(peers, *addrInfo)
	}

	return peers
}

// isDNSAddr checks if a multiaddr starts with a /dnsaddr/ component
func isDNSAddr(maddr multiaddr.Multiaddr) bool {
	first, _ := multiaddr.SplitFirst(maddr)
	return first != nil && first.Protocol().Code == multiaddr.P_DNSADDR
}

// peerSetKind identifies which managed peer pool a log line refers to. Both
// bootstrap and static peers go through the same connect/maintain code paths
// (one-shot dial on startup, persistent reconnect loop); the kind is only used
// to keep log output distinguishable.
const (
	bootstrapPeerKind = "bootstrap"
	staticPeerKind    = "static"
)

// connectToManagedPeers dials every peer in the list, fire-and-forget. Used
// for both bootstrap and static peer pools at client startup. The persistent
// reconnect loop is started separately by maintainPeerSet.
func connectToManagedPeers(ctx context.Context, h host.Host, kind string, peers []peer.AddrInfo, log logger) {
	for _, peerInfo := range peers {
		go func(pi peer.AddrInfo) {
			if connectErr := h.Connect(ctx, pi); connectErr != nil {
				log.Warnf("Failed to connect to %s peer %s (%v): %v", kind, pi.ID.String()[:16], pi.Addrs, connectErr)
			} else {
				log.Infof("Connected to %s peer: %s", kind, pi.ID.String())
			}
		}(peerInfo)
	}
}

func loadAndConnectCachedPeers(ctx context.Context, h host.Host, config Config, log logger) {
	if config.PeerCacheFile == "" {
		return
	}

	ttl := config.PeerCacheTTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}

	cachedPeers := loadPeerCache(config.PeerCacheFile, ttl, log)
	if len(cachedPeers) > 0 {
		log.Infof("Connecting to %d cached peers...", len(cachedPeers))
		connectToCachedPeers(ctx, h, cachedPeers, log)
	}
}

// Subscribe subscribes to a topic and returns a channel that will receive messages.
// The returned channel is closed when the client is closed (or, if the client
// is already closed, immediately). The channel is owned by the reader
// goroutine started here: it is the only code that closes it, and it does so
// only after it has stopped sending, so a consumer ranging over the channel
// terminates cleanly and a send-on-closed-channel panic is impossible.
func (c *client) Subscribe(topic string) <-chan Message {
	msgChan := make(chan Message, 100)

	c.logger.Debugf("Subscribing to topic: %s", topic)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.logger.Warnf("Subscribe to topic %s after Close: returning closed channel", topic)
		close(msgChan)
		return msgChan
	}
	c.readers.Add(1)
	c.mu.Unlock()

	go func() {
		defer c.readers.Done()
		defer close(msgChan)

		// Join or get existing topic
		c.mu.Lock()
		t, ok := c.topics[topic]
		c.mu.Unlock()

		if !ok {
			var err error
			t, err = c.pubsub.Join(topic)
			if err != nil {
				c.logger.Errorf("Failed to join topic %s: %v", topic, err)
				return
			}

			c.mu.Lock()
			c.topics[topic] = t
			c.mu.Unlock()
		}

		// Subscribe to topic
		sub, err := t.Subscribe()
		if err != nil {
			c.logger.Errorf("Failed to subscribe to topic %s: %v", topic, err)
			return
		}

		c.mu.Lock()
		c.subs[topic] = sub
		c.mu.Unlock()

		// Trigger immediate discovery for the new topic
		select {
		case c.discoverNow <- struct{}{}:
		default:
		}

		// Set up peer connection notifications for this topic
		c.host.Network().Notify(&network.NotifyBundle{
			ConnectedF: func(_ network.Network, conn network.Conn) {
				go func() {
					time.Sleep(500 * time.Millisecond)
					peerID := conn.RemotePeer()
					topicPeers := t.ListPeers()
					if slices.Contains(topicPeers, peerID) {
						// Tag topic peers with high value to protect from connection manager pruning
						c.host.ConnManager().TagPeer(peerID, fmt.Sprintf("topic:%s", topic), 100)
						c.logger.Debugf("Tagged topic peer %s for protection", peerID)

						name := c.peerTracker.getName(peerID)
						addr := conn.RemoteMultiaddr().String()
						c.logger.Infof("[CONNECTED] Topic peer %s [%s] %s", peerID.String(), name, addr)

						// Save peer cache
						c.savePeerCache()
					}
				}()
			},
			DisconnectedF: func(_ network.Network, conn network.Conn) {
				peerID := conn.RemotePeer()
				topicPeers := t.ListPeers()
				if slices.Contains(topicPeers, peerID) {
					// Untag peer when they disconnect from topic
					c.host.ConnManager().UntagPeer(peerID, fmt.Sprintf("topic:%s", topic))
					c.logger.Infof("[DISCONNECTED] Lost connection to topic peer %s", peerID.String()[:16])
				}
			},
		})

		// Start receiving messages
		c.receiveMessages(sub, t, msgChan)
	}()

	return msgChan
}

// Publish publishes a message to the specified topic.
func (c *client) Publish(ctx context.Context, topic string, data []byte) error {
	c.mu.RLock()
	t, ok := c.topics[topic]
	c.mu.RUnlock()

	if !ok {
		var err error
		t, err = c.pubsub.Join(topic)
		if err != nil {
			return fmt.Errorf("failed to join topic: %w", err)
		}

		c.mu.Lock()
		c.topics[topic] = t
		c.mu.Unlock()
	}

	// Wrap data with metadata
	msg := struct {
		Name string `json:"name"`
		Data []byte `json:"data"`
	}{
		Name: c.config.Name,
		Data: data,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	return t.Publish(ctx, msgBytes)
}

// GetPeers returns information about all known peers on subscribed topics.
func (c *client) GetPeers() []PeerInfo {
	allTopicPeers := c.peerTracker.getAllTopicPeers()
	peers := make([]PeerInfo, 0, len(allTopicPeers))

	for _, peerID := range allTopicPeers {
		conns := c.host.Network().ConnsToPeer(peerID)
		addrs := make([]string, 0, len(conns))
		for _, conn := range conns {
			addrs = append(addrs, conn.RemoteMultiaddr().String())
		}

		peers = append(peers, PeerInfo{
			ID:    peerID.String(),
			Name:  c.peerTracker.getName(peerID),
			Addrs: addrs,
		})
	}

	return peers
}

// GetID returns this peer's ID as a string.
func (c *client) GetID() string {
	return c.host.ID().String()
}

// closeReadersWait bounds how long Close waits for subscription readers to
// exit before tearing down the host regardless. It sits inside the overall
// closeTimeout so the host teardown still gets a share of the budget.
const (
	closeReadersWait = 1 * time.Second
	closeTimeout     = 2 * time.Second
)

// Close shuts down the client and releases all resources.
//
// Shutdown order matters: the producers (subscription readers) are stopped
// first and the consumer-facing channels are closed by those readers as they
// exit. Cancelling the client context unblocks every reader parked in
// Subscription.Next (which selects on that context) or in a channel send;
// Close then waits for them before closing topics and the host, so a reader
// that just pulled a buffered message can never race a close of the channel
// it is about to send on. Safe to call more than once.
func (c *client) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()

	c.cancel()

	done := make(chan struct{})
	go func() {
		c.mu.Lock()
		// Best-effort subscription cleanup. Readers are already unblocked by
		// the context cancellation above (pubsub subscriptions share the
		// client context, so Cancel is a no-op at this point); this only
		// matters if pubsub ever stops deriving from it.
		for _, sub := range c.subs {
			sub.Cancel()
		}
		c.mu.Unlock()

		// Wait for every reader to exit; each closes its own channel on the
		// way out. Bounded so a reader stuck in a blocking user logger cannot
		// hold the listening sockets open past the shutdown budget: after the
		// wait budget the host is torn down anyway.
		readersDone := make(chan struct{})
		go func() {
			c.readers.Wait()
			close(readersDone)
		}()
		select {
		case <-readersDone:
		case <-time.After(closeReadersWait):
			c.logger.Warnf("Subscription readers did not exit within %s, tearing down host anyway", closeReadersWait)
		}

		c.mu.Lock()
		// Close all topics
		for _, topic := range c.topics {
			_ = topic.Close()
		}
		c.mu.Unlock()

		// Close services
		if c.mdnsService != nil {
			_ = c.mdnsService.Close()
		}
		if c.dht != nil {
			_ = c.dht.Close()
		}
		_ = c.host.Close()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(closeTimeout):
		c.logger.Warnf("Clean shutdown timed out, forcing exit")
		return nil
	}
}

// Connect connects to a peer using a multiaddr string.
// This bypasses the bootstrap peer mechanism and directly connects to the specified peer.
func (c *client) Connect(ctx context.Context, peerMultiaddr string) error {
	maddr, err := multiaddr.NewMultiaddr(peerMultiaddr)
	if err != nil {
		return fmt.Errorf("invalid multiaddr %s: %w", peerMultiaddr, err)
	}

	addrInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("invalid peer address %s: %w", peerMultiaddr, err)
	}

	if addrInfo.ID == c.host.ID() {
		return nil // skip self
	}

	if err := c.host.Connect(ctx, *addrInfo); err != nil {
		return fmt.Errorf("failed to connect to peer %s: %w", addrInfo.ID.String(), err)
	}

	c.logger.Infof("Connected to static peer: %s", addrInfo.ID.String())
	return nil
}

// Internal methods

func (c *client) waitForDHTAndAdvertise(ctx context.Context, routingDiscovery *drouting.RoutingDiscovery) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(10 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			if c.config.EnableMDNS {
				c.logger.Infof("Timeout waiting for DHT peers - will rely on mDNS and peer cache")
			} else {
				c.logger.Infof("Timeout waiting for DHT peers - will rely on peer cache")
			}
			return
		case <-ticker.C:
			if c.dht != nil && len(c.dht.RoutingTable().ListPeers()) > 0 {
				c.advertiseTopics(ctx, routingDiscovery)
				return
			}
		}
	}
}

func (c *client) advertiseTopics(ctx context.Context, routingDiscovery *drouting.RoutingDiscovery) {
	c.mu.RLock()
	topicsCopy := make([]string, 0, len(c.topics))
	for topic := range c.topics {
		topicsCopy = append(topicsCopy, topic)
	}
	c.mu.RUnlock()

	for _, topic := range topicsCopy {
		if _, err := routingDiscovery.Advertise(ctx, topic); err != nil {
			c.logger.Warnf("Failed to advertise topic %s: %v", topic, err)
		} else {
			c.logger.Infof("Announcing presence on DHT for topic: %s", topic)
		}
	}
}

func (c *client) discoverPeers(ctx context.Context, routingDiscovery *drouting.RoutingDiscovery, runImmediately bool) {
	// Run discovery immediately on startup if requested
	if runImmediately {
		// Small delay to allow topics to be joined
		time.Sleep(1 * time.Second)
		c.findAndConnectPeers(ctx, routingDiscovery)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.discoverNow:
			c.advertiseTopics(ctx, routingDiscovery)
			c.findAndConnectPeers(ctx, routingDiscovery)
		case <-ticker.C:
			c.findAndConnectPeers(ctx, routingDiscovery)
		}
	}
}

func (c *client) findAndConnectPeers(ctx context.Context, routingDiscovery *drouting.RoutingDiscovery) {
	c.mu.RLock()
	topicsCopy := make([]string, 0, len(c.topics))
	for topic := range c.topics {
		topicsCopy = append(topicsCopy, topic)
	}
	c.mu.RUnlock()

	for _, topic := range topicsCopy {
		peerChan, err := routingDiscovery.FindPeers(ctx, topic)
		if err != nil {
			continue
		}
		go c.processPeerDiscovery(ctx, peerChan)
	}
}

func (c *client) processPeerDiscovery(ctx context.Context, peerChan <-chan peer.AddrInfo) {
	discCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for {
		select {
		case <-discCtx.Done():
			return
		case peerInfo, ok := <-peerChan:
			if !ok {
				return
			}
			c.connectToDiscoveredPeer(ctx, peerInfo)
		}
	}
}

func (c *client) connectToDiscoveredPeer(ctx context.Context, peerInfo peer.AddrInfo) {
	if peerInfo.ID == c.host.ID() || len(peerInfo.Addrs) == 0 {
		return
	}

	// ConnectionGater handles private IP filtering, so just try to connect
	if err := c.host.Connect(ctx, peerInfo); err != nil {
		if c.shouldLogConnectionError(err) {
			c.logger.Debugf("Failed to connect to discovered peer %s: %v", peerInfo.ID.String(), err)
		}
	}
}

func (c *client) shouldLogConnectionError(err error) bool {
	errStr := err.Error()
	ignoredErrors := []string{
		"connection refused",
		"rate limit exceeded",
		"NO_RESERVATION",
		"concurrent active dial",
		"all dials failed",
	}

	for _, ignored := range ignoredErrors {
		if strings.Contains(errStr, ignored) {
			return false
		}
	}
	return true
}

// allConnected reports whether every peer in the list is currently connected.
// Used by the maintenance loop's fast-retry phase to decide when the pool has
// converged so we can drop to steady-state cadence.
func (c *client) allConnected(peers []peer.AddrInfo) bool {
	for _, peerInfo := range peers {
		if c.host.Network().Connectedness(peerInfo.ID) != network.Connected {
			return false
		}
	}
	return true
}

// reconnectDisconnected dials any peer in the list that isn't currently
// connected. The kind argument only feeds log messages.
func (c *client) reconnectDisconnected(ctx context.Context, kind string, peers []peer.AddrInfo) {
	for _, peerInfo := range peers {
		if c.host.Network().Connectedness(peerInfo.ID) != network.Connected {
			c.logger.Infof("%s peer %s disconnected, attempting reconnection", kind, peerInfo.ID.String()[:16])
			go func(pi peer.AddrInfo) {
				if err := c.host.Connect(ctx, pi); err != nil {
					c.logger.Warnf("Failed to reconnect to %s peer %s: %v", kind, pi.ID.String()[:16], err)
				} else {
					c.logger.Infof("Successfully reconnected to %s peer %s", kind, pi.ID.String()[:16])
				}
			}(peerInfo)
		}
	}
}

// maintainPeerSet runs the persistent reconnect loop for a managed peer pool
// (bootstrap or static). It checks every 5 seconds for the first two minutes
// to handle simultaneous restarts where the one-shot startup dial fails before
// the remote is listening, then settles to a 30-second steady-state cadence.
func (c *client) maintainPeerSet(ctx context.Context, kind string, peers []peer.AddrInfo) {
	if len(peers) == 0 {
		c.logger.Debugf("No %s peers to maintain", kind)
		return
	}

	c.logger.Infof("Starting %s peer maintenance for %d peers", kind, len(peers))

	fastTicker := time.NewTicker(5 * time.Second)
	fastPhaseEnd := time.After(2 * time.Minute)

fastLoop:
	for {
		select {
		case <-ctx.Done():
			fastTicker.Stop()
			return
		case <-fastPhaseEnd:
			fastTicker.Stop()
			break fastLoop
		case <-fastTicker.C:
			if c.allConnected(peers) {
				fastTicker.Stop()
				c.logger.Infof("All %s peers connected, switching to maintenance mode", kind)
				break fastLoop
			}
			c.reconnectDisconnected(ctx, kind, peers)
		}
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Infof("%s peer maintenance stopping", kind)
			return
		case <-ticker.C:
			c.reconnectDisconnected(ctx, kind, peers)
		}
	}
}

// attemptDirectConnectionsToTopicPeers periodically queries connected peers for
// addresses of topic peers we're not directly connected to.
// This implements a simple peer address exchange by asking bootstrap servers for peer info.
func (c *client) attemptDirectConnectionsToTopicPeers(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	// Track when each peer was last asked for so a lookup that failed (every
	// connected peer timed out or knew nothing) is retried after
	// peerAddressRetryInterval instead of being written off for good.
	requested := make(map[peer.ID]time.Time)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.scanTopicPeersForAddresses(requested, time.Now())
		}
	}
}

// scanTopicPeersForAddresses starts a lookup for every topic peer that is not
// directly connected and is due for one, recording the attempt in requested.
func (c *client) scanTopicPeersForAddresses(requested map[peer.ID]time.Time, now time.Time) {
	allTopicPeers := c.peerTracker.getAllTopicPeers()
	c.logger.Debugf("Scanning %d topic peers for address discovery", len(allTopicPeers))

	topicSet := make(map[peer.ID]struct{}, len(allTopicPeers))
	for _, targetPeer := range allTopicPeers {
		topicSet[targetPeer] = struct{}{}

		// Skip if already connected directly
		if c.host.Network().Connectedness(targetPeer) == network.Connected {
			continue
		}

		if !shouldRequestPeerAddresses(requested, targetPeer, now) {
			continue
		}

		// Request addresses from connected peers
		go c.requestPeerAddresses(targetPeer)
		requested[targetPeer] = now
	}

	pruneRequested(requested, topicSet)
}

// shouldRequestPeerAddresses reports whether targetPeer has never been asked
// for, or was last asked for at least peerAddressRetryInterval ago.
func shouldRequestPeerAddresses(requested map[peer.ID]time.Time, targetPeer peer.ID, now time.Time) bool {
	last, ok := requested[targetPeer]
	return !ok || now.Sub(last) >= peerAddressRetryInterval
}

// pruneRequested drops entries for peers no longer on any subscribed topic so
// the map is bounded by the current topic peer set.
func pruneRequested(requested map[peer.ID]time.Time, topicPeers map[peer.ID]struct{}) {
	for id := range requested {
		if _, ok := topicPeers[id]; !ok {
			delete(requested, id)
		}
	}
}

// tryConnectToTopicPeer attempts to establish a direct connection to a topic peer
// using addresses from the peerstore. This is used when DHT is disabled to implement
// a lightweight peer exchange mechanism for topic peers.
//
// This only works for peers whose addresses are already in the peerstore.
// Without DHT, addresses come from:
// - Direct connections (bootstrap peers)
// - Identify protocol exchanges
// - GossipSub Peer Exchange (PX)
// - Peer cache
//
// For best results with DHT disabled, include all known bootstrap servers in BootstrapPeers.
func (c *client) tryConnectToTopicPeer(peerID peer.ID) {
	// Check if we're already connected
	if c.host.Network().Connectedness(peerID) == network.Connected {
		return
	}

	// Get peer addresses from peerstore
	addrs := c.host.Peerstore().Addrs(peerID)

	peerName := c.peerTracker.getName(peerID)
	c.logger.Debugf("Peer %s (%s): %d addresses in peerstore", peerName, peerID.String()[:16], len(addrs))

	if len(addrs) == 0 {
		// No addresses available - this peer is either:
		// 1. Behind NAT with no public address (relay-only)
		// 2. Not in our bootstrap list and not yet discovered via PX
		// Messages still flow via GossipSub relay through connected peers
		c.logger.Debugf("No addresses for peer %s - continuing via relay", peerName)
		return
	}

	// Log the addresses we have
	for i, addr := range addrs {
		c.logger.Debugf("  Address %d: %s", i+1, addr.String())
	}

	// Attempt connection (ConnectionGater will handle private IP filtering)
	peerInfo := peer.AddrInfo{
		ID:    peerID,
		Addrs: addrs,
	}

	if err := c.host.Connect(c.ctx, peerInfo); err != nil {
		if c.shouldLogConnectionError(err) {
			c.logger.Debugf("Failed to establish direct connection to topic peer %s: %v", peerID.String()[:16], err)
		}
	} else {
		c.logger.Infof("Established direct connection to topic peer %s (%s)", c.peerTracker.getName(peerID), peerID.String()[:16])
	}
}

// handleMalformedMessage records a malformed message from a peer, logs a
// debug entry with diagnostic context, and emits a single WARN when the peer
// first crosses the malformed-message threshold. Subsequent messages from
// peers past the threshold should be dropped by the caller via
// peerTracker.shouldSkipMalformed without invoking this helper.
func (c *client) handleMalformedMessage(author peer.ID, topicName string, data []byte, err error) {
	preview := data
	if len(preview) > 128 {
		preview = preview[:128]
	}
	count, justSkipped := c.peerTracker.recordMalformed(author)
	c.logger.Debugf("Malformed message from %s (%s) on topic %s: %v (count=%d, len=%d, preview=%q, hex=%x)",
		c.peerTracker.getName(author), author.String(), topicName, err, count, len(data), preview, data)
	if justSkipped {
		c.logger.Warnf("Skipping peer %s (%s) on topic %s: %d malformed messages exceeded threshold",
			c.peerTracker.getName(author), author.String(), topicName, count)
	}
}

func (c *client) receiveMessages(sub *pubsub.Subscription, topic *pubsub.Topic, msgChan chan Message) {
	for {
		msg, err := sub.Next(c.ctx)
		if err != nil {
			// Both ways Next can fail are terminal: the client context is
			// cancelled, or the subscription itself was cancelled, in which
			// case Next keeps returning ErrSubscriptionCancelled immediately.
			// Retrying the latter would spin at 100% CPU and, since Close
			// waits for readers, wedge shutdown.
			if c.ctx.Err() != nil || errors.Is(err, pubsub.ErrSubscriptionCancelled) {
				return
			}
			c.logger.Errorf("Error reading message: %v", err)
			continue
		}

		author := msg.GetFrom()
		if author == c.host.ID() {
			continue
		}

		// Drop messages from peers that have exceeded the malformed threshold.
		if c.peerTracker.shouldSkipMalformed(author) {
			continue
		}

		// Unmarshal message
		var m struct {
			Name string `json:"name"`
			Data []byte `json:"data"`
		}
		if err := json.Unmarshal(msg.Data, &m); err != nil {
			c.handleMalformedMessage(author, topic.String(), msg.Data, err)
			continue
		}

		// The name is peer-controlled and otherwise bounded only by the pubsub
		// message size; store and surface the sanitized form only.
		name := c.peerTracker.updateName(author, m.Name)
		c.peerTracker.recordMessageFrom(author)

		// Send to channel
		select {
		case msgChan <- Message{
			Topic:     topic.String(),
			From:      name,
			FromID:    author.String(),
			Data:      m.Data,
			Timestamp: time.Now(),
		}:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *client) savePeerCache() {
	// Skip if peer caching is disabled
	if c.config.PeerCacheFile == "" {
		return
	}

	// Collect unique peers from all topics
	peerSet := make(map[peer.ID]struct{})

	c.mu.RLock()
	for _, topic := range c.topics {
		topicPeers := topic.ListPeers()
		for _, p := range topicPeers {
			peerSet[p] = struct{}{}
		}
	}
	c.mu.RUnlock()

	// Load existing cache to preserve LastSeen for peers not currently connected
	// Use negative TTL to disable eviction when loading for merge
	existingPeers := loadPeerCache(c.config.PeerCacheFile, -1, c.logger)
	existingMap := make(map[string]time.Time)
	for _, ep := range existingPeers {
		existingMap[ep.ID] = ep.LastSeen
	}

	var cachedPeers []cachedPeer
	now := time.Now()

	for p := range peerSet {
		if conns := c.host.Network().ConnsToPeer(p); len(conns) > 0 {
			var addrs []string
			for _, conn := range conns {
				addrs = append(addrs, conn.RemoteMultiaddr().String())
			}
			cachedPeers = append(cachedPeers, cachedPeer{
				ID:       p.String(),
				Name:     c.peerTracker.getName(p),
				Addrs:    addrs,
				LastSeen: now,
			})
		}
	}

	if len(cachedPeers) > 0 {
		savePeerCache(cachedPeers, c.config.PeerCacheFile, c.logger)
	}
}

// requestPeerAddresses asks connected peers if they know addresses for a target
// peer. At most one lookup per target runs at a time; a call that finds one in
// flight returns immediately.
func (c *client) requestPeerAddresses(targetPeerID peer.ID) {
	if !c.beginAddrLookup(targetPeerID) {
		return
	}
	defer c.endAddrLookup(targetPeerID)

	peerName := c.peerTracker.getName(targetPeerID)

	// Get all directly connected peers
	connectedPeers := c.host.Network().Peers()

	c.logger.Debugf("Requesting addresses for peer %s from %d connected peers", peerName, len(connectedPeers))

	for _, connectedPeer := range connectedPeers {
		if c.ctx.Err() != nil {
			return
		}

		// Skip self
		if connectedPeer == c.host.ID() {
			continue
		}

		// Open stream to request peer addresses, preferring the half-close
		// framing and falling back to 1.0.0 for older peers.
		stream, err := c.host.NewStream(c.ctx, connectedPeer,
			protocol.ID(peerAddressRequestProtocolV2), protocol.ID(peerAddressRequestProtocol))
		if err != nil {
			// Only log if it's not a "protocol not supported" error
			if !strings.Contains(err.Error(), "protocols not supported") {
				c.logger.Debugf("Failed to open peer-addr-request stream to %s: %v", connectedPeer.String()[:16], err)
			}
			continue
		}

		addrs, err := c.exchangePeerAddressRequest(stream, targetPeerID)
		if err != nil {
			c.logger.Debugf("Peer address request to %s for %s failed: %v", connectedPeer.String()[:16], peerName, err)
			continue
		}

		c.logger.Debugf("Parsed response: %d addresses for %s", len(addrs), peerName)

		if len(addrs) > 0 {
			c.logger.Infof("Peer %s shared %d addresses for %s", connectedPeer.String()[:16], len(addrs), peerName)

			// Add addresses to peerstore
			maddrs := parseMultiaddrs(addrs[:min(len(addrs), maxSharedPeerAddresses)])
			c.host.Peerstore().AddAddrs(targetPeerID, maddrs, peerstore.PermanentAddrTTL)

			// Try to connect
			go c.tryConnectToTopicPeer(targetPeerID)
			return // Success, no need to ask other peers
		}
	}

	c.logger.Debugf("No connected peers had addresses for %s", peerName)
}

// beginAddrLookup marks targetPeerID as being looked up and reports whether
// the caller won that right.
func (c *client) beginAddrLookup(targetPeerID peer.ID) bool {
	c.addrLookupsMu.Lock()
	defer c.addrLookupsMu.Unlock()
	if _, inFlight := c.addrLookupsInFlight[targetPeerID]; inFlight {
		return false
	}
	c.addrLookupsInFlight[targetPeerID] = struct{}{}
	return true
}

func (c *client) endAddrLookup(targetPeerID peer.ID) {
	c.addrLookupsMu.Lock()
	defer c.addrLookupsMu.Unlock()
	delete(c.addrLookupsInFlight, targetPeerID)
}

// exchangePeerAddressRequest sends targetPeerID over an open peer-address
// stream and returns the responder's address list. The whole exchange runs
// under one deadline so a responder that never answers cannot pin the caller,
// and the response is size-bounded. The stream is closed on success and reset
// on any failure so the responder learns the exchange was abandoned.
func (c *client) exchangePeerAddressRequest(stream network.Stream, targetPeerID peer.ID) ([]string, error) {
	addrs, err := c.doPeerAddressExchange(stream, targetPeerID)
	if err != nil {
		_ = stream.Reset()
		return nil, err
	}
	_ = stream.Close()
	return addrs, nil
}

func (c *client) doPeerAddressExchange(stream network.Stream, targetPeerID peer.ID) ([]string, error) {
	if err := stream.SetDeadline(time.Now().Add(c.peerAddrStreamTimeout)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	if _, err := stream.Write([]byte(targetPeerID.String())); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Half-close so a 1.1.0 responder sees EOF. A 1.0.0 responder has already
	// received the data in its single read, so this is harmless there.
	if err := stream.CloseWrite(); err != nil {
		return nil, fmt.Errorf("close write: %w", err)
	}

	// The responder closes its side once the JSON is written, so read to EOF.
	raw, err := io.ReadAll(io.LimitReader(stream, maxPeerAddressResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(raw) > maxPeerAddressResponseBytes {
		return nil, errPeerAddressResponseTooLarge
	}

	var addrs []string
	if err := json.Unmarshal(raw, &addrs); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return addrs, nil
}

// handlePeerAddressRequest handles incoming requests for peer addresses.
//
// Everything the requester sends is untrusted: the read is bounded in both
// size and time, requests are rate limited per peer, and request bytes are
// never echoed into the log. Per-request logging is at debug level; the first
// rejection of a peer per rate-limit window is logged once at warn level.
func (c *client) handlePeerAddressRequest(stream network.Stream) {
	remote := stream.Conn().RemotePeer()

	if allowed, firstRejection := c.peerAddrLimiter.allow(remote, time.Now()); !allowed {
		if firstRejection {
			c.logger.Warnf("Peer %s exceeded %d peer address requests per %s; resetting further requests this window", remote.String()[:16], peerAddressRequestsPerWindow, peerAddressRequestWindow)
		}
		_ = stream.Reset()
		return
	}

	// Reset on failure so the requester sees an error rather than a clean EOF.
	if err := c.servePeerAddressRequest(stream, remote); err != nil {
		c.logger.Debugf("Rejected peer address request from %s: %v", remote.String()[:16], err)
		_ = stream.Reset()
		return
	}
	_ = stream.Close()
}

// servePeerAddressRequest reads one request from stream and writes the reply.
func (c *client) servePeerAddressRequest(stream network.Stream, remote peer.ID) error {
	if err := stream.SetDeadline(time.Now().Add(c.peerAddrStreamTimeout)); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}

	c.logger.Debugf("Received peer address request from %s", remote.String()[:16])

	requestedPeerID, err := readPeerIDRequest(stream, stream.Protocol())
	if err != nil {
		return err
	}

	// Only share direct addresses, never relay circuits.
	addrStrs := c.directPeerAddresses(requestedPeerID)

	c.logger.Debugf("Peer address request from %s for %s: sharing %d direct addresses",
		c.peerTracker.getName(remote), c.peerTracker.getName(requestedPeerID), len(addrStrs))

	response, err := json.Marshal(addrStrs)
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	if _, err := stream.Write(response); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	return nil
}

// readPeerIDRequest reads and decodes the encoded peer ID of a request.
//
// A 1.1.0 requester half-closes after writing, so the ID is read to EOF and
// fragmentation is handled. A 1.0.0 requester does not half-close and the
// request carries no length prefix, so the legacy framing is one read: the ID
// is written in a single call and yamux delivers that frame atomically, so in
// practice the read returns the whole ID. Should a legacy request ever arrive
// fragmented, the bytes are never accumulated speculatively, because a prefix
// of a base58 peer ID can itself decode as a different, valid
// identity-multihash peer ID; every such prefix is shorter than any real peer
// ID and is rejected by the minPeerIDBytes check, so the request fails rather
// than being answered for the wrong peer.
func readPeerIDRequest(r io.Reader, proto protocol.ID) (peer.ID, error) {
	var (
		raw []byte
		err error
	)
	if proto == peerAddressRequestProtocolV2 {
		raw, err = io.ReadAll(io.LimitReader(r, maxPeerIDRequestBytes+1))
	} else {
		raw, err = readLegacyPeerIDRequest(r)
	}
	if err != nil {
		return "", err
	}
	if len(raw) > maxPeerIDRequestBytes {
		return "", errPeerIDRequestTooLarge
	}

	id, err := peer.Decode(string(raw))
	if err != nil || len(id) < minPeerIDBytes {
		// Never echo request bytes: they are attacker-controlled.
		return "", fmt.Errorf("%w (%d bytes)", errPeerIDRequestInvalid, len(raw))
	}
	return id, nil
}

// readLegacyPeerIDRequest performs the single bounded read of the 1.0.0 framing.
func readLegacyPeerIDRequest(r io.Reader) ([]byte, error) {
	buf := make([]byte, maxPeerIDRequestBytes+1)
	n, err := r.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

// directPeerAddresses returns up to maxSharedPeerAddresses peerstore addresses
// for id, excluding relay circuit addresses. The result is never nil so it
// marshals as "[]".
func (c *client) directPeerAddresses(id peer.ID) []string {
	addrs := c.host.Peerstore().Addrs(id)
	direct := make([]string, 0, min(len(addrs), maxSharedPeerAddresses))
	for _, addr := range addrs {
		if len(direct) == maxSharedPeerAddresses {
			break
		}
		s := addr.String()
		if strings.Contains(s, "/p2p-circuit") {
			continue
		}
		direct = append(direct, s)
	}
	return direct
}

// Helper functions

type discoveryNotifee struct {
	h      host.Host
	ctx    context.Context //nolint:containedctx // Required for peer connection in callback
	logger logger
}

func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if n.h.ID() == pi.ID {
		return
	}

	if err := n.h.Connect(n.ctx, pi); err == nil {
		n.logger.Infof("Connected to peer: %s", pi.ID.String())
	}
}

func loadPeerCache(cacheFile string, ttl time.Duration, logger logger) []cachedPeer {
	// #nosec G304 -- cacheFile is from config, not user input
	file, err := os.Open(cacheFile)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warnf("Failed to open peer cache: %v", err)
		}
		return nil
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			logger.Warnf("Failed to close peer cache file: %v", closeErr)
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		logger.Warnf("Failed to read peer cache: %v", err)
		return nil
	}

	var peers []cachedPeer
	if err := json.Unmarshal(data, &peers); err != nil {
		logger.Warnf("Failed to parse peer cache: %v", err)
		return nil
	}

	// Skip eviction if TTL is negative
	if ttl < 0 {
		return peers
	}

	return evictStalePeers(peers, ttl, logger)
}

func evictStalePeers(peers []cachedPeer, ttl time.Duration, logger logger) []cachedPeer {
	now := time.Now()
	threshold := now.Add(-ttl)
	validPeers := make([]cachedPeer, 0, len(peers))
	evicted := 0

	for _, p := range peers {
		if p.LastSeen.IsZero() {
			// Safely truncate peer ID for logging
			peerID := p.ID
			if len(peerID) > 16 {
				peerID = peerID[:16]
			}
			logger.Debugf("Peer %s has no LastSeen timestamp, keeping for now", peerID)
			validPeers = append(validPeers, p)
		} else if p.LastSeen.After(threshold) {
			validPeers = append(validPeers, p)
		} else {
			evicted++
		}
	}

	if evicted > 0 {
		logger.Infof("Evicted %d stale peers (not seen for > %v)", evicted, ttl)
	}

	return validPeers
}

func savePeerCache(peers []cachedPeer, cacheFile string, logger logger) {
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		logger.Warnf("Failed to marshal peer cache: %v", err)
		return
	}

	if err := os.WriteFile(cacheFile, data, 0o600); err != nil {
		logger.Warnf("Failed to write peer cache: %v", err)
	}
}

func parseMultiaddrs(addrs []string) []multiaddr.Multiaddr {
	maddrs := make([]multiaddr.Multiaddr, 0, len(addrs))
	for _, addrStr := range addrs {
		maddr, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			continue
		}
		maddrs = append(maddrs, maddr)
	}
	return maddrs
}

func connectToCachedPeers(ctx context.Context, h host.Host, cachedPeers []cachedPeer, logger logger) {
	for _, cp := range cachedPeers {
		peerID, err := peer.Decode(cp.ID)
		if err != nil {
			logger.Warnf("Invalid cached peer ID %s: %v", cp.ID, err)
			continue
		}

		if h.Network().Connectedness(peerID) == network.Connected {
			continue
		}

		maddrs := parseMultiaddrs(cp.Addrs)
		if len(maddrs) == 0 {
			continue
		}

		addrInfo := peer.AddrInfo{
			ID:    peerID,
			Addrs: maddrs,
		}

		go func(ai peer.AddrInfo, name string) {
			if err := h.Connect(ctx, ai); err == nil {
				logger.Infof("Reconnected to cached peer: %s [%s]", name, ai.ID.String())
			} else {
				logger.Warnf("Failed to reconnect to cached peer %s [%s]: %v", name, ai.ID.String(), err)
			}
		}(addrInfo, cp.Name)
	}
}

// GeneratePrivateKey generates a new Ed25519 private key.
// Use this function to create a new key for Config.PrivateKey when setting up a new peer.
func GeneratePrivateKey() (crypto.PrivKey, error) {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	return priv, nil
}

// PrivateKeyToHex converts a private key to a hex string for storage.
func PrivateKeyToHex(priv crypto.PrivKey) (string, error) {
	keyBytes, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("failed to marshal key: %w", err)
	}
	return hex.EncodeToString(keyBytes), nil
}

// PrivateKeyFromHex loads a private key from a hex string.
func PrivateKeyFromHex(keyHex string) (crypto.PrivKey, error) {
	keyBytes, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex key: %w", err)
	}

	priv, err := crypto.UnmarshalPrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal private key: %w", err)
	}

	return priv, nil
}
