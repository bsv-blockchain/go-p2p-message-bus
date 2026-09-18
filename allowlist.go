package p2p

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// ErrInvalidAllowedPublisherID is returned by NewClient when Config.AllowedPublisherIDs
// contains an entry that is not a valid libp2p peer ID.
var ErrInvalidAllowedPublisherID = errors.New("invalid AllowedPublisherIDs entry")

// peerAllowlist is the set of peers whose pubsub messages this node accepts. A
// nil allowlist, or one built from an empty Config.AllowedPublisherIDs, is disabled
// and allows every peer.
type peerAllowlist struct {
	allowed map[peer.ID]struct{}
	drops   dropReporter
}

// newPeerAllowlist builds the allowlist from config. When AllowedPublisherIDs is
// empty the result is disabled. Otherwise the configured IDs are joined by
// selfID - locally published messages pass through the same validators, so a
// node that does not allow itself cannot publish at all - and by the peer IDs
// that config.StaticPeers names directly, which the config already describes
// as known trusted peers.
//
// Every publisher ID comes from text the operator wrote: config.AllowedPublisherIDs
// and the /p2p/ components of config.StaticPeers. Nothing this function reads is
// network-derived, and it deliberately takes no resolved addresses. Feeding it
// the parsePeerMultiaddrs output would let DNS decide who this node trusts:
// a bare /dnsaddr/<host> entry carries no suffix for go-multiaddr-dns to filter
// TXT records against, so every dnsaddr= record the lookup returns is accepted,
// and a hostile resolver, an on-path spoof or a poisoned cache could name any
// peer ID it liked. That would defeat the point of the allowlist, so the ID of
// a static peer that does not name itself is simply never learned - see
// warnStaticPeersWithoutPublisherID.
//
// BootstrapPeers are deliberately excluded too: bootstrap is a routing role,
// not a trust statement, and the default list is public infrastructure.
func newPeerAllowlist(config Config, selfID peer.ID, log logger) (*peerAllowlist, error) {
	if len(config.AllowedPublisherIDs) == 0 {
		// The zero-valued drops field is never used here: enabled() is false for
		// this allowlist (allowed is nil), so scoring.go never registers the
		// validator that would call recordDrop on it.
		return &peerAllowlist{}, nil
	}

	allowed := make(map[peer.ID]struct{}, len(config.AllowedPublisherIDs)+1)

	for _, entry := range config.AllowedPublisherIDs {
		id, err := peer.Decode(entry)
		if err != nil {
			return nil, fmt.Errorf("%w %q: %w", ErrInvalidAllowedPublisherID, entry, err)
		}

		allowed[id] = struct{}{}
	}

	configured := len(allowed)

	allowed[selfID] = struct{}{}
	fromSelf := len(allowed) - configured

	fromStatic := addStaticPublisherIDs(allowed, config.StaticPeers, log)

	// Report the configured count and what the implicit augmentation added
	// separately: a bare total is confusing, since configuring two IDs on a
	// node with one static peer logs four.
	log.Infof("Peer allowlist enabled: accepting pubsub messages from %d peer(s) - %d configured, %d for this node, %d from StaticPeers",
		len(allowed), configured, fromSelf, fromStatic)

	return &peerAllowlist{allowed: allowed, drops: newDropReporter()}, nil
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

// addStaticPublisherIDs adds the peer IDs that the configured static peers name
// to allowed, and returns how many of them were not already present.
//
// rawEntries are the unparsed config.StaticPeers strings, and they are the only
// input: an entry grants publisher trust exactly when it names its own ID via a
// /p2p/ component, which needs no resolution and so cannot be influenced by
// anything on the network. An entry that names no ID contributes nothing and is
// warned about; see newPeerAllowlist for why resolution is not used to fill that
// gap.
func addStaticPublisherIDs(allowed map[peer.ID]struct{}, rawEntries []string, log logger) int {
	before := len(allowed)

	var unnamed []string

	for _, entry := range rawEntries {
		id := publisherIDFromRawMultiaddr(entry)
		if id == "" {
			unnamed = append(unnamed, entry)

			continue
		}

		allowed[id] = struct{}{}
	}

	warnStaticPeersWithoutPublisherID(unnamed, log)

	return len(allowed) - before
}

// publisherIDFromRawMultiaddr extracts the peer ID an unparsed static-peer
// entry names, or "" when the entry names none and its ID can only be learned
// by resolution.
//
// It uses peer.SplitAddr, which is what peer.AddrInfoFromP2pAddr uses on the
// dialing path, so the allowlist and the dialer can never disagree about which
// peer an address denotes. That matters for relayed addresses: in
// /ip4/../p2p/<relay>/p2p-circuit/p2p/<target> the FIRST /p2p/ component is the
// relay and the LAST is the target. SplitAddr takes the last. Taking the first
// - as multiaddr.ValueForProtocol(P_P2P) does - would allowlist the relay,
// which this library also uses as a bootstrap/relay peer, re-admitting exactly
// the public infrastructure the publisher set is meant to exclude, while
// leaving the target the operator actually configured out of it.
func publisherIDFromRawMultiaddr(entry string) peer.ID {
	maddr, err := multiaddr.NewMultiaddr(entry)
	if err != nil {
		return "" // malformed; parsePeerMultiaddrs already logged this
	}

	// An address with no /p2p/ component at all yields the empty peer.ID, which
	// callers must skip rather than insert as a zero-valued key.
	_, id := peer.SplitAddr(maddr)

	return id
}

// warnStaticPeersWithoutPublisherID warns about static peers that contributed no
// ID to the publisher set.
//
// unnamed are the entries carrying no /p2p/ component. Since resolution grants
// no publisher trust, such an entry contributes nothing whether or not its DNS
// lookup succeeded, so every one of them is reported - no attribution by
// elimination, and no configuration in which the warning goes silent.
func warnStaticPeersWithoutPublisherID(unnamed []string, log logger) {
	if len(unnamed) == 0 {
		return
	}

	log.Warnf("Static peer(s) %q contributed no publisher ID: the entry does not end in a /p2p/<id> component, and resolved addresses are deliberately not trusted as publishers - use the /dnsaddr/<host>/p2p/<id> form when the publisher allowlist is in use",
		unnamed)
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
//
// The zero value is NOT supported: construct with newDropReporter.
type dropReporter struct {
	dropped atomic.Uint64
	lastLog atomic.Int64 // nanoseconds since start when the last line was emitted; zero means no line has been emitted yet
	start   time.Time    // anchor for lastLog's elapsed-time comparisons. time.Since(start) reads Go's monotonic clock, so drop logging is immune to wall-clock steps (NTP corrections, manual changes) that would otherwise suppress it for an arbitrary period. Must be set via newDropReporter; see there for why the zero value is not merely a shifted anchor.
}

// newDropReporter returns a reporter anchored to the current time. Always use
// it: a zero-valued dropReporter latches silent after its first line, for the
// life of the process.
//
// The zero time.Time is year 1, so time.Since(start) exceeds time.Duration's
// +-292-year range and time.Time.Sub saturates at math.MaxInt64 rather than
// wrapping. recordDrop's elapsed is then a constant for the life of the
// process: the first call takes the lastLog == 0 sentinel branch and logs,
// stores that constant, and every later call computes elapsed-last == 0 <
// dropLogInterval and returns. The clock is frozen, not offset. dropReporter
// is embedded by value in peerAllowlist, so this failure mode is one forgotten
// field away, with no compile-time signal.
func newDropReporter() dropReporter {
	return dropReporter{start: time.Now()}
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
// logging immediately. The sentinel is unambiguous because the marker
// recordDrop stores is offset by a nanosecond and so is never zero itself.
//
// The line is emitted at Info, not Debug: it is already rate-limited to one
// line per dropLogInterval, and "is this node dropping traffic?" is a primary
// operational question for a security control, which should not be invisible
// under a level-filtering logger.
func (r *dropReporter) recordDrop(log logger) {
	r.dropped.Add(1)

	// Offset by one nanosecond so a stored marker is never zero, which is
	// lastLog's "never logged" sentinel. time.Since(r.start) can genuinely read
	// 0 - a drop landing in the same tick of a coarse monotonic clock as the
	// reporter's construction - and storing that would leave the sentinel set,
	// so the rate limit would not engage until the clock advanced. Both sides of
	// the comparison below carry the same offset, so it cancels out and the
	// interval arithmetic is unchanged.
	elapsed := time.Since(r.start).Nanoseconds() + 1

	last := r.lastLog.Load()
	if last != 0 && elapsed-last < int64(dropLogInterval) {
		return
	}

	if !r.lastLog.CompareAndSwap(last, elapsed) {
		return // another goroutine already claimed this window
	}

	count := r.dropped.Swap(0)
	log.Infof("Dropped %d message(s) from non-allowlisted peers since the last report", count)
}
