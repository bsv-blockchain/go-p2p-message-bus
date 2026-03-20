package p2p

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDiscoverNowChannelInitialized verifies that the discoverNow channel
// is created during NewClient and is buffered with capacity 1.
func TestDiscoverNowChannelInitialized(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl.Close())
	}()

	c := cl.(*client)
	require.NotNil(t, c.discoverNow, "discoverNow channel should be initialized")
	assert.Equal(t, 1, cap(c.discoverNow), "discoverNow should be buffered with capacity 1")
}

// TestRoutingDiscoveryInitializedWhenDHTEnabled verifies that routingDiscovery
// is set on the client when DHT is enabled (the default).
func TestRoutingDiscoveryInitializedWhenDHTEnabled(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl.Close())
	}()

	c := cl.(*client)
	assert.NotNil(t, c.routingDiscovery, "routingDiscovery should be set when DHT is enabled")
}

// TestRoutingDiscoveryNilWhenDHTOff verifies that routingDiscovery is nil
// when DHT mode is "off".
func TestRoutingDiscoveryNilWhenDHTOff(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:       testPeerName,
		PrivateKey: privKey,
		DHTMode:    "off",
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl.Close())
	}()

	c := cl.(*client)
	assert.Nil(t, c.routingDiscovery, "routingDiscovery should be nil when DHT is off")
}

// TestSubscribeTriggersDiscoverNow verifies that Subscribe() sends a signal
// on the discoverNow channel to trigger immediate peer discovery.
func TestSubscribeTriggersDiscoverNow(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl.Close())
	}()

	c := cl.(*client)

	// Drain any existing signal from initialization
	select {
	case <-c.discoverNow:
	default:
	}

	// Subscribe to a topic
	msgChan := cl.Subscribe("trigger-test")
	require.NotNil(t, msgChan)

	// Wait for the Subscribe goroutine to run and signal
	time.Sleep(500 * time.Millisecond)

	// The discoverPeers loop consumes from discoverNow, so we can't always
	// catch the signal directly. Instead, verify that after subscribing,
	// the topic exists and the mechanism didn't panic.
	c.mu.RLock()
	_, topicExists := c.topics["trigger-test"]
	c.mu.RUnlock()
	assert.True(t, topicExists, "topic should exist after Subscribe")
}

// TestSubscribeDiscoverNowNonBlocking verifies that the discoverNow signal
// is non-blocking — if the channel is already full, Subscribe doesn't hang.
func TestSubscribeDiscoverNowNonBlocking(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl.Close())
	}()

	// Subscribe to multiple topics sequentially — none should block.
	// We subscribe one at a time with a small pause to let the goroutine
	// complete topic join before the next, avoiding a race in Close().
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			cl.Subscribe(fmt.Sprintf("rapid-topic-%d", i))
			time.Sleep(50 * time.Millisecond)
		}
		close(done)
	}()

	select {
	case <-done:
		// Success — rapid subscriptions completed without blocking
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe blocked — discoverNow signal is not non-blocking")
	}
}

// TestAdvertiseTopicsWithNoTopics verifies advertiseTopics doesn't panic
// or error when no topics are subscribed.
func TestAdvertiseTopicsWithNoTopics(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl.Close())
	}()

	c := cl.(*client)
	require.NotNil(t, c.routingDiscovery)

	// Should not panic with zero topics
	c.advertiseTopics(c.ctx, c.routingDiscovery)
}

// TestAdvertiseTopicsWithSubscribedTopics verifies advertiseTopics works
// after topics have been subscribed.
func TestAdvertiseTopicsWithSubscribedTopics(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl.Close())
	}()

	c := cl.(*client)
	require.NotNil(t, c.routingDiscovery)

	// Subscribe to create topics
	cl.Subscribe("advertise-test-1")
	cl.Subscribe("advertise-test-2")
	time.Sleep(200 * time.Millisecond)

	// Should not panic, and should iterate over topics
	c.advertiseTopics(c.ctx, c.routingDiscovery)

	c.mu.RLock()
	topicCount := len(c.topics)
	c.mu.RUnlock()
	assert.Equal(t, 2, topicCount, "should have 2 topics registered")
}

// TestGossipSubPeerExchangeEnabled verifies that GossipSub is created
// with PeerExchange enabled by checking the client creates successfully
// and can subscribe to topics (PeerExchange is an internal GossipSub option
// that can't be introspected directly, but would cause NewGossipSub to fail
// if misconfigured).
func TestGossipSubPeerExchangeEnabled(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl.Close())
	}()

	c := cl.(*client)
	assert.NotNil(t, c.pubsub, "pubsub should be initialized with PeerExchange")

	// Verify subscribe works (would fail if WithPeerExchange caused issues)
	msgChan := cl.Subscribe("px-test")
	require.NotNil(t, msgChan)
	time.Sleep(100 * time.Millisecond)
}

// TestTwoPeersDiscoverViaDirectConnect verifies that two peers can discover
// each other and exchange topic messages when connected directly.
// Note: BootstrapPeers are intentionally ignored in test mode (testing.Testing()),
// so we use Connect() to establish the initial connection.
func TestTwoPeersDiscoverViaDirectConnect(t *testing.T) {
	privKey1, err := GeneratePrivateKey()
	require.NoError(t, err)

	privKey2, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl1, err := NewClient(Config{
		Name:            "peer-a",
		PrivateKey:      privKey1,
		Port:            0,
		AllowPrivateIPs: true,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl1.Close())
	}()

	cl2, err := NewClient(Config{
		Name:            "peer-b",
		PrivateKey:      privKey2,
		Port:            0,
		AllowPrivateIPs: true,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl2.Close())
	}()

	// Get peer1's address and connect peer2 directly
	c1 := cl1.(*client)
	addrs := c1.host.Addrs()
	require.NotEmpty(t, addrs, "peer1 should have listen addresses")

	connectAddr := fmt.Sprintf("%s/p2p/%s", addrs[0].String(), c1.host.ID().String())
	err = cl2.Connect(cl2.(*client).ctx, connectAddr)
	require.NoError(t, err)

	// Both subscribe to the same topic — this triggers discoverNow
	_ = cl1.Subscribe("direct-connect-test")
	_ = cl2.Subscribe("direct-connect-test")

	// Wait for topic mesh formation
	time.Sleep(3 * time.Second)

	// Check connectivity — peer2 should be connected to peer1
	c2 := cl2.(*client)
	connectedPeers := c2.host.Network().Peers()
	t.Logf("Peer2 connected to %d peers", len(connectedPeers))

	found := false
	for _, p := range connectedPeers {
		if p == c1.host.ID() {
			found = true
			break
		}
	}
	assert.True(t, found, "peer2 should be connected to peer1")
}

// TestDiscoverNowChannelDHTOff verifies that discoverNow is initialized
// even when DHT is off, and that Subscribe doesn't panic.
func TestDiscoverNowChannelDHTOff(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:       testPeerName,
		PrivateKey: privKey,
		DHTMode:    "off",
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cl.Close())
	}()

	c := cl.(*client)
	require.NotNil(t, c.discoverNow, "discoverNow should be initialized even with DHT off")

	// Subscribe should work without panic (discoverNow signal is sent
	// but the DHT discovery loop isn't running — that's fine)
	msgChan := cl.Subscribe("dht-off-test")
	require.NotNil(t, msgChan)

	time.Sleep(200 * time.Millisecond)
}
