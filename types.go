package p2p

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Client defines the interface for a P2P messaging client.
type Client interface {
	// Subscribe subscribes to a topic and returns a channel that will receive messages.
	// The returned channel will be closed when the client is closed.
	Subscribe(topic string) <-chan Message

	// Publish publishes a message to the specified topic.
	Publish(ctx context.Context, topic string, data []byte) error

	// GetPeers returns information about all known peers on subscribed topics.
	GetPeers() []PeerInfo

	// GetID returns this peer's ID as a string.
	GetID() string

	// Connect connects to a peer using a multiaddr string (e.g. "/dns/localhost/tcp/9905/p2p/12D3KooW...").
	// This is useful for connecting to static peers that are known ahead of time.
	Connect(ctx context.Context, peerMultiaddr string) error

	// Close shuts down the client and releases all resources.
	Close() error
}

// P2PClient is a type alias for Client, maintained for backward compatibility.
//
//nolint:revive // P2PClient is intentionally named for backward compatibility, stuttering is acceptable
type P2PClient = Client

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
	ID       string    `json:"id"`
	Name     string    `json:"name,omitempty"`
	Addrs    []string  `json:"addrs"`
	LastSeen time.Time `json:"last_seen"`
}

// malformedThreshold is the number of malformed messages tolerated from a peer
// before it is skipped. A single WARN is logged on transition to skipped state;
// subsequent malformed messages from that peer are dropped silently.
const malformedThreshold = 5

type peerTracker struct {
	mu            sync.RWMutex
	names         map[peer.ID]string
	isRelaying    map[string]bool
	topicPeers    map[peer.ID]bool
	lastSeen      map[peer.ID]time.Time
	malformed     map[peer.ID]int
	skipMalformed map[peer.ID]bool
}

func newPeerTracker() *peerTracker {
	return &peerTracker{
		names:         make(map[peer.ID]string),
		isRelaying:    make(map[string]bool),
		topicPeers:    make(map[peer.ID]bool),
		lastSeen:      make(map[peer.ID]time.Time),
		malformed:     make(map[peer.ID]int),
		skipMalformed: make(map[peer.ID]bool),
	}
}

// recordMalformed increments the malformed message count for a peer.
// Returns the new count and whether this call transitioned the peer into
// the skip state (i.e., this is the first time threshold was exceeded).
func (pt *peerTracker) recordMalformed(peerID peer.ID) (count int, justSkipped bool) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.malformed[peerID]++
	c := pt.malformed[peerID]
	if c >= malformedThreshold && !pt.skipMalformed[peerID] {
		pt.skipMalformed[peerID] = true
		return c, true
	}
	return c, false
}

// shouldSkipMalformed reports whether the peer has exceeded the malformed
// threshold and should have its messages dropped silently.
func (pt *peerTracker) shouldSkipMalformed(peerID peer.ID) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.skipMalformed[peerID]
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
