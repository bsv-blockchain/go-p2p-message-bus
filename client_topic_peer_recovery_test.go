package p2p

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

const recoveryTopic = "recovery-topic"

// startTopicClient starts a client with the given key, subscribed to
// recoveryTopic, with staticPeers as its managed peers. Close is registered as
// a test cleanup straight away, so a failing assertion never leaks the client;
// Close is idempotent, so tests may also close it earlier.
func startTopicClient(t *testing.T, name string, key crypto.PrivKey, staticPeers []string) (*client, <-chan Message) {
	t.Helper()

	logs := &captureLogger{}

	cl, err := NewClient(Config{
		Name:            name,
		PrivateKey:      key,
		AllowPrivateIPs: true,
		StaticPeers:     staticPeers,
		Logger:          logs,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = cl.Close()

		if t.Failed() {
			t.Logf("--- %s logs ---\n%s", name, logs.String())
		}
	})

	c := cl.(*client)

	return c, c.Subscribe(recoveryTopic)
}

// topicHasPeer reports whether from's view of recoveryTopic includes id.
func topicHasPeer(from *client, id peer.ID) bool {
	from.mu.RLock()
	topic, ok := from.topics[recoveryTopic]
	from.mu.RUnlock()

	if !ok {
		return false
	}

	for _, p := range topic.ListPeers() {
		if p == id {
			return true
		}
	}

	return false
}

// receives reports whether a message published by pub on recoveryTopic shows up
// on ch within wait.
func receives(t *testing.T, pub *client, ch <-chan Message, wait time.Duration) bool {
	t.Helper()

	payload := []byte(fmt.Sprintf("probe-%d", time.Now().UnixNano()))
	deadline := time.After(wait)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()

	for {
		_ = pub.Publish(context.Background(), recoveryTopic, payload)

		select {
		case msg, ok := <-ch:
			if !ok {
				return false
			}
			if string(msg.Data) == string(payload) {
				return true
			}
		case <-tick.C:
		case <-deadline:
			return false
		}
	}
}

// connIDs returns the IDs of from's connections to id.
func connIDs(from *client, id peer.ID) []string {
	conns := from.host.Network().ConnsToPeer(id)
	ids := make([]string, 0, len(conns))

	for _, conn := range conns {
		ids = append(ids, conn.ID())
	}

	return ids
}

// waitFor polls cond until it holds or timeout elapses.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}

	return cond()
}

// TestTopicPeerRecoversAfterOverlappingRestarts reproduces a rolling update of
// a node whose identity is fixed: a new process with the same private key
// starts and connects while the old one is still alive, then the old one
// exits. This leaves the long-lived peer with a live connection to the node but
// no gossipsub session: the node never reappears in the topic and messages stop
// flowing, with nothing on either side that recovers it. The client must notice
// and re-establish the session.
func TestTopicPeerRecoversAfterOverlappingRestarts(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process restart scenario takes tens of seconds; skip in -short")
	}

	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	// As in a teranode deployment, node-a lists node-b as a managed peer by
	// stable peer ID. node-b's first process starts first so node-a can be given
	// its real address; later node-b processes come up elsewhere (a new pod gets
	// a new IP) and dial in.
	b, bMsgs := startTopicClient(t, "node-b", keyB, nil)
	bID := b.host.ID()

	a, _ := startTopicClient(t, "node-a", keyA, []string{loopbackAddr(t, b)})
	aAddr := loopbackAddr(t, a)

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) },
		15*time.Second, 100*time.Millisecond, "node-b should join node-a's topic view")
	require.True(t, receives(t, a, bMsgs, 10*time.Second), "baseline: node-a's messages should reach node-b")

	// A real node has been in its topics long before a rollout; give the topic
	// peer watchdog a couple of rounds to observe node-b there.
	time.Sleep(2 * topicPeerCheckInterval)

	// Overlapping restarts: start the replacement, let it connect, then stop the
	// old process. Each swap closes a connection while another one to the same
	// peer ID is still open.
	const generations = 5
	for i := 0; i < generations; i++ {
		next, nextMsgs := startTopicClient(t, "node-b", keyB, []string{aAddr})

		require.Eventually(t, func() bool {
			return a.host.Network().Connectedness(bID) == network.Connected &&
				len(a.host.Network().ConnsToPeer(bID)) > 1
		}, 15*time.Second, 50*time.Millisecond, "generation %d should connect while the old process is alive", i)

		require.NoError(t, b.Close())

		b, bMsgs = next, nextMsgs
	}

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) },
		90*time.Second, 250*time.Millisecond,
		"after overlapping restarts node-a must see node-b in the topic again")
	require.True(t, receives(t, a, bMsgs, 30*time.Second),
		"after overlapping restarts node-a's messages must reach node-b again")
}

