package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/multiformats/go-multiaddr"
)

// Compile-time check to ensure Client implements P2PClient interface
var _ P2PClient = (*Client)(nil)

// Client represents a P2P messaging client.
type Client struct {
	config      Config
	host        host.Host
	dht         *dht.IpfsDHT
	pubsub      *pubsub.PubSub
	topics      map[string]*pubsub.Topic
	subs        map[string]*pubsub.Subscription
	msgChans    map[string]chan Message
	mu          sync.RWMutex
	peerTracker *peerTracker
	ctx         context.Context
	cancel      context.CancelFunc
	mdnsService mdns.Service
	logger      logger
}

// NewClient creates and initializes a new P2P client.
// It automatically starts the client and begins peer discovery.
func NewClient(config Config) (P2PClient, error) {
	if config.Name == "" {
		return nil, fmt.Errorf("config.Name is required")
	}

	// Use provided logger or default
	logger := config.Logger
	if logger == nil {
		logger = &DefaultLogger{}
		logger.Debugf("Using default logger")
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Validate private key (required)
	if config.PrivateKey == nil {
		cancel()
		return nil, fmt.Errorf("config.PrivateKey is required")
	}

	var hostOpts []libp2p.Option
	hostOpts = append(hostOpts, libp2p.Identity(config.PrivateKey))

	// Configure announce addresses if provided (useful for K8s)
	var announceAddrs []multiaddr.Multiaddr
	if len(config.AnnounceAddrs) > 0 {
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
		logger.Infof("Using custom announce addresses: %v", config.AnnounceAddrs)
	}

	// define address factory to remove all private IPs from being broadcasted
	addressFactory := func(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
		var publicAddrs []multiaddr.Multiaddr
		for _, addr := range addrs {
			// if IP is not private, add it to the list
			if !isPrivateIP(addr) || config.AllowPrivateIPs {
				publicAddrs = append(publicAddrs, addr)
			}
		}
		// If a user specified a broadcast IP append it here
		if len(announceAddrs) > 0 {
			// here we're appending the external facing multiaddr we created above to the addressFactory so it will be broadcast out when I connect to a bootstrap node.
			publicAddrs = append(publicAddrs, announceAddrs...)
		}

		// If we still don't have any advertisable addresses then attempt to grab it from `https://ifconfig.me/ip`
		if len(publicAddrs) == 0 {
			// If no public addresses are set, let's attempt to grab it publicly
			// Ignore errors because we don't care if we can't find it
			ifconfig, err := GetPublicIP(context.Background())
			if err != nil {
				logger.Infof("Failed to get public IP address: %v", err)
			}
			if len(ifconfig) > 0 {
				addr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%d", ifconfig, config.Port))
				if err != nil {
					logger.Infof("Failed to create multiaddr from public IP: %v", err)
				}
				if addr != nil {
					publicAddrs = append(publicAddrs, addr)
				}
			}
		}

		return publicAddrs
	}

	// Create an IP filter to optionally block private network ranges from being dialed
	ipFilter, err := conngater.NewBasicConnectionGater(nil)
	if err != nil {
		return nil, err
	}

	// By default, filter private IPs
	if !config.AllowPrivateIPs {
		// Add private IP blocks to be filtered out
		for _, cidr := range []string{
			"10.0.0.0/8",     // Private network 10.0.0.0 to 10.255.255.255
			"172.16.0.0/12",  // Private network 172.16.0.0 to 172.31.255.255
			"192.168.0.0/16", // Private network 192.168.0.0 to 192.168.255.255
			"127.0.0.0/16",   // Local network
			"100.64.0.0/10",  // Shared Address Space
			"169.254.0.0/16", // Link-local addresses
		} {
			var ipnet *net.IPNet
			var err error
			_, ipnet, err = net.ParseCIDR(cidr)
			if err != nil {
				return nil, err
			}
			err = ipFilter.BlockSubnet(ipnet)
			if err != nil {
				continue
			}
		}
	}

	// Create libp2p host
	hostOpts = append(hostOpts,
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", config.Port), // Listen on all interfaces
			fmt.Sprintf("/ip6/::/tcp/%d", config.Port),
		),
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
		libp2p.EnableRelay(),
		libp2p.EnableAutoNATv2(),
		libp2p.AddrsFactory(addressFactory),
		libp2p.ConnectionGater(ipFilter),
	)

	h, err := libp2p.New(hostOpts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create host: %w", err)
	}

	logger.Infof("P2P Client created. ID: %s", h.ID())
	logger.Infof("Listening on: %v", h.Addrs())

	// Set up DHT with bootstrap peers
	bootstrapPeers := dht.GetDefaultBootstrapPeerAddrInfos()
	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer), dht.BootstrapPeers(bootstrapPeers...))
	if err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}

	if err := kadDHT.Bootstrap(ctx); err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("failed to bootstrap DHT: %w", err)
	}

	// Connect to bootstrap peers
	for _, peerInfo := range bootstrapPeers {
		go func(pi peer.AddrInfo) {
			if err := h.Connect(ctx, pi); err == nil {
				log.Printf("Connected to bootstrap peer: %s", pi.ID.String())
			}
		}(peerInfo)
	}

	// Load and connect to cached peers
	if config.PeerCacheFile != "" {
		cachedPeers := loadPeerCache(config.PeerCacheFile, logger)
		if len(cachedPeers) > 0 {
			logger.Infof("Connecting to %d cached peers...", len(cachedPeers))

			connectToCachedPeers(ctx, h, cachedPeers, logger)
		}
	}

	// Create pubsub
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("failed to create pubsub: %w", err)
	}

	// Set up mDNS discovery
	mdnsService := mdns.NewMdnsService(h, "", &discoveryNotifee{h: h, ctx: ctx})
	if err := mdnsService.Start(); err != nil {
		log.Printf("Warning: mDNS failed to start: %v", err)
	} else {
		log.Println("mDNS discovery started")
	}

	client := &Client{
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
		logger:      logger,
	}

	// Start DHT discovery
	routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)
	go client.waitForDHTAndAdvertise(ctx, routingDiscovery)
	go client.discoverPeers(ctx, routingDiscovery)

	return client, nil
}

