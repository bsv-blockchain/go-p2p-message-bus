package p2p

import (
	"slices"
	"strings"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

const (
	defaultTopicPeerRecoveryGrace       = 30 * time.Second
	defaultTopicPeerRecoveryMinInterval = 2 * time.Minute
	topicPeerCheckInterval              = 5 * time.Second

	// maxConsecutiveResets is how many resets a peer gets over one topic without
	// reappearing in that topic. A peer that left a topic on purpose is not
	// reset forever. Seeing it in that topic again, or a new connection (a new
	// session), re-arms the topic.
	maxConsecutiveResets = 3

	// maxResetsPerCooldown caps the resets of one peer until resetCooldown has
	// passed since its latest reset, and the cap is not cleared when the peer
	// reappears. A stream reset kills the remote's gossipsub writer to us, and
	// go-libp2p-pubsub (v0.18.0, backoff.go updateAndGet: MaxBackoffAttempts =
	// 4, TimeToLive = 10m) respawns a dead writer for a still-connected peer
	// only four times; the count starts over only once more than TimeToLive has
	// passed since the latest death, not as each death ages out, and the fifth
	// death drops the writer for good while the connection stays up. Two resets
	// leave headroom for real deaths the remote sees, such as the overlapping
	// restart this watchdog exists for.
	maxResetsPerCooldown = 2
	// resetCooldown is pubsub's TimeToLive plus a margin, so the remote's count
	// has started over by the time ours does.
	resetCooldown = 11 * time.Minute

	// writerRepairInitialDelay and writerRepairMaxDelay bound the per-peer
	// backoff between writer repairs while a peer's writer stays missing.
	writerRepairInitialDelay = 10 * time.Second
	writerRepairMaxDelay     = 5 * time.Minute

	// writerRepairAfterReset skips writer repair right after our own reset,
	// while pubsub is still respawning the writer it just lost.
	writerRepairAfterReset = 15 * time.Second

	// topicPeerRedialDelay gives both sides time to tear the closed session down
	// before we dial again.
	topicPeerRedialDelay = 500 * time.Millisecond
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
// every topic, permanently. See resetPeerSession for how the session is
// rebuilt.
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
	// consecutiveResets counts, per topic, resets of the peer since it was last
	// seen in that topic or since its last new session. Presence in another
	// topic does not re-arm it.
	consecutiveResets map[peer.ID]map[string]int
	// cooldownResets counts the peer's resets since resetCooldown last passed
	// without one.
	cooldownResets map[peer.ID]int
	// conns holds the IDs of the peer's connections at the last observation,
	// to spot a new session that formed between two rounds.
	conns map[peer.ID][]string

	repair *writerRepairBackoff
}

func newTopicPeerWatchdog(grace, minResetInterval time.Duration, assumeShared bool) *topicPeerWatchdog {
	return &topicPeerWatchdog{
		grace:            grace,
		minResetInterval: minResetInterval,
		assumeShared:     assumeShared,
		seen:             make(map[peer.ID]map[string]bool),
		missingSince:     make(map[peer.ID]map[string]time.Time),
		lastReset:        make(map[peer.ID]time.Time),

		consecutiveResets: make(map[peer.ID]map[string]int),
		cooldownResets:    make(map[peer.ID]int),
		conns:             make(map[peer.ID][]string),
		repair:            newWriterRepairBackoff(),
	}
}

// observeConns records the IDs of id's open connections at now. A connection
// that was not open at the previous observation means a new session, possibly
// formed between two rounds without us ever seeing the peer disconnected (a
// quick reconnect, or an overlapping restart that keeps another connection), so
// it re-arms the per-topic give-up and restarts the grace timers. Our own
// close-and-redial is not a new session for this purpose (otherwise a peer that
// left a topic on purpose would never be given up on).
func (w *topicPeerWatchdog) observeConns(now time.Time, id peer.ID, connIDs []string) {
	prev, known := w.conns[id]

	if len(connIDs) == 0 {
		delete(w.conns, id)
		return
	}

	w.conns[id] = connIDs

	if !known || w.recentlyReset(now, id, w.minResetInterval) {
		return
	}

	for _, c := range connIDs {
		if !slices.Contains(prev, c) {
			delete(w.consecutiveResets, id)
			delete(w.missingSince, id)

			return
		}
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
		delete(w.consecutiveResets[id], topic)

		return false
	}

	// A disconnected peer belongs to the reconnect loop, and the grace period
	// only counts time spent connected. The next connection is a new session,
	// so the per-topic give-up starts over, unless the disconnect is our own
	// reset closing the connection (otherwise a peer that left a topic on
	// purpose would never be given up on).
	if !connected {
		delete(w.missingSince[id], topic)

		if !w.recentlyReset(now, id, w.minResetInterval) {
			delete(w.consecutiveResets, id)
		}

		return false
	}

	if !w.assumeShared && !w.seen[id][topic] {
		return false
	}

	if w.consecutiveResets[id][topic] >= maxConsecutiveResets || w.cooldownFull(now, id) {
		return false
	}

	return w.missingPastGrace(now, id, topic) && !w.recentlyReset(now, id, w.minResetInterval)
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

// recentlyReset reports whether id was reset less than d ago.
func (w *topicPeerWatchdog) recentlyReset(now time.Time, id peer.ID, d time.Duration) bool {
	last, ok := w.lastReset[id]

	return ok && now.Sub(last) < d
}

// cooldownFull reports whether id has used up its resets, which it gets back
// only once resetCooldown has passed since its latest reset.
func (w *topicPeerWatchdog) cooldownFull(now time.Time, id peer.ID) bool {
	if !w.recentlyReset(now, id, resetCooldown) {
		delete(w.cooldownResets, id)
	}

	return w.cooldownResets[id] >= maxResetsPerCooldown
}

// recordReset notes that id's session was reset at now over topics. The grace
// timers for id restart from scratch, each topic's reset count grows until the
// peer is seen in that topic again, and the reset counts against the cooldown.
func (w *topicPeerWatchdog) recordReset(now time.Time, id peer.ID, topics []string) {
	if !w.recentlyReset(now, id, resetCooldown) {
		delete(w.cooldownResets, id)
	}

	w.lastReset[id] = now
	w.cooldownResets[id]++
	delete(w.missingSince, id)

	if w.consecutiveResets[id] == nil {
		w.consecutiveResets[id] = make(map[string]int)
	}

	for _, topic := range topics {
		w.consecutiveResets[id][topic]++
	}
}

// writerRepairBackoff spaces out writer repairs for a peer whose writer stays
// missing, so a repair that cannot succeed (the remote refuses new streams, for
// example) does not retry every round for the life of the connection.
type writerRepairBackoff struct {
	delay map[peer.ID]time.Duration
	next  map[peer.ID]time.Time
}

func newWriterRepairBackoff() *writerRepairBackoff {
	return &writerRepairBackoff{
		delay: make(map[peer.ID]time.Duration),
		next:  make(map[peer.ID]time.Time),
	}
}

// allow reports whether a repair for id may run at now.
func (b *writerRepairBackoff) allow(now time.Time, id peer.ID) bool {
	next, ok := b.next[id]

	return !ok || !now.Before(next)
}

// record notes a repair for id at now and doubles the delay before the next.
func (b *writerRepairBackoff) record(now time.Time, id peer.ID) {
	d := b.delay[id] * 2
	if d == 0 {
		d = writerRepairInitialDelay
	}

	if d > writerRepairMaxDelay {
		d = writerRepairMaxDelay
	}

	b.delay[id] = d
	b.next[id] = now.Add(d)
}

// clear resets the backoff for id once it has a writer again.
func (b *writerRepairBackoff) clear(id peer.ID) {
	delete(b.delay, id)
	delete(b.next, id)
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

// recoveryPeers returns the peers the topic peer watchdog watches, one entry
// per peer ID: the bootstrap peers NewClient selected, when they were
// explicitly configured, and the static peers. bootstrapPeers is NewClient's
// already-resolved snapshot, so /dnsaddr/ entries are not looked up a second
// time (a second lookup could fail or disagree and drop a connected bootstrap
// peer from recovery). When BootstrapPeers is empty the snapshot is the public
// IPFS fallback, whose nodes never join our topics, and it is excluded. A peer
// listed several times (a /dnsaddr/ resolving to one entry per address, or the
// same peer in both lists) is merged into one entry with all its addresses.
func recoveryPeers(config Config, bootstrapPeers, staticPeers []peer.AddrInfo) []peer.AddrInfo {
	candidates := staticPeers
	if len(config.BootstrapPeers) > 0 {
		candidates = append(append([]peer.AddrInfo{}, bootstrapPeers...), staticPeers...)
	}

	index := make(map[peer.ID]int, len(candidates))
	watched := make([]peer.AddrInfo, 0, len(candidates))

	for _, pi := range candidates {
		i, ok := index[pi.ID]
		if !ok {
			index[pi.ID] = len(watched)
			watched = append(watched, peer.AddrInfo{ID: pi.ID, Addrs: slices.Clone(pi.Addrs)})

			continue
		}

		for _, addr := range pi.Addrs {
			if !slices.ContainsFunc(watched[i].Addrs, addr.Equal) {
				watched[i].Addrs = append(watched[i].Addrs, addr)
			}
		}
	}

	return watched
}

// monitorTopicPeers runs the topic peer watchdog over the managed peers until
// the client is closed. When a managed peer has been connected but missing
// from a topic for longer than the grace period, resetPeerSession rebuilds its
// gossipsub session; each round also rebuilds a missing gossipsub writer to a
// connected managed peer.
func (c *client) monitorTopicPeers(managed []peer.AddrInfo) {
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

	relayEvents, stopRelayEvents := c.watchRelayReservations()
	defer stopRelayEvents()

	ticker := time.NewTicker(topicPeerCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case e, ok := <-relayEvents:
			if !ok {
				relayEvents = nil
				continue
			}

			c.storeRelayAddrs(e)
		case <-ticker.C:
			c.checkTopicPeers(w, managed, time.Now())
		}
	}
}

// watchRelayReservations subscribes to our relay reservations, published as
// circuit addresses, and returns the event channel (nil if the subscription
// failed) and a function that ends the subscription. host.Addrs() cannot be
// used for this: a configured AnnounceAddrs replaces the addresses it returns.
// The autorelay emitter is stateful, so the current set arrives right after
// subscribing.
func (c *client) watchRelayReservations() (<-chan interface{}, func()) {
	sub, err := c.host.EventBus().Subscribe(new(event.EvtAutoRelayAddrsUpdated))
	if err != nil {
		c.logger.Warnf("Failed to watch relay reservations, topic peer recovery may close relay connections: %v", err)
		return nil, func() {}
	}

	return sub.Out(), func() { _ = sub.Close() }
}

// storeRelayAddrs records the relay reservations carried by e, an
// EvtAutoRelayAddrsUpdated, for relayInUse.
func (c *client) storeRelayAddrs(e interface{}) {
	if evt, ok := e.(event.EvtAutoRelayAddrsUpdated); ok {
		addrs := slices.Clone(evt.RelayAddrs)
		c.relayAddrs.Store(&addrs)
	}
}

// checkTopicPeers feeds one round of observations to w and resets every peer
// it flags.
func (c *client) checkTopicPeers(w *topicPeerWatchdog, managed []peer.AddrInfo, now time.Time) {
	if c.ctx.Err() != nil {
		return
	}

	topicPeers := c.subscribedTopicPeers()

	for _, pi := range managed {
		if pi.ID == c.host.ID() || c.ctx.Err() != nil {
			continue
		}

		c.checkManagedPeer(w, pi, topicPeers, now)
	}
}

// subscribedTopicPeers returns the gossipsub peers of every topic we subscribe
// to. Only subscribed topics count: Publish joins a topic without subscribing,
// and a managed peer is never "missing" from a topic we only publish to.
// ListPeers round-trips through the pubsub event loop, so the topic handles are
// collected under the lock and queried after releasing it.
func (c *client) subscribedTopicPeers() map[string][]peer.ID {
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

	return topicPeers
}

// checkManagedPeer runs one round of the watchdog for pi: it repairs a missing
// writer, records pi's topic membership, and resets pi if the watchdog flags it.
func (c *client) checkManagedPeer(w *topicPeerWatchdog, pi peer.AddrInfo, topicPeers map[string][]peer.ID, now time.Time) {
	w.observeConns(now, pi.ID, c.connIDs(pi.ID))

	connected := c.host.Network().Connectedness(pi.ID) == network.Connected
	if connected {
		c.maybeRepairWriter(w, pi.ID, now)
	}

	var missing []string

	for name, members := range topicPeers {
		if w.observe(now, pi.ID, name, connected, slices.Contains(members, pi.ID)) {
			missing = append(missing, name)
		}
	}

	if len(missing) == 0 {
		return
	}

	slices.Sort(missing)
	w.recordReset(now, pi.ID, missing)
	c.resetPeerSession(pi, missing)
}

// resetPeerSession rebuilds the gossipsub session with pi. The reset is
// per-peer, not per-topic: it briefly interrupts every topic shared with the
// peer, including healthy ones, until both sides have re-announced their
// subscriptions and re-grafted.
//
// For a peer we dialed that does not relay for us, it closes the connection
// and redials. Both sides then drop the peer and handshake from scratch, and
// because the peer is no longer connected when its writer dies,
// go-libp2p-pubsub does not count it against the remote's writer-respawn
// budget. This works whether or not the remote runs this watchdog.
//
// Only the gossipsub streams are reset, and the connection stays up, for a
// peer that relays for us (we hold a reservation on it, or a connection is
// relayed through it), since closing the connection would drop relay
// reservations and relayed connections to unrelated peers, and for a peer we
// have no outbound connection to, since its configured address may be stale
// (a restarted pod with a new IP) and we might not be able to dial it back.
// A stream reset kills both writers while the connection stays up, which costs
// each side one writer respawn; maxResetsPerCooldown keeps us within the
// remote's budget, and our own side is repaired by maybeRepairWriter, but a
// remote that has already exhausted its budget for us can only recover itself
// (or on a new connection).
func (c *client) resetPeerSession(pi peer.AddrInfo, topics []string) {
	name := c.peerTracker.getName(pi.ID)

	switch {
	case c.relayInUse(pi.ID):
		c.logger.Warnf("Managed peer %s [%s] connected but missing from topics %v; it relays for us, resetting its gossipsub streams",
			pi.ID.String(), name, topics)
		c.logger.Debugf("Reset %d gossipsub stream(s) to peer %s", c.resetPeerStreams(pi.ID), pi.ID.String())
	case !c.hasOutboundConn(pi.ID):
		c.logger.Warnf("Managed peer %s [%s] connected but missing from topics %v; it is only connected inbound, resetting its gossipsub streams",
			pi.ID.String(), name, topics)
		c.logger.Debugf("Reset %d gossipsub stream(s) to peer %s", c.resetPeerStreams(pi.ID), pi.ID.String())
	default:
		c.logger.Warnf("Managed peer %s [%s] connected but missing from topics %v; closing the connection to rebuild its gossipsub session",
			pi.ID.String(), name, topics)
		c.closePeerSession(pi)
	}

	c.notifyTopicPeerReset(pi.ID, topics)
}

// notifyTopicPeerReset calls OnTopicPeerReset, if set, and recovers a panic in
// it so a faulty callback cannot take the process down.
func (c *client) notifyTopicPeerReset(id peer.ID, topics []string) {
	cb := c.config.OnTopicPeerReset
	if cb == nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			c.logger.Errorf("OnTopicPeerReset panicked for peer %s: %v", id.String(), r)
		}
	}()

	cb(id.String(), topics)
}

