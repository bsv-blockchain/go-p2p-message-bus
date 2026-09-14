package p2p

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloseUnderGossipLoadDoesNotPanic reproduces the shutdown race: a
// subscription reader that has just pulled a buffered message from
// Subscription.Next enters its channel-send select while Close is closing
// that channel. With close ownership on the consumer side that is a
// "send on closed channel" panic; with the reader owning the close, the
// reader exits on context cancellation and closes the channel itself.
//
// The consumer deliberately stops draining after the mesh is up so the
// reader alternates between a full buffer and fresh sends at Close time.
func TestCloseUnderGossipLoadDoesNotPanic(t *testing.T) {
	c1, c2 := startStaticPeerPair(t)

	const topic = "close-under-load"
	msgChan := c1.Subscribe(topic)
	_ = c2.Subscribe(topic)

	require.Eventually(t, connectedness(c2, c1), 6*time.Second, 100*time.Millisecond)

	// Publisher keeps the topic busy until the test ends. Lightly throttled:
	// the race only needs the reader's buffer full, not a saturated core.
	stopPublishing := make(chan struct{})
	var publishers sync.WaitGroup
	publishers.Add(1)
	go func() {
		defer publishers.Done()
		for i := 0; ; i++ {
			select {
			case <-stopPublishing:
				return
			case <-time.After(time.Millisecond):
			}
			_ = c2.Publish(context.Background(), topic, []byte("m"+strconv.Itoa(i)))
		}
	}()
	defer func() {
		close(stopPublishing)
		publishers.Wait()
	}()

	// Wait for the gossip mesh to deliver at least one message to c1.
	require.Eventually(t, func() bool {
		select {
		case _, ok := <-msgChan:
			return ok
		default:
			return false
		}
	}, 15*time.Second, 50*time.Millisecond, "c1 must receive gossip from c2 before the shutdown race is exercised")

	// Let the reader's buffer fill while nobody drains it, so the reader is
	// parked in its send when Close runs: the exact state that panicked
	// when Close closed the channel under it.
	require.Eventually(t, func() bool { return len(msgChan) == cap(msgChan) },
		5*time.Second, 10*time.Millisecond, "reader buffer must be full so the reader is parked in the send")

	require.NoError(t, c1.Close())

	// Close must have waited for the reader: by the time it returns the
	// channel is already closed, so a non-blocking drain must terminate on
	// the closed channel rather than run dry on an open one.
	requireDrainsToClosed(t, msgChan, "subscription channel must be closed by its reader before Close returns")
}

// requireDrainsToClosed drains ch without blocking and fails unless the drain
// ends on a closed channel, i.e. the channel was already closed when called.
func requireDrainsToClosed(t *testing.T, ch <-chan Message, msg string) {
	t.Helper()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			t.Fatal(msg)
		}
	}
}

// TestSubscribeAfterCloseReturnsClosedChannel guards the Add-after-Wait side
// of the same ownership change: once Close has begun, Subscribe must not
// register a new reader (a WaitGroup misuse) and must hand the caller a
// channel that terminates a range loop immediately.
func TestSubscribeAfterCloseReturnsClosedChannel(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{Name: testPeerName, PrivateKey: privKey})
	require.NoError(t, err)
	require.NoError(t, cl.Close())

	ch := cl.Subscribe("late-topic")
	requireDrainsToClosed(t, ch, "channel from a post-Close Subscribe must be closed immediately")

	require.NoError(t, cl.Close(), "Close must stay idempotent")
}

// TestSubscribeConcurrentWithClose exercises the closed/readers interlock
// under the race detector: Subscribe calls overlapping Close must either be
// tracked (and have their channel closed by the reader) or refused with an
// already-closed channel, never Add to a draining WaitGroup.
func TestSubscribeConcurrentWithClose(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{Name: testPeerName, PrivateKey: privKey})
	require.NoError(t, err)

	const subscribers = 20
	channels := make([]<-chan Message, subscribers)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < subscribers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			channels[i] = cl.Subscribe("racy-topic-" + strconv.Itoa(i))
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		assert.NoError(t, cl.Close())
	}()

	close(start)
	wg.Wait()

	for i, ch := range channels {
		require.Eventually(t, func() bool {
			select {
			case _, ok := <-ch:
				return !ok
			default:
				return false
			}
		}, 5*time.Second, 10*time.Millisecond, "channel %d must end up closed whether its Subscribe won or lost the race with Close", i)
	}
}
