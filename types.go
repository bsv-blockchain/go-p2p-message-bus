package p2p

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// P2PClient defines the interface for a P2P messaging client.
type P2PClient interface {
	// Subscribe subscribes to a topic and returns a channel that will receive messages.
	// The returned channel will be closed when the client is closed.
	Subscribe(topic string) <-chan Message

	// Publish publishes a message to the specified topic.
	Publish(ctx context.Context, topic string, data []byte) error

	// GetPeers returns information about all known peers on subscribed topics.
	GetPeers() []PeerInfo

	// GetID returns this peer's ID as a string.
	GetID() string

	// Close shuts down the client and releases all resources.
	Close() error
}

// Message represents a received message from a peer.
type Message struct {
	Topic     string    // The topic this message was received on
	From      string    // The sender's name
	FromID    string    // The sender's peer ID
	Data      []byte    // The message payload
	Timestamp time.Time // When the message was received
}

// PeerInfo contains information about a connected peer.
type PeerInfo struct {
	ID    string   // Peer ID
	Name  string   // Peer name (if known)
	Addrs []string // Peer addresses
}

// Internal types for peer tracking

type cachedPeer struct {
	ID    string   `json:"id"`
	Name  string   `json:"name,omitempty"`
	Addrs []string `json:"addrs"`
}

type peerTracker struct {
	mu           sync.RWMutex
	names        map[peer.ID]string
	relayCount   int
	isRelaying   map[string]bool
	topicPeers   map[peer.ID]bool
	lastSeen     map[peer.ID]time.Time
}

func newPeerTracker() *peerTracker {
	return &peerTracker{
		names:      make(map[peer.ID]string),
		isRelaying: make(map[string]bool),
		topicPeers: make(map[peer.ID]bool),
		lastSeen:   make(map[peer.ID]time.Time),
	}
}

func (pt *peerTracker) updateName(peerID peer.ID, name string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.names[peerID] = name
}

func (pt *peerTracker) getName(peerID peer.ID) string {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	if name, ok := pt.names[peerID]; ok {
		return name
	}
	return "unknown"
}

func (pt *peerTracker) recordRelay(srcPeer, dstPeer peer.ID) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	key := srcPeer.String() + "->" + dstPeer.String()
	if !pt.isRelaying[key] {
		pt.isRelaying[key] = true
		pt.relayCount++
	}
}

func (pt *peerTracker) getRelayCount() int {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.relayCount
}

func (pt *peerTracker) recordMessageFrom(peerID peer.ID) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.topicPeers[peerID] = true
	pt.lastSeen[peerID] = time.Now()
}

func (pt *peerTracker) getAllTopicPeers() []peer.ID {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	peers := make([]peer.ID, 0, len(pt.topicPeers))
	for peerID := range pt.topicPeers {
		peers = append(peers, peerID)
	}
	return peers
}

func (pt *peerTracker) getLastSeen(peerID peer.ID) time.Time {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.lastSeen[peerID]
}