// hasOutboundConn reports whether we have a connection to id that we dialed.
func (c *client) hasOutboundConn(id peer.ID) bool {
	for _, conn := range c.host.Network().ConnsToPeer(id) {
		if conn.Stat().Direction == network.DirOutbound {
			return true
		}
	}

	return false
}

// connIDs returns the IDs of our open connections to id.
func (c *client) connIDs(id peer.ID) []string {
	conns := c.host.Network().ConnsToPeer(id)
	ids := make([]string, 0, len(conns))

	for _, conn := range conns {
		ids = append(ids, conn.ID())
	}

	return ids
}

// resetPeerStreams resets every gossipsub stream to id and leaves the
// connection open. It returns how many streams it reset.
func (c *client) resetPeerStreams(id peer.ID) int {
	reset := 0

	for _, conn := range c.host.Network().ConnsToPeer(id) {
		for _, s := range conn.GetStreams() {
			if isPubSubProtocol(s.Protocol()) {
				_ = s.Reset()
				reset++
			}
		}
	}

	return reset
}

// closePeerSession closes every connection to pi and dials it again. The
// redial is tracked in c.background so Close waits for it.
func (c *client) closePeerSession(pi peer.AddrInfo) {
	if err := c.host.Network().ClosePeer(pi.ID); err != nil {
		c.logger.Warnf("Failed to close connection to peer %s: %v", pi.ID.String(), err)
	}

	c.background.Add(1)

	go func() {
		defer c.background.Done()

		select {
		case <-c.ctx.Done():
			return
		case <-time.After(topicPeerRedialDelay):
		}

		if err := c.host.Connect(c.ctx, pi); err != nil {
			c.logger.Debugf("Redial of peer %s after session reset failed (the managed-peer reconnect loop retries): %v", pi.ID.String(), err)
		}
	}()
}