// TestTopicPeerRecoveryIgnoresPublishOnlyTopics checks that the watchdog only
// judges topics this client subscribes to. Publish joins a topic without
// subscribing, so a healthy managed peer is never a member of it from our
// point of view; treating it as missing would reset the peer every minimum
// interval even with ManagedPeersShareTopics set.
func TestTopicPeerRecoveryIgnoresPublishOnlyTopics(t *testing.T) {
	const publishOnlyTopic = "publish-only-topic"

	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	b, _ := startTopicClient(t, "node-b", keyB, nil)
	bID := b.host.ID()

	var resets []string

	// The background watchdog is off so the test drives the rounds itself; the
	// reset callback still records anything resetPeerSession does.
	clA, err := NewClient(Config{
		Name:                     "node-a",
		PrivateKey:               keyA,
		AllowPrivateIPs:          true,
		StaticPeers:              []string{loopbackAddr(t, b)},
		Logger:                   &captureLogger{},
		DisableTopicPeerRecovery: true,
		ManagedPeersShareTopics:  true,
		OnTopicPeerReset:         func(id string, topics []string) { resets = append(resets, fmt.Sprintf("%s %v", id, topics)) },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = clA.Close() })

	a := clA.(*client)
	a.Subscribe(recoveryTopic)

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) },
		15*time.Second, 100*time.Millisecond, "node-b should join node-a's topic view")

	// Publishing joins the topic locally without subscribing to it.
	require.NoError(t, a.Publish(context.Background(), publishOnlyTopic, []byte("hello")))

	managed := []peer.AddrInfo{{ID: bID}}
	w := newTopicPeerWatchdog(time.Second, time.Minute, true)
	now := time.Now()

	a.checkTopicPeers(w, managed, now)
	a.checkTopicPeers(w, managed, now.Add(2*time.Second))

	require.Empty(t, resets, "a healthy peer must not be reset over a topic we only publish to")
	require.Equal(t, network.Connected, a.host.Network().Connectedness(bID), "node-b must stay connected")
}

// TestTopicPeerResetRecoversRemoteThatDoesNotManageUs covers the remote side of
// a reset. Every stream reset also kills the remote's writer to us, which costs
// the remote one of pubsub's four writer respawns per sliding window, and only
// a remote that runs this watchdog with us as a managed peer can repair its own
// writer. A peer that does not relay for us is therefore reset by closing the
// connection, which the remote does not count against that budget, and redialed.
func TestTopicPeerResetRecoversRemoteThatDoesNotManageUs(t *testing.T) {
	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	// node-b has no managed peers, so it never repairs anything itself.
	b, bMsgs := startTopicClient(t, "node-b", keyB, nil)
	bID := b.host.ID()

	a, aMsgs := startTopicClient(t, "node-a", keyA, []string{loopbackAddr(t, b)})
	aID := a.host.ID()

	pi := peer.AddrInfo{ID: bID, Addrs: b.host.Addrs()}

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) && topicHasPeer(b, aID) },
		15*time.Second, 100*time.Millisecond, "nodes should see each other in the topic")

	healthy := func() bool {
		return topicHasPeer(a, bID) && topicHasPeer(b, aID) &&
			a.hasOutboundPubSubStream(bID) && b.hasOutboundPubSubStream(aID)
	}

	// More resets than pubsub's per-window writer respawn budget (four). Each
	// reset waits for both writers to be back first, so every reset really
	// kills node-b's writer to node-a.
	for i := 0; i < 5; i++ {
		require.Eventually(t, healthy, 20*time.Second, 100*time.Millisecond,
			"before reset %d: both nodes should have a writer and see each other", i+1)

		a.resetPeerSession(pi, []string{recoveryTopic})
	}

	require.Eventually(t, healthy, 20*time.Second, 100*time.Millisecond,
		"after the resets both nodes must have a writer and see each other again")

	require.True(t, receives(t, b, aMsgs, 15*time.Second), "node-b's messages must still reach node-a")
	require.True(t, receives(t, a, bMsgs, 15*time.Second), "node-a's messages must still reach node-b")
}

// TestResetPeerStreamsKeepsConnection checks the stream-only reset used for a
// peer that relays for us: it rebuilds the gossipsub streams without closing
// the connection, so relay reservations and relayed connections survive.
func TestResetPeerStreamsKeepsConnection(t *testing.T) {
	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	a, _ := startTopicClient(t, "node-a", keyA, nil)
	b, bMsgs := startTopicClient(t, "node-b", keyB, []string{loopbackAddr(t, a)})
	bID := b.host.ID()

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) && a.hasOutboundPubSubStream(bID) },
		15*time.Second, 100*time.Millisecond, "node-b should join node-a's topic view")

	before := connIDs(a, bID)
	require.NotEmpty(t, before)

	require.Positive(t, a.resetPeerStreams(bID), "the reset must reset at least one gossipsub stream")

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) },
		15*time.Second, 100*time.Millisecond, "node-b should be back in the topic after the reset")
	require.ElementsMatch(t, before, connIDs(a, bID), "the reset must keep the existing connection")
	require.True(t, receives(t, a, bMsgs, 10*time.Second), "messages must flow after the reset")
}

