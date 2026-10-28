package p2p

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/stretchr/testify/require"
)

const restartTopic = "restart-topic"

// startRestartClient starts a client with the given key, subscribed to
// restartTopic, with staticPeers as its static peers. Close is registered as a
// test cleanup straight away, so a failing assertion never leaks the client;
// Close is idempotent, so tests may also close it earlier.
func startRestartClient(t *testing.T, name string, key crypto.PrivKey, staticPeers []string) (*client, <-chan Message) {
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

	return c, c.Subscribe(restartTopic)
}

// receivesOn reports whether a message published by pub on restartTopic shows
// up on ch within wait.
func receivesOn(t *testing.T, pub *client, ch <-chan Message, wait time.Duration) bool {
	t.Helper()

	payload := fmt.Sprintf("probe-%d", time.Now().UnixNano())
	deadline := time.After(wait)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()

	for {
		_ = pub.Publish(context.Background(), restartTopic, []byte(payload))

		select {
		case msg, ok := <-ch:
			if !ok {
				return false
			}
			if string(msg.Data) == payload {
				return true
			}
		case <-tick.C:
		case <-deadline:
			return false
		}
	}
}

// TestTopicPeerSurvivesOverlappingRestarts reproduces a rolling update of a
// node whose identity is fixed: a new process with the same private key starts
// and connects while the old one is still alive, then the old one exits.
//
// On stock go-libp2p-pubsub (v0.18.0, v0.19.0) the old connection's death
// clears the topic state the new process had just announced, so the
// long-lived peer keeps a live connection to the node but never sees it in the
// topic again; and after four such restarts within ten minutes it also stops
// respawning its writer to the node. go.mod replaces pubsub with a fork that
// fixes both; this test fails without it.
func TestTopicPeerSurvivesOverlappingRestarts(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process restart scenario takes tens of seconds; skip in -short")
	}

	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)
	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	// As in a teranode deployment, node-a lists node-b as a static peer.
	// node-b's first process starts first so node-a can be given its real
	// address; later node-b processes come up elsewhere (a new pod gets a new
	// IP) and dial in.
	b, bMsgs := startRestartClient(t, "node-b", keyB, nil)
	bID := b.host.ID()

	a, _ := startRestartClient(t, "node-a", keyA, []string{loopbackAddr(t, b)})
	aAddr := loopbackAddr(t, a)

	inTopic := func() bool { return slices.Contains(a.TopicPeers(restartTopic), bID.String()) }

	require.Eventually(t, inTopic, 15*time.Second, 100*time.Millisecond, "node-b should join node-a's topic view")
	require.True(t, receivesOn(t, a, bMsgs, 10*time.Second), "baseline: node-a's messages should reach node-b")

	// More restarts than pubsub's dead-peer backoff allows writer respawns
	// (four per ten minutes), each settled so that every old connection's death
	// really kills node-a's writer to node-b.
	const restarts = 7
	for i := 1; i <= restarts; i++ {
		next, nextMsgs := startRestartClient(t, "node-b", keyB, []string{aAddr})

		require.Eventually(t, func() bool {
			return a.host.Network().Connectedness(bID) == network.Connected &&
				len(a.host.Network().ConnsToPeer(bID)) > 1
		}, 15*time.Second, 50*time.Millisecond, "restart %d should connect while the old process is alive", i)

		// Let node-a process the new process's subscriptions.
		time.Sleep(time.Second)

		require.NoError(t, b.Close())

		require.Eventually(t, func() bool { return len(a.host.Network().ConnsToPeer(bID)) == 1 },
			15*time.Second, 50*time.Millisecond, "restart %d: the old connection should go away", i)

		require.Eventually(t, inTopic, 10*time.Second, 100*time.Millisecond,
			"restart %d: node-a must still see node-b in the topic", i)
		require.True(t, receivesOn(t, a, nextMsgs, 10*time.Second),
			"restart %d: node-a's messages must reach the new node-b", i)

		b = next
	}
}