// Subscribe subscribes to a topic and returns a channel that will receive messages.
// The returned channel will be closed when the client is closed.
func (c *Client) Subscribe(topic string) <-chan Message {
	msgChan := make(chan Message, 100)

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
				log.Printf("Failed to join topic %s: %v", topic, err)
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
			log.Printf("Failed to subscribe to topic %s: %v", topic, err)
			close(msgChan)
			return
		}

		c.mu.Lock()
		c.subs[topic] = sub
		c.mu.Unlock()

		// Set up peer connection notifications for this topic
		c.host.Network().Notify(&network.NotifyBundle{
			ConnectedF: func(n network.Network, conn network.Conn) {
				go func() {
					time.Sleep(500 * time.Millisecond)
					peerID := conn.RemotePeer()
					topicPeers := t.ListPeers()
					for _, tp := range topicPeers {
						if tp == peerID {
							name := c.peerTracker.getName(peerID)
							addr := conn.RemoteMultiaddr().String()
							log.Printf("[CONNECTED] Topic peer %s [%s] %s", peerID.String(), name, addr)

							// Save peer cache
							c.savePeerCache(t)
							return
						}
					}
				}()
			},
			DisconnectedF: func(n network.Network, conn network.Conn) {
				peerID := conn.RemotePeer()
				topicPeers := t.ListPeers()
				for _, tp := range topicPeers {
					if tp == peerID {
						log.Printf("[DISCONNECTED] Lost connection to topic peer %s", peerID.String()[:16])
						return
					}
				}
			},
		})

		// Start receiving messages
		c.receiveMessages(sub, t, msgChan)
	}()

	return msgChan
}

// Publish publishes a message to the specified topic.
func (c *Client) Publish(ctx context.Context, topic string, data []byte) error {
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
func (c *Client) GetPeers() []PeerInfo {
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
func (c *Client) GetID() string {
	return c.host.ID().String()
}

// Close shuts down the client and releases all resources.
func (c *Client) Close() error {
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
			topic.Close()
		}
		c.mu.Unlock()

		// Close services
		if c.mdnsService != nil {
			c.mdnsService.Close()
		}
		c.dht.Close()
		c.host.Close()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		log.Printf("Warning: Clean shutdown timed out, forcing exit")
		return nil
	}
}

// Internal methods

func (c *Client) waitForDHTAndAdvertise(ctx context.Context, routingDiscovery *drouting.RoutingDiscovery) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(10 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			c.logger.Infof("Timeout waiting for DHT peers - will rely on mDNS and peer cache")
			return
		case <-ticker.C:
			if len(c.dht.RoutingTable().ListPeers()) > 0 {
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

func (c *Client) discoverPeers(ctx context.Context, routingDiscovery *drouting.RoutingDiscovery) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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

				go func(ctx context.Context) {
					for peer := range peerChan {
						if peer.ID == c.host.ID() {
							continue
						}
						c.host.Connect(ctx, peer)
					}
				}(ctx)
			}
		}
	}
}

