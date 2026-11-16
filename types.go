package p2p

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Client defines the interface for a P2P messaging client.
type Client interface {
	// Subscribe subscribes to a topic and returns a channel that will receive messages.
	// The returned channel is closed when the client is closed; a Subscribe issued
	// after Close returns an already-closed channel.
	Subscribe(topic string) <-chan Message

	// Publish publishes a message to the specified topic.
	Publish(ctx context.Context, topic string, data []byte) error

	// GetPeers returns the peers that authored a message on a joined topic and
	// are connected now or were heard from within the last hour. At most 10,000
	// such authors are tracked; connected ones are always kept.
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
	From      string    // The sender's name, as sanitized by the receiver (printable, at most 64 bytes)
	FromID    string    // The sender's peer ID
	Data      []byte    // The message payload
	Timestamp time.Time // When the message was received
}

// PeerInfo contains information about a connected peer.
type PeerInfo struct {
	ID    string   // Peer ID
	Name  string   // Peer name (if known), as sanitized by the receiver (printable, at most 64 bytes)
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

// maxPeerNameLen bounds the peer name accepted from a gossip envelope. The
// field is peer-controlled and otherwise limited only by the pubsub message
// size, and it is embedded in log lines, so an unbounded name would turn every
// mention of the peer into a log-amplification channel.
const maxPeerNameLen = 64

// requestWindow counts inbound peer-address requests from one peer within the
// rate-limit window that started at start, rejected ones included.
type requestWindow struct {
	start time.Time
	count int
}

// peerAddressLimiter enforces the per-peer budget of inbound peer-address
// requests. It has its own lock so a request flood never contends with the
// peerTracker lock on the pubsub receive path.
type peerAddressLimiter struct {
	mu      sync.Mutex
	windows map[peer.ID]requestWindow
}

func newPeerAddressLimiter() *peerAddressLimiter {
	return &peerAddressLimiter{windows: make(map[peer.ID]requestWindow)}
}

// allow reports whether peerID may open another peer-address request at now,
// counting the request either way. firstRejection is true only for the first
// request over budget in the current window, so the caller can log it once.
// Expired windows are pruned on every call; the map is bounded by the set of
// peers that made a request within the last window.
func (l *peerAddressLimiter) allow(peerID peer.ID, now time.Time) (allowed, firstRejection bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for id, w := range l.windows {
		if now.Sub(w.start) >= peerAddressRequestWindow {
			delete(l.windows, id)
		}
	}

	w := l.windows[peerID]
	if w.count == 0 {
		w.start = now
	}
	w.count++
	l.windows[peerID] = w

	if w.count > peerAddressRequestsPerWindow {
		return false, w.count == peerAddressRequestsPerWindow+1
	}
	return true, false
}

// maxTrackedPeers and trackedPeerTTL bound the peerTracker. Message authors
// are attacker-chosen (one connection can relay messages from any number of
// freshly minted identities), so without a bound every new author would add
// entries that are never reclaimed. Connected peers are exempt from both.
const (
	maxTrackedPeers = 10_000
	trackedPeerTTL  = time.Hour
)

// peerTracker records what is known about message authors. Every tracked peer
// sits in order, most recently active at the front; a peer is forgotten from
// every map at once when it has been silent for ttl or when the cap forces the
// least recently active one out. Peers isConnected reports as connected are
// never forgotten.
type peerTracker struct {
	mu         sync.RWMutex
	names      map[peer.ID]string
	topicPeers map[peer.ID]bool
	// lastSeen is the last message of any kind, valid or malformed, except that
	// messages from a peer in skipMalformed are dropped before they are
	// recorded: a skipped peer ages out after ttl and is then judged afresh.
	lastSeen      map[peer.ID]time.Time
	malformed     map[peer.ID]int
	skipMalformed map[peer.ID]bool

	order *list.List // of peer.ID
	elems map[peer.ID]*list.Element

	maxPeers    int
	ttl         time.Duration
	now         func() time.Time
	isConnected func(peer.ID) bool
}

func newPeerTracker() *peerTracker {
	return &peerTracker{
		names:         make(map[peer.ID]string),
		topicPeers:    make(map[peer.ID]bool),
		lastSeen:      make(map[peer.ID]time.Time),
		malformed:     make(map[peer.ID]int),
		skipMalformed: make(map[peer.ID]bool),
		order:         list.New(),
		elems:         make(map[peer.ID]*list.Element),
		maxPeers:      maxTrackedPeers,
		ttl:           trackedPeerTTL,
		now:           time.Now,
		isConnected:   func(peer.ID) bool { return false },
	}
}

// touchLocked marks peerID as active now, tracking it if it is new, and evicts
// whatever the cap or TTL no longer allows. Callers hold pt.mu for writing.
func (pt *peerTracker) touchLocked(peerID peer.ID) {
	now := pt.now()
	pt.lastSeen[peerID] = now
	if e, ok := pt.elems[peerID]; ok {
		pt.order.MoveToFront(e)
	} else {
		pt.elems[peerID] = pt.order.PushFront(peerID)
	}
	pt.evictLocked(now, peerID)
}

// evictLocked forgets peers from the back of order while they are expired or
// the tracker is over its cap. A connected peer found there is moved to the
// front instead, so the loop visits each peer at most once per call. keep, the
// peer being touched, is never evicted: reaching it means every other peer is
// connected, and the cap yields.
func (pt *peerTracker) evictLocked(now time.Time, keep peer.ID) {
	for n := pt.order.Len(); n > 0; n-- {
		back := pt.order.Back()
		id := back.Value.(peer.ID)
		if id == keep || (pt.order.Len() <= pt.maxPeers && !pt.expiredLocked(id, now)) {
			return
		}
		if pt.isConnected(id) {
			pt.order.MoveToFront(back)
			continue
		}
		pt.forgetLocked(id)
	}
}

func (pt *peerTracker) expiredLocked(peerID peer.ID, now time.Time) bool {
	return now.Sub(pt.lastSeen[peerID]) >= pt.ttl
}

// forgetLocked removes peerID from every map. Callers hold pt.mu for writing.
func (pt *peerTracker) forgetLocked(peerID peer.ID) {
	if e, ok := pt.elems[peerID]; ok {
		pt.order.Remove(e)
		delete(pt.elems, peerID)
	}
	delete(pt.names, peerID)
	delete(pt.topicPeers, peerID)
	delete(pt.lastSeen, peerID)
	delete(pt.malformed, peerID)
	delete(pt.skipMalformed, peerID)
}

// recordMalformed increments the malformed message count for a peer.
// Returns the new count and whether this call transitioned the peer into
// the skip state (i.e., this is the first time threshold was exceeded).
func (pt *peerTracker) recordMalformed(peerID peer.ID) (count int, justSkipped bool) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.touchLocked(peerID)
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

// updateName stores the sanitized form of a peer-supplied name and returns it.
func (pt *peerTracker) updateName(peerID peer.ID, name string) string {
	name = sanitizePeerName(name)
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.touchLocked(peerID)
	pt.names[peerID] = name
	return name
}

// sanitizePeerName drops non-printable runes (so a name can never break a log
// line) and truncates to maxPeerNameLen bytes on a rune boundary.
func sanitizePeerName(name string) string {
	// Bound the work before decoding: strings.Map expands each invalid byte to
	// a 3-byte replacement rune, and the input is peer-controlled.
	if len(name) > 4*maxPeerNameLen {
		name = name[:4*maxPeerNameLen]
	}

	name = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, name)

	if len(name) <= maxPeerNameLen {
		return name
	}
	cut := maxPeerNameLen
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}

func (pt *peerTracker) getName(peerID peer.ID) string {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	if name, ok := pt.names[peerID]; ok {
		return name
	}
	return "unknown"
}

// recordMessageFrom records a valid message from peerID and reports whether
// it is the first one since the peer was last tracked as an author.
func (pt *peerTracker) recordMessageFrom(peerID peer.ID) (firstAuthored bool) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.touchLocked(peerID)
	firstAuthored = !pt.topicPeers[peerID]
	pt.topicPeers[peerID] = true
	return firstAuthored
}

// isAuthor reports whether peerID is tracked as the author of a valid message.
func (pt *peerTracker) isAuthor(peerID peer.ID) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.topicPeers[peerID]
}

// getAllTopicPeers returns every tracked author of a valid message that is
// connected or has been active within the TTL.
func (pt *peerTracker) getAllTopicPeers() []peer.ID {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	now := pt.now()
	peers := make([]peer.ID, 0, len(pt.topicPeers))
	for peerID := range pt.topicPeers {
		if pt.expiredLocked(peerID, now) && !pt.isConnected(peerID) {
			continue
		}
		peers = append(peers, peerID)
	}
	return peers
}
