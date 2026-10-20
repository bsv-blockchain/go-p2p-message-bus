package p2p

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

const recoveryTopic = "recovery-topic"

// startTopicClient starts a client with the given key, subscribed to
// recoveryTopic, with staticPeers as its managed peers. The caller owns Close.
func startTopicClient(t *testing.T, name string, key crypto.PrivKey, staticPeers []string) (*client, <-chan Message) {
	t.Helper()

	return startTopicClientOnPort(t, name, key, 0, staticPeers)
}

// startTopicClientOnPort is startTopicClient listening on a fixed port.
func startTopicClientOnPort(t *testing.T, name string, key crypto.PrivKey, port int, staticPeers []string) (*client, <-chan Message) {
	t.Helper()

	logs := &captureLogger{}

	cl, err := NewClient(Config{
		Name:            name,
		PrivateKey:      key,
		Port:            port,
		AllowPrivateIPs: true,
		StaticPeers:     staticPeers,
		Logger:          logs,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("--- %s logs ---\n%s", name, logs.String())
		}
	})

	c := cl.(*client)

	return c, c.Subscribe(recoveryTopic)
}

// freePort returns a TCP port that was free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	return port
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
			if ok && string(msg.Data) == string(payload) {
				return true
			}
		case <-tick.C:
		case <-deadline:
			return false
		}
	}
}

// TestTopicPeerRecoversAfterOverlappingRestarts reproduces a rolling update of
// a node whose identity is fixed: a new process with the same private key
// starts and connects while the old one is still alive, then the old one
// exits. Repeated a few times in quick succession, as a deployment rollout plus
// the managed-peer reconnect loop can do, this leaves the long-lived peer with
// a live connection to the node but no gossipsub session: the node never
// reappears in the topic and messages stop flowing, with nothing on either side
// that recovers it. The client must notice and re-establish the session.
func TestTopicPeerRecoversAfterOverlappingRestarts(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process restart scenario takes tens of seconds; skip in -short")
	}

	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	// As in a teranode deployment, each node lists the other as a managed peer
	// by stable peer ID. node-b's first process listens on a known port so
	// node-a's entry can be written before either starts; later node-b
	// processes come up on other ports (a new pod gets a new IP) and dial in.
	bID, err := peer.IDFromPrivateKey(keyB)
	require.NoError(t, err)

	bPort := freePort(t)

	a, _ := startTopicClient(t, "node-a", keyA, []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", bPort, bID)})
	t.Cleanup(func() { _ = a.Close() })

	aAddr := loopbackAddr(t, a)

	b, bMsgs := startTopicClientOnPort(t, "node-b", keyB, bPort, []string{aAddr})

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

	t.Cleanup(func() { _ = b.Close() })

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) },
		90*time.Second, 250*time.Millisecond,
		"after overlapping restarts node-a must see node-b in the topic again")
	require.True(t, receives(t, a, bMsgs, 30*time.Second),
		"after overlapping restarts node-a's messages must reach node-b again")
}

// TestTopicPeerRecoveryIgnoresPublishOnlyTopics checks that the watchdog only
// judges topics this client subscribes to. Publish joins a topic without
// subscribing, so a healthy managed peer is never a member of it from our
// point of view; treating it as missing would disconnect the peer every
// minimum interval even with ManagedPeersShareTopics set.
func TestTopicPeerRecoveryIgnoresPublishOnlyTopics(t *testing.T) {
	const publishOnlyTopic = "publish-only-topic"

	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	bID, err := peer.IDFromPrivateKey(keyB)
	require.NoError(t, err)

	bPort := freePort(t)
	bAddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", bPort, bID)

	var resets []string

	// The background watchdog is off so the test drives the rounds itself; the
	// reset callback still records anything resetPeerSession does.
	clA, err := NewClient(Config{
		Name:                     "node-a",
		PrivateKey:               keyA,
		AllowPrivateIPs:          true,
		StaticPeers:              []string{bAddr},
		Logger:                   &captureLogger{},
		DisableTopicPeerRecovery: true,
		ManagedPeersShareTopics:  true,
		OnTopicPeerReset:         func(id string, topics []string) { resets = append(resets, fmt.Sprintf("%s %v", id, topics)) },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = clA.Close() })

	a := clA.(*client)
	a.Subscribe(recoveryTopic)

	b, _ := startTopicClientOnPort(t, "node-b", keyB, bPort, []string{loopbackAddr(t, a)})
	t.Cleanup(func() { _ = b.Close() })

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) },
		15*time.Second, 100*time.Millisecond, "node-b should join node-a's topic view")

	// Publishing joins the topic locally without subscribing to it.
	require.NoError(t, a.Publish(context.Background(), publishOnlyTopic, []byte("hello")))

	managed := []peer.AddrInfo{{ID: bID}}
	w := newTopicPeerWatchdog(time.Second, time.Minute, true)
	now := time.Now()

	a.checkTopicPeers(context.Background(), w, managed, now)
	a.checkTopicPeers(context.Background(), w, managed, now.Add(2*time.Second))

	require.Empty(t, resets, "a healthy peer must not be reset over a topic we only publish to")
	require.Equal(t, network.Connected, a.host.Network().Connectedness(bID), "node-b must stay connected")
}

// TestTopicPeerResetKeepsConnection checks that a session reset rebuilds the
// gossipsub streams without closing the connection. Bootstrap peers double as
// relays, so closing the connection would drop relay reservations and relayed
// connections to unrelated peers.
func TestTopicPeerResetKeepsConnection(t *testing.T) {
	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	a, _ := startTopicClient(t, "node-a", keyA, nil)
	t.Cleanup(func() { _ = a.Close() })

	b, bMsgs := startTopicClient(t, "node-b", keyB, []string{loopbackAddr(t, a)})
	t.Cleanup(func() { _ = b.Close() })

	bID := b.host.ID()
	require.Eventually(t, func() bool { return topicHasPeer(a, bID) },
		15*time.Second, 100*time.Millisecond, "node-b should join node-a's topic view")

	connIDs := func() []string {
		conns := a.host.Network().ConnsToPeer(bID)
		ids := make([]string, 0, len(conns))
		for _, conn := range conns {
			ids = append(ids, conn.ID())
		}
		return ids
	}

	before := connIDs()
	require.NotEmpty(t, before)

	a.resetPeerSession(context.Background(), peer.AddrInfo{ID: bID}, []string{recoveryTopic})

	require.Eventually(t, func() bool { return topicHasPeer(a, bID) },
		15*time.Second, 100*time.Millisecond, "node-b should be back in the topic after the reset")
	require.ElementsMatch(t, before, connIDs(), "the reset must keep the existing connection")
	require.True(t, receives(t, a, bMsgs, 10*time.Second), "messages must flow after the reset")
}
