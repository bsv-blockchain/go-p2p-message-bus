package p2p

import (
	"context"
	"slices"
	"strings"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	defaultTopicPeerRecoveryGrace       = 30 * time.Second
	defaultTopicPeerRecoveryMinInterval = 2 * time.Minute
	topicPeerCheckInterval              = 5 * time.Second
	// maxConsecutiveResets is how many resets a peer gets without reappearing in
	// any watched topic before the watchdog stops resetting it. A peer that left
	// a topic on purpose is not reset forever; seeing it in a topic again
	// re-arms the watchdog.
	maxConsecutiveResets = 3
)

// topicPeerWatchdog decides when a managed peer that is connected but missing
// from one of our topics has been missing long enough that its gossipsub
// session must be rebuilt.
//
// Why this exists: go-libp2p-pubsub keys its per-peer state by peer ID. When a
// new process with the same identity connects while the old one is still up
// (a rolling restart of a node with a fixed key), the new session announces its
// subscriptions first; when the old session then dies, pubsub's dead-peer
// handling clears ALL topic state for that peer ID, and since the peer is still
// connected it only respawns its own writer. The surviving session never
// re-announces its subscriptions, so the peer stays connected but absent from
// every topic, permanently. Resetting the gossipsub streams to the peer makes
// both sides respawn their writers and resend their subscriptions, without
// touching the connection.
//
// Without assumeShared, a peer is only watched on a topic after this process
// has seen it there, so a process that starts up already facing the stale
// state cannot recover it; the peer on the other side, which saw it before,
// does. Set ManagedPeersShareTopics where every managed peer shares our topics.
//
// The watchdog holds only the decision state; the caller supplies observations
// and performs the reset. It is not safe for concurrent use.
type topicPeerWatchdog struct {
	grace            time.Duration
	minResetInterval time.Duration
	// assumeShared treats every managed peer as a member of every topic we
	// subscribe to. Without it, a peer only counts for a topic once this process
	// has seen it there, so a pure bootstrap server is never reset.
	assumeShared bool

	seen         map[peer.ID]map[string]bool
	missingSince map[peer.ID]map[string]time.Time
	lastReset    map[peer.ID]time.Time
	// consecutiveResets counts resets since the peer was last seen in a topic.
	consecutiveResets map[peer.ID]int
}

func newTopicPeerWatchdog(grace, minResetInterval time.Duration, assumeShared bool) *topicPeerWatchdog {
	return &topicPeerWatchdog{
		grace:            grace,
		minResetInterval: minResetInterval,
		assumeShared:     assumeShared,
		seen:             make(map[peer.ID]map[string]bool),
		missingSince:     make(map[peer.ID]map[string]time.Time),
		lastReset:        make(map[peer.ID]time.Time),

		consecutiveResets: make(map[peer.ID]int),
	}
}

// observe records whether id is connected and present in topic at now, and
// reports whether its session should be reset.
func (w *topicPeerWatchdog) observe(now time.Time, id peer.ID, topic string, connected, inTopic bool) bool {
	if inTopic {
		if w.seen[id] == nil {
			w.seen[id] = make(map[string]bool)
		}

		w.seen[id][topic] = true
		delete(w.missingSince[id], topic)
		delete(w.consecutiveResets, id)

		return false
	}

	// A disconnected peer belongs to the reconnect loop, and the grace period
	// only counts time spent connected.
	if !connected {
		delete(w.missingSince[id], topic)
		return false
	}

	if !w.assumeShared && !w.seen[id][topic] {
		return false
	}

	if w.consecutiveResets[id] >= maxConsecutiveResets {
		return false
	}

	return w.missingPastGrace(now, id, topic) && !w.resetTooRecent(now, id)
}

// missingPastGrace starts or reads id's missing-since timer for topic and
// reports whether the grace period has elapsed.
func (w *topicPeerWatchdog) missingPastGrace(now time.Time, id peer.ID, topic string) bool {
	if w.missingSince[id] == nil {
		w.missingSince[id] = make(map[string]time.Time)
	}

	since, ok := w.missingSince[id][topic]
	if !ok {
		w.missingSince[id][topic] = now
		return false
	}

	return now.Sub(since) >= w.grace
}

// resetTooRecent reports whether id was reset less than minResetInterval ago.
func (w *topicPeerWatchdog) resetTooRecent(now time.Time, id peer.ID) bool {
	last, ok := w.lastReset[id]

	return ok && now.Sub(last) < w.minResetInterval
}

// recordReset notes that id's session was reset at now. The grace timers for
// id restart from scratch once it reconnects.
func (w *topicPeerWatchdog) recordReset(now time.Time, id peer.ID) {
	w.lastReset[id] = now
	w.consecutiveResets[id]++
	delete(w.missingSince, id)
}