// relayInUse reports whether id currently carries relay traffic for us.
func (c *client) relayInUse(id peer.ID) bool {
	conns := c.host.Network().Conns()
	connAddrs := make([]multiaddr.Multiaddr, 0, len(conns))

	for _, conn := range conns {
		connAddrs = append(connAddrs, conn.RemoteMultiaddr())
	}

	var reservations []multiaddr.Multiaddr
	if addrs := c.relayAddrs.Load(); addrs != nil {
		reservations = *addrs
	}

	return relayInUse(id, reservations, connAddrs)
}

// relayInUse reports whether relay is in use as a circuit relay: one of our
// relay reservation addresses is a circuit through it, or one of our
// connections is relayed through it.
func relayInUse(relay peer.ID, reservationAddrs, connAddrs []multiaddr.Multiaddr) bool {
	marker := "/p2p/" + relay.String() + "/p2p-circuit"

	for _, addrs := range [][]multiaddr.Multiaddr{reservationAddrs, connAddrs} {
		for _, addr := range addrs {
			if addr != nil && strings.Contains(addr.String(), marker) {
				return true
			}
		}
	}

	return false
}

// isPubSubProtocol reports whether proto is one of the gossipsub (or floodsub)
// protocols this client's pubsub speaks.
func isPubSubProtocol(proto protocol.ID) bool {
	return slices.Contains(pubsub.GossipSubDefaultProtocols, proto)
}

