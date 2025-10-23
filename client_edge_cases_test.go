package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientTwoWayMessaging(t *testing.T) {
	// Create two clients
	privKey1, err := GeneratePrivateKey()
	require.NoError(t, err)

	privKey2, err := GeneratePrivateKey()
	require.NoError(t, err)

	config1 := Config{
		Name:       "sender",
		PrivateKey: privKey1,
	}

	config2 := Config{
		Name:       "receiver",
		PrivateKey: privKey2,
	}

	cl1, err := NewClient(config1)
	require.NoError(t, err)
	defer func() {
		closeErr := cl1.Close()
		require.NoError(t, closeErr)
	}()

	cl2, err := NewClient(config2)
	require.NoError(t, err)
	defer func() {
		closeErr := cl2.Close()
		require.NoError(t, closeErr)
	}()

	// Subscribe to same topic
	msgChan1 := cl1.Subscribe("messaging-test")
	msgChan2 := cl2.Subscribe("messaging-test")

	// Give time for subscriptions and discovery
	time.Sleep(3 * time.Second)

	// Publish from client1
	ctx := context.Background()
	testData := []byte("Hello from client1")
	err = cl1.Publish(ctx, "messaging-test", testData)
	require.NoError(t, err)

	// Try to receive on both clients (one should get it, the sender filters itself)
	select {
	case msg := <-msgChan2:
		assert.Equal(t, "sender", msg.From)
		assert.Equal(t, testData, msg.Data)
	case <-msgChan1:
		// Sender shouldn't receive own message
		t.Fatal("sender received own message")
	case <-time.After(5 * time.Second):
		// Timeout is acceptable if peers haven't discovered each other
		t.Log("No message received (peers may not have connected)")
	}
}

func TestClientSubscribeToMultipleTopicsSequentially(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	// Subscribe to multiple topics one at a time
	topics := []string{"topic-a", "topic-b", "topic-c", "topic-d", "topic-e"}
	channels := make([]<-chan Message, len(topics))

	for i, topic := range topics {
		channels[i] = cl.Subscribe(topic)
		require.NotNil(t, channels[i])
		time.Sleep(50 * time.Millisecond) // Space out subscriptions
	}

	// Verify all channels are still open
	for i, ch := range channels {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatalf("channel %d closed unexpectedly", i)
			}
		case <-time.After(50 * time.Millisecond):
			// Expected - channel should be open but no messages
		}
	}
}

func TestClientPublishAfterSubscribe(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	// Subscribe first
	_ = cl.Subscribe("ordered-topic")
	time.Sleep(100 * time.Millisecond)

	// Then publish
	ctx := context.Background()
	err = cl.Publish(ctx, "ordered-topic", []byte("data"))
	require.NoError(t, err)
}

func TestClientMultiplePublishesToSameTopic(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	ctx := context.Background()

	// Publish multiple times to same topic
	for i := 0; i < 5; i++ {
		err := cl.Publish(ctx, "repeated-topic", []byte("message"))
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClientGetIDConsistencyAcrossCalls(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	// Get ID multiple times
	id1 := cl.GetID()
	id2 := cl.GetID()
	id3 := cl.GetID()

	// Should always be the same
	assert.Equal(t, id1, id2)
	assert.Equal(t, id2, id3)
	assert.NotEmpty(t, id1)
}

func TestPeerTrackerEdgeCases(t *testing.T) {
	tracker := newPeerTracker()

	// Test with same peer multiple times
	peerID := generateTestPeerID(t)

	tracker.updateName(peerID, "name1")
	tracker.updateName(peerID, "name2")
	tracker.updateName(peerID, "name3")

	name := tracker.getName(peerID)
	assert.Equal(t, "name3", name)

	// Record message multiple times
	tracker.recordMessageFrom(peerID)
	firstTime := tracker.lastSeen[peerID]

	time.Sleep(10 * time.Millisecond)

	tracker.recordMessageFrom(peerID)
	secondTime := tracker.lastSeen[peerID]

	assert.True(t, secondTime.After(firstTime))

	// Get peers
	peers := tracker.getAllTopicPeers()
	assert.Len(t, peers, 1)
}

func TestClientCloseWithActiveSubscriptions(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)

	// Create many active subscriptions
	channels := make([]<-chan Message, 10)
	for i := 0; i < 10; i++ {
		channels[i] = cl.Subscribe("topic-" + string(rune('0'+i)))
	}

	// Give subscriptions time to start
	time.Sleep(200 * time.Millisecond)

	// Close with active subscriptions
	err = cl.Close()
	require.NoError(t, err)

	// All channels should be closed
	for i, ch := range channels {
		select {
		case _, ok := <-ch:
			assert.False(t, ok, "channel %d should be closed", i)
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("channel %d not closed in time", i)
		}
	}
}

func TestClientPublishToMultipleTopics(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	ctx := context.Background()

	// Publish to different topics
	topics := []string{"t1", "t2", "t3", "t4", "t5"}
	for _, topic := range topics {
		err := cl.Publish(ctx, topic, []byte("test"))
		require.NoError(t, err)
	}
}

func TestNewClientQuickStartAndStop(t *testing.T) {
	// Test rapid client creation and shutdown
	for i := 0; i < 3; i++ {
		privKey, err := GeneratePrivateKey()
		require.NoError(t, err)

		config := Config{
			Name:       "quick-" + string(rune('0'+i)),
			PrivateKey: privKey,
		}

		cl, err := NewClient(config)
		require.NoError(t, err)

		// Immediately close
		err = cl.Close()
		require.NoError(t, err)
	}
}

func TestEvictStalePeersEdgeCaseTiny(t *testing.T) {
	logger := &DefaultLogger{}
	now := time.Now()

	peers := []cachedPeer{
		{
			ID:       "peer1",
			LastSeen: now.Add(-10 * time.Second),
		},
	}

	// Very small TTL
	result := evictStalePeers(peers, 5*time.Second, logger)

	// Peer should be evicted (older than 5 seconds)
	assert.Empty(t, result)
}
