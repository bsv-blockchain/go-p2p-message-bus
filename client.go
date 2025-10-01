package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
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
	"github.com/multiformats/go-multiaddr"
)

// Client represents a P2P messaging client.
type Client struct {
	config      Config
	host        host.Host
	dht         *dht.IpfsDHT
	pubsub      *pubsub.PubSub
	topics      map[string]*pubsub.Topic
	subs        map[string]*pubsub.Subscription
	msgChans    map[string]chan Message
	peerTracker *peerTracker
	ctx         context.Context
	cancel      context.CancelFunc
	mdnsService mdns.Service
	logger      logger
}

// NewClient creates and initializes a new P2P client.
// It automatically starts the client and begins peer discovery.
func NewClient(config Config) (*Client, error) {
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

	// Create libp2p host
	hostOpts = append(hostOpts,
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/0",
			"/ip6/::/tcp/0",
		),
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
		libp2p.EnableRelay(),
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
	c.msgChans[topic] = msgChan

	go func() {
		// Join or get existing topic
		t, ok := c.topics[topic]
		if !ok {
			var err error
			t, err = c.pubsub.Join(topic)
			if err != nil {
				log.Printf("Failed to join topic %s: %v", topic, err)
				close(msgChan)
				return
			}
			c.topics[topic] = t
		}

		// Subscribe to topic
		sub, err := t.Subscribe()
		if err != nil {
			log.Printf("Failed to subscribe to topic %s: %v", topic, err)
			close(msgChan)
			return
		}
		c.subs[topic] = sub

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
func (c *Client) Publish(topic string, data []byte) error {
	t, ok := c.topics[topic]
	if !ok {
		var err error
		t, err = c.pubsub.Join(topic)
		if err != nil {
			return fmt.Errorf("failed to join topic: %w", err)
		}
		c.topics[topic] = t
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

	return t.Publish(c.ctx, msgBytes)
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

// Close shuts down the client and releases all resources.
func (c *Client) Close() error {
	c.cancel()

	done := make(chan struct{})
	go func() {
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
				for topic := range c.topics {
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
			for topic := range c.topics {
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

func keyToHex(priv crypto.PrivKey) string {
	keyBytes, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(keyBytes)
}
