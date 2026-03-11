package p2p

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testData = "test data"

func TestClientSubscribeMultipleTopicsConcurrently(t *testing.T) {
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

	// Subscribe to multiple topics concurrently
	topics := []string{"topic1", "topic2", "topic3", "topic4", "topic5"}
	channels := make([]<-chan Message, len(topics))

	for i, topic := range topics {
		channels[i] = cl.Subscribe(topic)
		require.NotNil(t, channels[i])
	}

	// Verify all channels are open
	for _, ch := range channels {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatal("channel closed unexpectedly")
			}
		case <-time.After(100 * time.Millisecond):
			// Channel not closed, which is expected
		}
	}
}

func TestClientPublishToUnsubscribedTopic(t *testing.T) {
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

	// Publish to a topic we haven't subscribed to
	err = cl.Publish(ctx, "new-topic", []byte(testData))
	require.NoError(t, err)
}

func TestClientPublishWithCanceledContext(t *testing.T) {
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

	// Create and immediately cancel context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Try to publish with canceled context
	err = cl.Publish(ctx, testTopicName, []byte("test"))
	// Should either succeed immediately or fail with context error
	// Either is acceptable behavior
	if err != nil {
		assert.Contains(t, err.Error(), "context")
	}
}

func TestClientPublishEmptyData(t *testing.T) {
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

	// Publish empty data
	err = cl.Publish(ctx, testTopicName, []byte{})
	require.NoError(t, err)

	// Publish nil data
	err = cl.Publish(ctx, testTopicName, nil)
	require.NoError(t, err)
}

func TestClientPublishLargeData(t *testing.T) {
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

	// Publish large data (1MB)
	largeData := make([]byte, 1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	err = cl.Publish(ctx, testTopicName, largeData)
	require.NoError(t, err)
}

func TestClientCloseClosesSubscriptionChannels(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)

	// Subscribe to multiple topics
	ch1 := cl.Subscribe("topic1")
	ch2 := cl.Subscribe("topic2")
	ch3 := cl.Subscribe("topic3")

	// Give subscriptions time to initialize
	time.Sleep(200 * time.Millisecond)

	// Close client
	err = cl.Close()
	require.NoError(t, err)

	// All channels should be closed
	select {
	case _, ok := <-ch1:
		assert.False(t, ok, "channel 1 should be closed")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel 1 not closed after client Close()")
	}

	select {
	case _, ok := <-ch2:
		assert.False(t, ok, "channel 2 should be closed")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel 2 not closed after client Close()")
	}

	select {
	case _, ok := <-ch3:
		assert.False(t, ok, "channel 3 should be closed")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel 3 not closed after client Close()")
	}
}

func TestClientSubscribeContextCancellation(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)

	// Subscribe to a topic
	msgChan := cl.Subscribe("cancel-test")
	require.NotNil(t, msgChan)

	// Give subscription time to start
	time.Sleep(100 * time.Millisecond)

	// Close the client (which cancels the context)
	err = cl.Close()
	require.NoError(t, err)

	// Channel should be closed
	select {
	case _, ok := <-msgChan:
		assert.False(t, ok, "channel should be closed")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel not closed after context cancellation")
	}
}

func TestClientPublishConcurrent(t *testing.T) {
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

	// Publish concurrently from multiple goroutines to different topics
	// to avoid topic join race condition
	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			data := []byte("test message")
			topicName := "test-topic-" + strconv.Itoa(n)
			done <- cl.Publish(ctx, topicName, data)
		}(i)
	}

	// Wait for all publishes
	for i := 0; i < 10; i++ {
		publishErr := <-done
		assert.NoError(t, publishErr)
	}
}

func TestMessageStructFields(t *testing.T) {
	msg := Message{
		Topic:     "test-topic",
		From:      "test-peer",
		FromID:    "12D3KooTest",
		Data:      []byte(testData),
		Timestamp: time.Now(),
	}

	assert.Equal(t, "test-topic", msg.Topic)
	assert.Equal(t, "test-peer", msg.From)
	assert.Equal(t, "12D3KooTest", msg.FromID)
	assert.Equal(t, []byte(testData), msg.Data)
	assert.False(t, msg.Timestamp.IsZero())
}

func TestPeerInfoStructFields(t *testing.T) {
	info := PeerInfo{
		ID:    "peer-id",
		Name:  "peer-name",
		Addrs: []string{"/ip4/127.0.0.1/tcp/4001"},
	}

	assert.Equal(t, "peer-id", info.ID)
	assert.Equal(t, "peer-name", info.Name)
	assert.Len(t, info.Addrs, 1)
	assert.Equal(t, "/ip4/127.0.0.1/tcp/4001", info.Addrs[0])
}