// TestTopicPeerResetKeepsInboundOnlyConnection covers a managed peer that is
// only connected inbound. Its configured address may be stale, so closing the
// connection could leave us unable to dial it back; the reset falls back to the
// gossipsub streams and keeps the connection.
func TestTopicPeerResetKeepsInboundOnlyConnection(t *testing.T) {
	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	// node-b dials node-a; node-a never dials node-b.
	a, _ := startTopicClient(t, "node-a", keyA, nil)
	b, bMsgs := startTopicClient(t, "node-b", keyB, []string{loopbackAddr(t, a)})
	bID := b.host.ID()

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) && a.hasOutboundPubSubStream(bID) },
		15*time.Second, 100*time.Millisecond, "node-b should join node-a's topic view")
	require.False(t, a.hasOutboundConn(bID), "node-a must only be connected inbound")

	before := connIDs(a, bID)

	a.resetPeerSession(peer.AddrInfo{ID: bID}, []string{recoveryTopic})

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) && a.hasOutboundPubSubStream(bID) },
		15*time.Second, 100*time.Millisecond, "node-b should be back in the topic after the reset")
	require.ElementsMatch(t, before, connIDs(a, bID), "an inbound-only connection must be kept")
	require.True(t, receives(t, a, bMsgs, 10*time.Second), "messages must flow after the reset")
}

// TestTopicPeerResetCallbackPanicIsRecovered checks that a panicking
// OnTopicPeerReset cannot take the process down.
func TestTopicPeerResetCallbackPanicIsRecovered(t *testing.T) {
	logs := &captureLogger{}
	c := &client{
		config: Config{OnTopicPeerReset: func(string, []string) { panic("boom") }},
		logger: logs,
	}

	require.NotPanics(t, func() { c.notifyTopicPeerReset(peer.ID("peer-b"), []string{recoveryTopic}) })
	require.Contains(t, logs.String(), "OnTopicPeerReset panicked")
}

// TestCloseDoesNotWaitForeverOnBlockedWatchdog checks that a watchdog stuck in
// a blocking OnTopicPeerReset does not stop Close from tearing the host down.
func TestCloseDoesNotWaitForeverOnBlockedWatchdog(t *testing.T) {
	key, err := GeneratePrivateKey()
	require.NoError(t, err)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	cl, err := NewClient(Config{
		Name:             "node-a",
		PrivateKey:       key,
		AllowPrivateIPs:  true,
		Logger:           &captureLogger{},
		OnTopicPeerReset: func(string, []string) { <-release },
	})
	require.NoError(t, err)

	c := cl.(*client)
	addr := loopbackAddr(t, c)

	// Stand in for a watchdog round blocked in the callback.
	c.background.Add(1)
	go func() {
		defer c.background.Done()
		c.notifyTopicPeerReset(peer.ID("peer-b"), []string{recoveryTopic})
	}()

	require.NoError(t, cl.Close())

	require.Eventually(t, func() bool { return len(c.host.Network().ListenAddresses()) == 0 },
		closeTimeout, 50*time.Millisecond, "the host must be closed (%s)", addr)
}

// TestTopicPeerRecoveryRebuildsExhaustedWriter covers repeated stream resets.
// go-libp2p-pubsub respawns a dead writer for a still-connected peer only a
// few times within its backoff window; after that it deletes the peer's
// outbound queue and never recreates it while the connection stays up. A
// watchdog round must notice the missing writer and rebuild it without
// touching the connection.
func TestTopicPeerRecoveryRebuildsExhaustedWriter(t *testing.T) {
	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	a, _ := startTopicClient(t, "node-a", keyA, nil)
	b, bMsgs := startTopicClient(t, "node-b", keyB, []string{loopbackAddr(t, a)})
	bID := b.host.ID()

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) && a.hasOutboundPubSubStream(bID) },
		15*time.Second, 100*time.Millisecond, "node-a should have a writer to node-b")

	before := connIDs(a, bID)

	// Exhaust pubsub's writer-respawn budget for node-b.
	for i := 0; i < 6; i++ {
		a.resetPeerStreams(bID)
		_ = waitFor(3*time.Second, func() bool { return a.hasOutboundPubSubStream(bID) })
	}

	// One watchdog round must restore the writer.
	w := newTopicPeerWatchdog(time.Minute, time.Minute, true)
	a.checkTopicPeers(w, []peer.AddrInfo{{ID: bID}}, time.Now())

	require.Eventually(t, func() bool { return a.hasOutboundPubSubStream(bID) },
		10*time.Second, 100*time.Millisecond, "node-a must have a writer to node-b again")
	require.True(t, receives(t, a, bMsgs, 15*time.Second), "node-a's messages must reach node-b again")
	require.ElementsMatch(t, before, connIDs(a, bID), "recovery must keep the existing connection")
}