// hasOutboundPubSubStream reports whether we have a gossipsub writer stream open
// to id, i.e. pubsub still holds an outbound queue for the peer.
func (c *client) hasOutboundPubSubStream(id peer.ID) bool {
	for _, conn := range c.host.Network().ConnsToPeer(id) {
		for _, s := range conn.GetStreams() {
			if s.Stat().Direction == network.DirOutbound && isPubSubProtocol(s.Protocol()) {
				return true
			}
		}
	}

	return false
}

// maybeRepairWriter rebuilds a missing writer to id, unless we reset the peer
// a moment ago (pubsub is still respawning the writer) or the per-peer backoff
// says to wait.
func (c *client) maybeRepairWriter(w *topicPeerWatchdog, id peer.ID, now time.Time) {
	if c.hasOutboundPubSubStream(id) {
		w.repair.clear(id)
		return
	}

	if w.recentlyReset(now, id, writerRepairAfterReset) || !w.repair.allow(now, id) {
		return
	}

	if c.repairWriter(id) {
		w.repair.record(now, id)
	}
}

// peerProtocolsEmitter returns the emitter used to re-announce a peer's
// protocols on the host event bus, creating it on first use. The failure is
// logged outside the Once, so a blocking logger cannot hold up Close, which
// settles the same Once.
func (c *client) peerProtocolsEmitter() event.Emitter {
	var err error

	c.protoEmitterOnce.Do(func() {
		c.protoEmitter, err = c.host.EventBus().Emitter(new(event.EvtPeerProtocolsUpdated))
	})

	if err != nil {
		c.logger.Warnf("Failed to create peer protocols emitter, gossipsub writer repair disabled: %v", err)
	}

	return c.protoEmitter
}

