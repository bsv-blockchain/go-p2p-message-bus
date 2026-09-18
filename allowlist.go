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
// of staticPeers, which the config already describes as known trusted peers.
// staticPeers must be the already-parsed result of
// parsePeerMultiaddrs(config.StaticPeers, log): the caller (NewClient) parses
// StaticPeers once for dialing, the allowlist and the GossipSub direct-peer
// set, so this function does not parse it again. BootstrapPeers are
// deliberately excluded: bootstrap is a routing role, not a trust statement,
// and the default list is public infrastructure.
func newPeerAllowlist(config Config, selfID peer.ID, staticPeers []peer.AddrInfo, log logger) (*peerAllowlist, error) {
	if len(config.AllowedPublisherIDs) == 0 {
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

	fromStatic := addStaticPublisherIDs(allowed, config.StaticPeers, staticPeers, log)

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

// addStaticPublisherIDs adds the peer IDs of the configured static peers to
// allowed and returns how many of them were not already present.
//
// IDs arrive by two routes. resolved is the parsePeerMultiaddrs output, which
// is authoritative but incomplete: a /dnsaddr/ entry whose DNS lookup fails at
// startup drops out of that list entirely and, without a second route, would be
// silently missing from the allowlist for the whole process lifetime even
// though its peer ID was available all along. So rawEntries - the unparsed
// config.StaticPeers strings - are scanned as well for entries that name their
// ID directly via a /p2p/ component, which needs no resolution.
func addStaticPublisherIDs(allowed map[peer.ID]struct{}, rawEntries []string, resolved []peer.AddrInfo, log logger) int {
	before := len(allowed)

	for _, addrInfo := range resolved {
		allowed[addrInfo.ID] = struct{}{}
	}

	named := make(map[peer.ID]struct{}, len(rawEntries))

	var unnamed []string

	for _, entry := range rawEntries {
		id := publisherIDFromRawMultiaddr(entry)
		if id == "" {
			unnamed = append(unnamed, entry)

			continue
		}

		named[id] = struct{}{}
		allowed[id] = struct{}{}
	}

	warnStaticPeersWithoutPublisherID(unnamed, named, resolved, log)

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

// warnStaticPeersWithoutPublisherID warns about static peers that contributed
// no ID to the publisher set, whose messages will therefore be dropped.
//
// unnamed are the entries carrying no /p2p/ component, so their ID is only
// knowable through resolution; named are the IDs the raw scan did learn.
// Resolved AddrInfos carry no back-reference to the entry they came from, so
// attribution is only possible in the negative: if every resolved ID is one a
// raw entry already names, then no unnamed entry resolved and all of them
// failed. If some resolved ID is unaccounted for, at least one unnamed entry
// did resolve and the rest cannot be singled out, so nothing is reported.
func warnStaticPeersWithoutPublisherID(unnamed []string, named map[peer.ID]struct{}, resolved []peer.AddrInfo, log logger) {
	if len(unnamed) == 0 {
		return
	}

	for _, addrInfo := range resolved {
		if _, ok := named[addrInfo.ID]; !ok {
			return
		}
	}

	log.Warnf("Static peer(s) %q contributed no publisher ID (DNS resolution yielded nothing and the entry carries no /p2p/ component); messages from them will be dropped - use the /dnsaddr/<host>/p2p/<id> form when the publisher allowlist is in use",
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
// wrapping. elapsed is then permanently MaxInt64: the first recordDrop takes
// the lastLog == 0 sentinel branch and logs, stores MaxInt64, and every later
// call computes elapsed-last == 0 < dropLogInterval and returns. The clock is
// frozen, not offset. dropReporter is embedded by value in peerAllowlist, so
// this failure mode is one forgotten field away, with no compile-time signal.
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
// logging immediately.
//
// The line is emitted at Info, not Debug: it is already rate-limited to one
// line per dropLogInterval, and "is this node dropping traffic?" is a primary
// operational question for a security control, which should not be invisible
// under a level-filtering logger.
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
	log.Infof("Dropped %d message(s) from non-allowlisted peers since the last report", count)
}