func (c *Client) receiveMessages(sub *pubsub.Subscription, topic *pubsub.Topic, msgChan chan Message) {
	for {
		msg, err := sub.Next(c.ctx)
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			log.Printf("Error reading message: %v", err)
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
			log.Printf("Error unmarshaling message: %v", err)
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

func (c *Client) savePeerCache(topic *pubsub.Topic) {
	// Skip if peer caching is disabled
	if c.config.PeerCacheFile == "" {
		return
	}

	topicPeers := topic.ListPeers()
	var cachedPeers []cachedPeer

	for _, p := range topicPeers {
		if conns := c.host.Network().ConnsToPeer(p); len(conns) > 0 {
			var addrs []string
			for _, conn := range conns {
				addrs = append(addrs, conn.RemoteMultiaddr().String())
			}
			cachedPeers = append(cachedPeers, cachedPeer{
				ID:    p.String(),
				Name:  c.peerTracker.getName(p),
				Addrs: addrs,
			})
		}
	}

	if len(cachedPeers) > 0 {
		savePeerCache(cachedPeers, c.config.PeerCacheFile, c.logger)
	}
}

// Helper functions

type discoveryNotifee struct {
	h   host.Host
	ctx context.Context
}

func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if n.h.ID() == pi.ID {
		return
	}

	if err := n.h.Connect(n.ctx, pi); err == nil {
		log.Printf("Connected to peer: %s", pi.ID.String())
	}
}

func loadPeerCache(cacheFile string, logger logger) []cachedPeer {
	file, err := os.Open(cacheFile)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warnf("Failed to open peer cache: %v", err)
		}
		return nil
	}
	defer file.Close()

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

	return peers
}

func savePeerCache(peers []cachedPeer, cacheFile string, logger logger) {
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		logger.Warnf("Failed to marshal peer cache: %v", err)
		return
	}

	if err := os.WriteFile(cacheFile, data, 0644); err != nil {
		logger.Warnf("Failed to write peer cache: %v", err)
	}
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

		var maddrs []multiaddr.Multiaddr
		for _, addrStr := range cp.Addrs {
			maddr, err := multiaddr.NewMultiaddr(addrStr)
			if err != nil {
				continue
			}
			maddrs = append(maddrs, maddr)
		}

		if len(maddrs) == 0 {
			continue
		}

		addrInfo := peer.AddrInfo{
			ID:    peerID,
			Addrs: maddrs,
		}

		go func(ai peer.AddrInfo) {
			if err := h.Connect(ctx, ai); err == nil {
				logger.Infof("Reconnected to cached peer: %s", ai.ID.String())
			}
		}(addrInfo)
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

// Function to check if an IP address is private
func isPrivateIP(addr multiaddr.Multiaddr) bool {
	ipStr, err := extractIPFromMultiaddr(addr)
	if err != nil {
		return false
	}
	// Check for IPv6 loopback
	if ipStr == "::1" {
		return true
	}

	ip := net.ParseIP(ipStr)
	if ip == nil || ip.To4() == nil {
		return false
	}

	// Define private IPv4 and IPv6 ranges
	privateRanges := []*net.IPNet{
		// IPv4
		{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)},
		{IP: net.ParseIP("172.16.0.0"), Mask: net.CIDRMask(12, 32)},
		{IP: net.ParseIP("192.168.0.0"), Mask: net.CIDRMask(16, 32)},
		{IP: net.ParseIP("127.0.0.0"), Mask: net.CIDRMask(8, 32)},
		// IPv6
		{IP: net.ParseIP("fc00::"), Mask: net.CIDRMask(7, 128)},  // Unique local address
		{IP: net.ParseIP("fe80::"), Mask: net.CIDRMask(10, 128)}, // Link-local unicast
	}

	// Check if the IP falls into any of the private ranges or is loopback (::1)
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}

	return false
}

// Function to extract IP information from a Multiaddr (supports IPv4 and IPv6)
func extractIPFromMultiaddr(addr multiaddr.Multiaddr) (string, error) {
	ip, err := addr.ValueForProtocol(multiaddr.P_IP4)
	if err == nil && ip != "" {
		return ip, nil
	}
	return addr.ValueForProtocol(multiaddr.P_IP6)
}

// GetPublicIP fetches the public IP address from ifconfig.me
func GetPublicIP(ctx context.Context) (string, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			// Force the use of IPv4 by specifying 'tcp4' as the network
			return (&net.Dialer{}).DialContext(ctx, "tcp4", addr)
		},
		TLSHandshakeTimeout: 10 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://ifconfig.me/ip", nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), resp.Body.Close()
}