// repairWriter asks pubsub to rebuild its writer to a connected peer that has
// none, and reports whether it asked.
//
// go-libp2p-pubsub respawns a dead writer for a still-connected peer only a
// few times per backoff window; after that it drops the peer's outbound queue
// and never recreates it while the connection stays up, so stream resets can
// leave a connected peer that never hears from us.
//
// This relies on go-libp2p-pubsub internals (v0.18.0, peer_notify.go and
// pubsub.go): watchForNewPeers subscribes to EvtPeerProtocolsUpdated and calls
// notifyNewPeer when Added contains a supported protocol, and
// handlePendingPeers then opens a new writer for a connected peer that is not
// in p.peers (it does nothing for one that is). A pubsub upgrade that changes
// that path would silently disable this repair. The event is emitted on the
// host-wide bus, so other subscribers (kad-dht, for one) also see a protocols
// update that adds nothing new.
func (c *client) repairWriter(id peer.ID) bool {
	protos, err := c.host.Peerstore().GetProtocols(id)
	if err != nil {
		return false
	}

	protos = slices.DeleteFunc(protos, func(p protocol.ID) bool { return !isPubSubProtocol(p) })
	if len(protos) == 0 {
		return false
	}

	em := c.peerProtocolsEmitter()
	if em == nil {
		return false
	}

	if err := em.Emit(event.EvtPeerProtocolsUpdated{Peer: id, Added: protos}); err != nil {
		c.logger.Debugf("Failed to re-announce gossipsub protocols for peer %s: %v", id.String(), err)
		return false
	}

	c.logger.Debugf("No gossipsub writer to managed peer %s [%s]; asked pubsub to rebuild it", id.String(), c.peerTracker.getName(id))

	return true
}
