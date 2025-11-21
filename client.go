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

	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
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
)

const (
	// peerAddressRequestProtocol is the protocol ID for requesting peer addresses
	peerAddressRequestProtocol = "/p2p-msg-bus/peer-addr-request/1.0.0"
)

var (
	// ErrNameRequired is returned when Config.Name is not provided.
	ErrNameRequired = errors.New("config.Name is required")
	// ErrPrivateKeyRequired is returned when Config.PrivateKey is not provided.
	ErrPrivateKeyRequired = errors.New("config.PrivateKey is required")
)

// Compile-time check to ensure client implements Client interface
var _ Client = (*client)(nil)

// client represents a P2P messaging client implementation.
type client struct {
	config      Config
	host        host.Host
	dht         *dht.IpfsDHT
	pubsub      *pubsub.PubSub
	topics      map[string]*pubsub.Topic
	subs        map[string]*pubsub.Subscription
	msgChans    map[string]chan Message
	mu          sync.RWMutex
	peerTracker *peerTracker
	ctx         context.Context //nolint:containedctx // Client manages its own lifecycle
	cancel      context.CancelFunc
	mdnsService mdns.Service
	logger      logger
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

	// Connect to bootstrap peers (which are also used as relay peers)
	connectToBootstrapPeers(ctx, h, bootstrapPeers, clientLogger)
	loadAndConnectCachedPeers(ctx, h, config, clientLogger)

	// Create pubsub
	ps, err := pubsub.NewGossipSub(ctx, h)
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

	c := &client{
		config:      config,
		host:        h,
		dht:         kadDHT,
		pubsub:      ps,
		topics:      make(map[string]*pubsub.Topic),
		subs:        make(map[string]*pubsub.Subscription),
		msgChans:    make(map[string]chan Message),
		peerTracker: newPeerTracker(),
		ctx:         ctx,
		cancel:      cancel,
		mdnsService: mdnsService,
		logger:      clientLogger,
	}

	// Set up peer address request/response protocol handler
	// This allows other peers to request addresses from us (useful for DHT-off clients)
	h.SetStreamHandler(protocol.ID(peerAddressRequestProtocol), c.handlePeerAddressRequest)
	clientLogger.Infof("Peer address request protocol handler installed")

	// Start DHT discovery (only if DHT is enabled)
	if kadDHT != nil {
		routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)
		go c.waitForDHTAndAdvertise(ctx, routingDiscovery)
		go c.discoverPeers(ctx, routingDiscovery, true)
	} else {
		clientLogger.Infof("Skipping DHT discovery - peer discovery will happen via GossipSub topic mesh only")
		clientLogger.Infof("Topic peer exchange enabled - will attempt direct connections to discovered peers")
		// Periodically check for new peer addresses learned via PX
		go c.attemptDirectConnectionsToTopicPeers(ctx)
		// Maintain bootstrap peer connections in DHT off mode
		go c.maintainBootstrapConnections(ctx, bootstrapPeers)
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
	hostOpts = append(hostOpts,
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", config.Port),
			fmt.Sprintf("/ip6/::/tcp/%d", config.Port),
		),
	)

	// Enable NAT features only if explicitly enabled
	// UPnP/NAT-PMP scans the local gateway which triggers network scanning alerts
	if config.EnableNAT {
		hostOpts = append(hostOpts,
			libp2p.NATPortMap(),
			libp2p.EnableNATService(),
			libp2p.EnableHolePunching(),
		)
		log.Infof("UPnP/NAT-PMP enabled (will scan local gateway for port mapping)")
	} else {
		log.Infof("UPnP/NAT-PMP disabled (production safe default)")
	}

	hostOpts = append(hostOpts,
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

	// If server mode and custom cleanup interval specified, configure ProviderManager
	if mode == dht.ModeServer && config.DHTCleanupInterval > 0 {
		log.Infof("Configuring DHT cleanup interval: %v", config.DHTCleanupInterval)

		// Create an in-memory datastore for the provider manager
		// Same as default DHT datastore creation
		datastore := dssync.MutexWrap(ds.NewMapDatastore())

		providerManager, err := records.NewProviderManager(
			ctx,
			h.ID(),
			h.Peerstore(),
			datastore,
			records.CleanupInterval(config.DHTCleanupInterval),
		)
		if err != nil {
			_ = h.Close()
			cancel()
			return nil, fmt.Errorf("failed to create provider manager: %w", err)
		}
		dhtOpts = append(dhtOpts, dht.ProviderStore(providerManager))
	}

	kadDHT, err := dht.New(ctx, h, dhtOpts...)
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

// parsePeerMultiaddrs is a shared helper to parse bootstrap peer multiaddr strings
func parsePeerMultiaddrs(peerConfigs []string, log logger) []peer.AddrInfo {
	if len(peerConfigs) == 0 {
		return nil
	}

	peers := make([]peer.AddrInfo, 0, len(peerConfigs))
	for _, peerStr := range peerConfigs {
		maddr, err := multiaddr.NewMultiaddr(peerStr)
		if err != nil {
			log.Errorf("Invalid bootstrap address %s: %v (hint: use /dns4/ for hostnames, /ip4/ for IP addresses)", peerStr, err)
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

func connectToBootstrapPeers(ctx context.Context, h host.Host, peers []peer.AddrInfo, log logger) {
	for _, peerInfo := range peers {
		go func(pi peer.AddrInfo) {
			if connectErr := h.Connect(ctx, pi); connectErr == nil {
				log.Infof("Connected to bootstrap peer: %s", pi.ID.String())
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
// The returned channel will be closed when the client is closed.
func (c *client) Subscribe(topic string) <-chan Message {
	msgChan := make(chan Message, 100)

	c.logger.Debugf("Subscribing to topic: %s", topic)

	c.mu.Lock()
	c.msgChans[topic] = msgChan
	c.mu.Unlock()

	go func() {
		// Join or get existing topic
		c.mu.Lock()
		t, ok := c.topics[topic]
		c.mu.Unlock()

		if !ok {
			var err error
			t, err = c.pubsub.Join(topic)
			if err != nil {
				c.logger.Errorf("Failed to join topic %s: %v", topic, err)
				close(msgChan)
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
			close(msgChan)
			return
		}

		c.mu.Lock()
		c.subs[topic] = sub
		c.mu.Unlock()

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

// Close shuts down the client and releases all resources.
func (c *client) Close() error {
	c.cancel()

	done := make(chan struct{})
	go func() {
		c.mu.Lock()
		// Close all message channels
		for _, ch := range c.msgChans {
			close(ch)
		}

		// Cancel all subscriptions
		for _, sub := range c.subs {
			sub.Cancel()
		}

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
	case <-time.After(2 * time.Second):
		c.logger.Warnf("Clean shutdown timed out, forcing exit")
		return nil
	}
}

// Internal methods

//nolint:gocyclo // DHT advertising logic complexity is reasonable
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
				// Advertise on DHT for all topics
				c.mu.RLock()
				topicsCopy := make([]string, 0, len(c.topics))
				for topic := range c.topics {
					topicsCopy = append(topicsCopy, topic)
				}
				c.mu.RUnlock()

				for _, topic := range topicsCopy {
					_, err := routingDiscovery.Advertise(ctx, topic)
					if err != nil {
						c.logger.Warnf("Failed to advertise topic %s: %v", topic, err)
					} else {
						c.logger.Infof("Announcing presence on DHT for topic: %s", topic)
					}
				}
				return
			}
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

// maintainBootstrapConnections periodically checks bootstrap peer connections
// and reconnects if they become disconnected. This is critical in DHT off mode
// where bootstrap peers are the only way to stay connected to the network.
func (c *client) maintainBootstrapConnections(ctx context.Context, bootstrapPeers []peer.AddrInfo) {
	if len(bootstrapPeers) == 0 {
		c.logger.Debugf("No bootstrap peers to maintain")
		return
	}

	c.logger.Infof("Starting bootstrap peer maintenance for %d peers", len(bootstrapPeers))
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Infof("Bootstrap peer maintenance stopping")
			return
		case <-ticker.C:
			for _, peerInfo := range bootstrapPeers {
				// Check if we're still connected to this bootstrap peer
				if c.host.Network().Connectedness(peerInfo.ID) != network.Connected {
					c.logger.Infof("Bootstrap peer %s disconnected, attempting reconnection", peerInfo.ID.String()[:16])
					go func(pi peer.AddrInfo) {
						if err := c.host.Connect(ctx, pi); err != nil {
							c.logger.Warnf("Failed to reconnect to bootstrap peer %s: %v", pi.ID.String()[:16], err)
						} else {
							c.logger.Infof("Successfully reconnected to bootstrap peer %s", pi.ID.String()[:16])
						}
					}(peerInfo)
				}
			}
		}
	}
}

// attemptDirectConnectionsToTopicPeers periodically queries connected peers for
// addresses of topic peers we're not directly connected to.
// This implements a simple peer address exchange by asking bootstrap servers for peer info.
func (c *client) attemptDirectConnectionsToTopicPeers(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	// Track peers we've already requested to avoid spam
	requested := make(map[peer.ID]bool)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			allTopicPeers := c.peerTracker.getAllTopicPeers()
			c.logger.Debugf("Scanning %d topic peers for address discovery", len(allTopicPeers))

			for _, targetPeer := range allTopicPeers {
				// Skip if already connected directly
				if c.host.Network().Connectedness(targetPeer) == network.Connected {
					continue
				}

				// Skip if already requested
				if requested[targetPeer] {
					continue
				}

				// Request addresses from connected peers
				go c.requestPeerAddresses(targetPeer)
				requested[targetPeer] = true
			}
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

func (c *client) receiveMessages(sub *pubsub.Subscription, topic *pubsub.Topic, msgChan chan Message) {
	for {
		msg, err := sub.Next(c.ctx)
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			c.logger.Errorf("Error reading message: %v", err)
			continue
		}

		author := msg.GetFrom()
		if author == c.host.ID() {
			continue
		}

		// Unmarshal message
		var m struct {
			Name string `json:"name"`
			Data []byte `json:"data"`
		}
		if err := json.Unmarshal(msg.Data, &m); err != nil {
			c.logger.Errorf("Error unmarshaling message: %v", err)
			continue
		}

		c.peerTracker.updateName(author, m.Name)
		c.peerTracker.recordMessageFrom(author)

		// Send to channel
		select {
		case msgChan <- Message{
			Topic:     topic.String(),
			From:      m.Name,
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

// requestPeerAddresses asks connected peers if they know addresses for a target peer
func (c *client) requestPeerAddresses(targetPeerID peer.ID) {
	peerName := c.peerTracker.getName(targetPeerID)

	// Get all directly connected peers
	connectedPeers := c.host.Network().Peers()

	c.logger.Debugf("Requesting addresses for peer %s from %d connected peers", peerName, len(connectedPeers))

	for _, connectedPeer := range connectedPeers {
		// Skip self
		if connectedPeer == c.host.ID() {
			continue
		}

		// Open stream to request peer addresses
		stream, err := c.host.NewStream(c.ctx, connectedPeer, protocol.ID(peerAddressRequestProtocol))
		if err != nil {
			// Only log if it's not a "protocol not supported" error
			if !strings.Contains(err.Error(), "protocols not supported") {
				c.logger.Debugf("Failed to open peer-addr-request stream to %s: %v", connectedPeer.String()[:16], err)
			}
			continue
		}

		// Send the peer ID we're looking for
		_, err = stream.Write([]byte(targetPeerID.String()))
		if err != nil {
			_ = stream.Close()
			c.logger.Debugf("Failed to write to stream: %v", err)
			continue
		}

		c.logger.Debugf("Sent address request for %s to %s", peerName, connectedPeer.String()[:16])

		// Read response
		buf := make([]byte, 4096)
		n, err := stream.Read(buf)
		_ = stream.Close()

		if err != nil && !errors.Is(err, io.EOF) {
			c.logger.Debugf("Failed to read response from %s: %v", connectedPeer.String()[:16], err)
			continue
		}

		c.logger.Debugf("Received %d bytes response from %s", n, connectedPeer.String()[:16])

		// Parse response (JSON array of multiaddrs)
		var addrs []string
		if err := json.Unmarshal(buf[:n], &addrs); err != nil {
			c.logger.Debugf("Failed to parse response: %v (data: %s)", err, string(buf[:n]))
			continue
		}

		c.logger.Debugf("Parsed response: %d addresses for %s", len(addrs), peerName)

		if len(addrs) > 0 {
			c.logger.Infof("Peer %s shared %d addresses for %s", connectedPeer.String()[:16], len(addrs), peerName)

			// Add addresses to peerstore
			maddrs := parseMultiaddrs(addrs)
			c.host.Peerstore().AddAddrs(targetPeerID, maddrs, peerstore.PermanentAddrTTL)

			// Try to connect
			go c.tryConnectToTopicPeer(targetPeerID)
			return // Success, no need to ask other peers
		}
	}

	c.logger.Debugf("No connected peers had addresses for %s", peerName)
}

// handlePeerAddressRequest handles incoming requests for peer addresses
func (c *client) handlePeerAddressRequest(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	c.logger.Infof("Received peer address request from %s", stream.Conn().RemotePeer().String()[:16])

	// Read the requested peer ID
	buf := make([]byte, 256)
	n, err := stream.Read(buf)
	if err != nil {
		c.logger.Errorf("Failed to read peer ID from request: %v", err)
		return
	}

	requestedPeerIDStr := string(buf[:n])
	c.logger.Debugf("Request for peer ID: %s", requestedPeerIDStr)

	requestedPeerID, err := peer.Decode(requestedPeerIDStr)
	if err != nil {
		c.logger.Errorf("Failed to decode peer ID: %v", err)
		return
	}

	requesterName := c.peerTracker.getName(stream.Conn().RemotePeer())
	requestedName := c.peerTracker.getName(requestedPeerID)

	// Check if we have addresses for this peer
	addrs := c.host.Peerstore().Addrs(requestedPeerID)

	c.logger.Infof("Peer address request from %s for %s: found %d addresses in peerstore",
		requesterName, requestedName, len(addrs))

	if len(addrs) == 0 {
		// No addresses - send empty response
		_, _ = stream.Write([]byte("[]"))
		return
	}

	// Filter out relay circuit addresses - only share direct addresses
	directAddrs := make([]multiaddr.Multiaddr, 0)
	for _, addr := range addrs {
		// Skip relay circuit addresses
		if strings.Contains(addr.String(), "/p2p-circuit") {
			continue
		}
		directAddrs = append(directAddrs, addr)
	}

	if len(directAddrs) == 0 {
		c.logger.Debugf("No direct addresses for %s (only relay circuits)", requestedName)
		_, _ = stream.Write([]byte("[]"))
		return
	}

	// Convert to string array
	addrStrs := make([]string, len(directAddrs))
	for i, addr := range directAddrs {
		addrStrs[i] = addr.String()
		c.logger.Debugf("  Direct address %d: %s", i+1, addr.String())
	}

	// Send as JSON
	response, err := json.Marshal(addrStrs)
	if err != nil {
		_, _ = stream.Write([]byte("[]"))
		return
	}

	c.logger.Infof("Sharing %d direct addresses for peer %s with %s", len(directAddrs), requestedName, requesterName)
	_, _ = stream.Write(response)
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
