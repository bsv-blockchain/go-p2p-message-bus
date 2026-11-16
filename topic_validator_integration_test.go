package p2p

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

// TestTopicValidatorRejectsBeforeRelay pins that a topic validator drops an
// oversized message before GossipSub forwards it, over a line topology:
//
//	A <-> B <-> C
//
// B validates the topic; A and C do not. A publishes a small and an oversized
// payload. The small one must reach C, which proves B is relaying A's traffic;
// the oversized one must never reach C. Without the validator B forwards both,
// because GossipSub relays before the application sees a message.
//
// The nodes come from newLineTopologyClient, whose DHT-off setting keeps A and
// C from discovering and dialing each other, which would bypass B.
func TestTopicValidatorRejectsBeforeRelay(t *testing.T) {
	const (
		topicName  = "topic-validator-relay-test"
		maxPayload = 1024
	)

	small := []byte("small")
	oversized := bytes.Repeat([]byte("x"), 2*maxPayload)

	newNode := func(name string, validators map[string]TopicValidator, log logger) (*client, peer.ID) {
		key, err := GeneratePrivateKey()
		require.NoError(t, err)

		id, err := peer.IDFromPrivateKey(key)
		require.NoError(t, err)

		cl := newLineTopologyClient(t, Config{Name: name, PrivateKey: key, TopicValidators: validators, Logger: log})

		return cl.(*client), id
	}

	logB := &captureLogger{}

	clA, idA := newNode("peer-a", nil, &captureLogger{})
	clB, _ := newNode("peer-b", map[string]TopicValidator{topicName: {MaxPayloadSize: maxPayload}}, logB)
	clC, idC := newNode("peer-c", nil, &captureLogger{})

	require.NotNil(t, clA.Subscribe(topicName))
	chB := clB.Subscribe(topicName)
	chC := clC.Subscribe(topicName)

	require.NoError(t, clB.Connect(clB.ctx, loopbackAddr(t, clA)))
	require.NoError(t, clC.Connect(clC.ctx, loopbackAddr(t, clB)))

	requireNotConnected(t, clA, idC, "before publishing")

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = clA.Publish(context.Background(), topicName, small)
				_ = clA.Publish(context.Background(), topicName, oversized)
			}
		}
	}()

	requireOversizedNeverRelayed(t, chB, chC, idA, small, maxPayload)

	// B's silence must come from the validator, not from oversized messages
	// never arriving at B.
	require.Eventually(t, func() bool {
		return strings.Contains(logB.String(), "on topic "+topicName+" that failed validation")
	}, 15*time.Second, 50*time.Millisecond, "B never reported rejecting a message")

	requireNotConnected(t, clA, idC, "after the observation window")
}

// requireOversizedNeverRelayed watches B's and C's subscriptions until C has
// seen A's small payload (the positive control: B is relaying A's traffic) and
// at least 5s have passed with nothing larger than maxPayload at B or C.
// Deadlines are set once, outside the loop, so the steady publish stream
// cannot reset them.
func requireOversizedNeverRelayed(t *testing.T, chB, chC <-chan Message, idA peer.ID, small []byte, maxPayload int) {
	t.Helper()

	observationDeadline := time.After(5 * time.Second)
	controlDeadline := time.After(30 * time.Second)

	var observed, smallAtC bool

	for !observed || !smallAtC {
		select {
		case msg := <-chB:
			require.LessOrEqual(t, len(msg.Data), maxPayload, "B delivered an oversized message to its subscriber")
		case msg := <-chC:
			require.LessOrEqual(t, len(msg.Data), maxPayload, "B relayed an oversized message to C")

			if msg.FromID == idA.String() && bytes.Equal(msg.Data, small) {
				smallAtC = true
			}
		case <-observationDeadline:
			observed = true
			observationDeadline = nil
		case <-controlDeadline:
			t.Fatal("C never received A's small payload: B was not relaying, so C's silence about the oversized one proves nothing")
		}
	}
}

// TestMaxMessageSizeDropsOversizedFrames pins that Config.MaxMessageSize
// reaches pubsub. A publishes payloads larger than capped's limit to two
// direct neighbors: uncapped (the positive control, proving A publishes into a
// live mesh) must receive them and capped must not.
func TestMaxMessageSizeDropsOversizedFrames(t *testing.T) {
	const (
		topicName = "max-message-size-test"
		limit     = 4 * 1024
	)

	payload := bytes.Repeat([]byte("x"), 2*limit)

	newNode := func(name string, maxMessageSize int) *client {
		key, err := GeneratePrivateKey()
		require.NoError(t, err)

		return newLineTopologyClient(t, Config{Name: name, PrivateKey: key, MaxMessageSize: maxMessageSize}).(*client)
	}

	clA := newNode("peer-a", 0)
	capped := newNode("capped", limit)
	uncapped := newNode("uncapped", 0)

	require.NotNil(t, clA.Subscribe(topicName))
	chCapped := capped.Subscribe(topicName)
	chUncapped := uncapped.Subscribe(topicName)

	require.NoError(t, capped.Connect(capped.ctx, loopbackAddr(t, clA)))
	require.NoError(t, uncapped.Connect(uncapped.ctx, loopbackAddr(t, clA)))

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = clA.Publish(context.Background(), topicName, payload)
			}
		}
	}()

	observationDeadline := time.After(5 * time.Second)
	controlDeadline := time.After(30 * time.Second)

	var observed, gotAtUncapped bool

	for !observed || !gotAtUncapped {
		select {
		case <-chCapped:
			t.Fatal("a node with MaxMessageSize below the frame size received the message")
		case <-chUncapped:
			gotAtUncapped = true
		case <-observationDeadline:
			observed = true
			observationDeadline = nil
		case <-controlDeadline:
			t.Fatal("the uncapped node never received A's message: A was not publishing into a live mesh")
		}
	}
}
