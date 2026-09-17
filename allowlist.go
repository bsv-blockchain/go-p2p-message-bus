package p2p

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// ErrInvalidAllowedPeerID is returned by NewClient when Config.AllowedPeerIDs
// contains an entry that is not a valid libp2p peer ID.
var ErrInvalidAllowedPeerID = errors.New("invalid AllowedPeerIDs entry")

// peerAllowlist is the set of peers whose pubsub messages this node accepts. A
// nil allowlist, or one built from an empty Config.AllowedPeerIDs, is disabled
// and allows every peer.
type peerAllowlist struct {
	allowed map[peer.ID]struct{}
	drops   dropReporter
}

// newPeerAllowlist builds the allowlist from config. When AllowedPeerIDs is
// empty the result is disabled. Otherwise the configured IDs are joined by
// selfID - locally published messages pass through the same validators, so a
// node that does not allow itself cannot publish at all - and by the peer IDs
// of staticPeers, which the config already describes as known trusted peers.
// staticPeers must be the already-parsed result of
// parsePeerMultiaddrs(config.StaticPeers, log): the caller (NewClient) parses
// StaticPeers once for both dialing and the allowlist, so this function does
// not parse it again. BootstrapPeers are deliberately excluded: bootstrap is a
// routing role, not a trust statement, and the default list is public
// infrastructure.
func newPeerAllowlist(config Config, selfID peer.ID, staticPeers []peer.AddrInfo, log logger) (*peerAllowlist, error) {
	if len(config.AllowedPeerIDs) == 0 {
		return &peerAllowlist{}, nil
	}

	allowed := make(map[peer.ID]struct{}, len(config.AllowedPeerIDs)+1)

	for _, entry := range config.AllowedPeerIDs {
		id, err := peer.Decode(entry)
		if err != nil {
			return nil, fmt.Errorf("%w %q: %w", ErrInvalidAllowedPeerID, entry, err)
		}

		allowed[id] = struct{}{}
	}

	allowed[selfID] = struct{}{}

	for _, addrInfo := range staticPeers {
		allowed[addrInfo.ID] = struct{}{}
	}

	// staticPeers only contains peers that parsePeerMultiaddrs successfully
	// resolved: a /dnsaddr/ entry whose DNS lookup fails at startup drops out
	// of that list entirely and, without this, would be silently missing from
	// the allowlist for the whole process lifetime even though its peer ID
	// was available all along. Scan the raw config strings for peers that
	// name their ID directly via a /p2p/ component - no resolution needed -
	// and add those too. A /dnsaddr/ entry with no /p2p/ suffix still only
	// yields an ID through resolution, so it is unaffected by this loop.
	for _, entry := range config.StaticPeers {
		maddr, err := multiaddr.NewMultiaddr(entry)
		if err != nil {
			continue // parsePeerMultiaddrs already logged this
		}

		idStr, err := maddr.ValueForProtocol(multiaddr.P_P2P)
		if err != nil {
			continue // no /p2p/ component; the ID genuinely requires resolution
		}

		id, err := peer.Decode(idStr)
		if err != nil {
			continue
		}

		allowed[id] = struct{}{}
	}

	log.Infof("Peer allowlist enabled: accepting pubsub messages from %d peer(s)", len(allowed))

	return &peerAllowlist{allowed: allowed, drops: dropReporter{start: time.Now()}}, nil
}

// enabled reports whether the allowlist restricts anything. A nil allowlist is
// disabled.
func (a *peerAllowlist) enabled() bool {
	return a != nil && len(a.allowed) > 0
}

// allows reports whether pubsub messages authored by id should be accepted. A
// disabled allowlist allows every peer.
func (a *peerAllowlist) allows(id peer.ID) bool {
	if !a.enabled() {
		return true
	}

	_, ok := a.allowed[id]

	return ok
}

// dropLogInterval bounds the allowlist validator's drop logging to one
// aggregate line per interval, however many messages are dropped in it.
const dropLogInterval = 30 * time.Second

// dropReporter bounds the drop logging on the allowlist rejection path to one
// aggregate line per dropLogInterval, with O(1) memory. It holds only a
// counter, a start-of-life anchor, and an elapsed-time marker - never
// per-peer state. The set of rejected authors on a public network is
// unbounded, so a map keyed by peer (the pattern
// peerTracker.shouldSkipMalformed uses for the bounded, known set of
// currently-connected peers) would be a memory leak here.
//
// Safe for concurrent use: pubsub's validation workers call recordDrop from
// multiple goroutines. Uses sync/atomic only, no mutex, since this sits on
// the message validation hot path.
type dropReporter struct {
	dropped atomic.Uint64
	lastLog atomic.Int64 // nanoseconds since start when the last line was emitted; zero means no line has been emitted yet
	start   time.Time    // anchor for lastLog's elapsed-time comparisons. time.Since(start) reads Go's monotonic clock, so drop logging is immune to wall-clock steps (NTP corrections, manual changes) that would otherwise suppress it for an arbitrary period. newPeerAllowlist sets this to time.Now(); left zero-valued by tests that construct a dropReporter directly, which only shifts the anchor and does not affect correctness.
}

// recordDrop records one dropped message. The counter is incremented
// unconditionally, so a caller that loses the race below never loses a
// count. Once at least dropLogInterval has elapsed since the last emitted
// line, callers race to claim the logging slot with a CompareAndSwap on
// lastLog; exactly one wins per interval, logs the aggregate count dropped
// since then, and resets the counter. Losers simply return - having already
// recorded their drop above.
//
// lastLog's zero value means no line has ever been emitted, and is treated
// as an unconditional first log rather than run through the elapsed-time
// check below: with a monotonic start anchor, elapsed time near the
// reporter's start is itself near zero, so the interval check alone would
// make the very first drop wait out a full dropLogInterval instead of
// logging immediately.
func (r *dropReporter) recordDrop(log logger) {
	r.dropped.Add(1)

	elapsed := time.Since(r.start).Nanoseconds()

	last := r.lastLog.Load()
	if last != 0 && elapsed-last < int64(dropLogInterval) {
		return
	}

	if !r.lastLog.CompareAndSwap(last, elapsed) {
		return // another goroutine already claimed this window
	}

	count := r.dropped.Swap(0)
	log.Debugf("Dropped %d message(s) from non-allowlisted peers since the last report", count)
}