// TopicPeers returns the IDs of the peers gossipsub currently has in topic, or
// nil if this client has not joined it.
func (c *client) TopicPeers(topic string) []string {
	c.mu.RLock()
	t, ok := c.topics[topic]
	c.mu.RUnlock()

	if !ok {
		return nil
	}

	ids := t.ListPeers()
	out := make([]string, 0, len(ids))

	for _, id := range ids {
		out = append(out, id.String())
	}

	return out
}

// recoveryPeers returns the peers the topic peer watchdog watches: the
// explicitly configured BootstrapPeers and the static peers. The public IPFS
// bootstrap nodes used when BootstrapPeers is empty never join our topics and
// are excluded.
func recoveryPeers(config Config, staticPeers []peer.AddrInfo, log logger) []peer.AddrInfo {
	var explicit []peer.AddrInfo
	if len(config.BootstrapPeers) > 0 {
		explicit = parsePeerMultiaddrs(config.BootstrapPeers, log)
	}

	return append(explicit, staticPeers...)
}

// monitorTopicPeers runs the topic peer watchdog over the managed peers for the
// lifetime of ctx. When a managed peer has been connected but missing from a
// topic for longer than the grace period, it closes the connection and redials,
// which makes both sides drop the stale gossipsub session and handshake again.
func (c *client) monitorTopicPeers(ctx context.Context, managed []peer.AddrInfo) {
	if c.config.DisableTopicPeerRecovery || len(managed) == 0 {
		return
	}

	grace := c.config.TopicPeerRecoveryGrace
	if grace <= 0 {
		grace = defaultTopicPeerRecoveryGrace
	}

	minInterval := c.config.TopicPeerRecoveryMinInterval
	if minInterval <= 0 {
		minInterval = defaultTopicPeerRecoveryMinInterval
	}

	w := newTopicPeerWatchdog(grace, minInterval, c.config.ManagedPeersShareTopics)

	ticker := time.NewTicker(topicPeerCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.checkTopicPeers(ctx, w, managed, time.Now())
		}
	}
}

// checkTopicPeers feeds one round of observations to w and resets every peer
// it flags.
func (c *client) checkTopicPeers(ctx context.Context, w *topicPeerWatchdog, managed []peer.AddrInfo, now time.Time) {
	// Only topics we subscribe to count. Publish joins a topic without
	// subscribing, and a managed peer is never "missing" from a topic we only
	// publish to.
	// ListPeers round-trips through the pubsub event loop, so collect the topic
	// handles under the lock and query them after releasing it.
	c.mu.RLock()
	watched := make(map[string]*pubsub.Topic, len(c.subs))
	for name := range c.subs {
		if t, ok := c.topics[name]; ok {
			watched[name] = t
		}
	}
	c.mu.RUnlock()

	topicPeers := make(map[string][]peer.ID, len(watched))
	for name, t := range watched {
		topicPeers[name] = t.ListPeers()
	}

	for _, pi := range managed {
		if pi.ID == c.host.ID() {
			continue
		}

		connected := c.host.Network().Connectedness(pi.ID) == network.Connected

		var missing []string

		for name, members := range topicPeers {
			if w.observe(now, pi.ID, name, connected, slices.Contains(members, pi.ID)) {
				missing = append(missing, name)
			}
		}

		if len(missing) == 0 {
			continue
		}

		slices.Sort(missing)
		w.recordReset(now, pi.ID)
		c.resetPeerSession(ctx, pi, missing)
	}
}

// resetPeerSession resets the gossipsub streams to pi without closing the
// connection: bootstrap peers double as relays, and closing the connection
// would drop relay reservations and relayed connections to unrelated peers.
// Resetting our outbound stream makes pubsub respawn its writer and resend our
// subscriptions; resetting the inbound one makes the remote do the same, so
// it re-announces its subscriptions to us.
func (c *client) resetPeerSession(_ context.Context, pi peer.AddrInfo, topics []string) {
	c.logger.Warnf("Managed peer %s [%s] connected but missing from topics %v; resetting its gossipsub streams",
		pi.ID.String(), c.peerTracker.getName(pi.ID), topics)

	reset := 0

	for _, conn := range c.host.Network().ConnsToPeer(pi.ID) {
		for _, s := range conn.GetStreams() {
			if isPubSubProtocol(s.Protocol()) {
				_ = s.Reset()
				reset++
			}
		}
	}

	c.logger.Debugf("Reset %d gossipsub stream(s) to peer %s", reset, pi.ID.String())

	if cb := c.config.OnTopicPeerReset; cb != nil {
		cb(pi.ID.String(), topics)
	}
}

// isPubSubProtocol reports whether proto is a gossipsub or floodsub protocol.
func isPubSubProtocol(proto protocol.ID) bool {
	return strings.HasPrefix(string(proto), "/meshsub/") || strings.HasPrefix(string(proto), "/floodsub/")
}
