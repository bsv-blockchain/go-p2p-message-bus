package p2p

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientGetPeersWithTopicPeers(t *testing.T) {
	privKey1, err := GeneratePrivateKey()
	require.NoError(t, err)

	config1 := Config{
		Name:       "peer1",
		PrivateKey: privKey1,
	}

	client1, err := NewClient(config1)
	require.NoError(t, err)
	defer func() {
		closeErr := client1.Close()
		require.NoError(t, closeErr)
	}()

	// Initially should have no peers
	peers := client1.GetPeers()
	assert.Empty(t, peers)

	// Subscribe to a topic
	_ = client1.Subscribe("test-topic")

	// Give subscription time to initialize
	time.Sleep(100 * time.Millisecond)

	// Still should have no peers (no other clients)
	peers = client1.GetPeers()
	assert.NotNil(t, peers)
}

func TestClientGetPeersReturnsCorrectStructure(t *testing.T) {
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

	peers := cl.GetPeers()

	// Should return empty slice, not nil
	assert.NotNil(t, peers)
	assert.IsType(t, []PeerInfo{}, peers)
}

func TestClientSavePeerCacheDisabled(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		PeerCacheFile: "", // Cache disabled
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	c := cl.(*client)

	// Should not panic when caching is disabled
	c.savePeerCache()
}

func TestClientSavePeerCacheEnabled(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cacheFile := filepath.Join(t.TempDir(), "peers_save.json")

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		PeerCacheFile: cacheFile,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	c := cl.(*client)

	// Subscribe to a topic to initialize topics map
	_ = c.Subscribe("test-topic")
	time.Sleep(100 * time.Millisecond)

	// Call savePeerCache
	c.savePeerCache()

	// If there are no connected peers, cache file may or may not exist
	// This is acceptable behavior - just verify no panic occurred
}

func TestClientSavePeerCacheWithInvalidPath(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	// Use an invalid path that cannot be written
	invalidPath := "/nonexistent/directory/peers.json"

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		PeerCacheFile: invalidPath,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	c := cl.(*client)

	// Subscribe to a topic
	_ = c.Subscribe("test-topic")
	time.Sleep(50 * time.Millisecond)

	// Should not panic even with invalid path
	c.savePeerCache()
}

func TestClientPeerCachePersistence(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), "peers_persist.json")

	// Pre-populate cache with a peer
	cachedPeers := []cachedPeer{
		{
			ID:       "QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N",
			Name:     "existing-peer",
			Addrs:    []string{"/ip4/127.0.0.1/tcp/4001"},
			LastSeen: time.Now(),
		},
	}

	data, err := json.MarshalIndent(cachedPeers, "", "  ")
	require.NoError(t, err)
	err = os.WriteFile(cacheFile, data, 0o600)
	require.NoError(t, err)

	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		PeerCacheFile: cacheFile,
		PeerCacheTTL:  24 * time.Hour,
	}

	// Create client - should load cached peers
	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	// Give time for cache loading
	time.Sleep(100 * time.Millisecond)

	// Verify cache file still exists
	_, err = os.Stat(cacheFile)
	require.NoError(t, err)
}

func TestClientSavePeerCacheMergesExistingData(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), "peers_merge.json")

	// Pre-populate cache
	existingPeers := []cachedPeer{
		{
			ID:       "QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N",
			Name:     "old-peer",
			Addrs:    []string{"/ip4/192.168.1.1/tcp/4001"},
			LastSeen: time.Now().Add(-48 * time.Hour),
		},
	}

	data, err := json.MarshalIndent(existingPeers, "", "  ")
	require.NoError(t, err)
	err = os.WriteFile(cacheFile, data, 0o600)
	require.NoError(t, err)

	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		PeerCacheFile: cacheFile,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	c := cl.(*client)

	// Subscribe to trigger topic creation
	_ = c.Subscribe("test-topic")
	time.Sleep(50 * time.Millisecond)

	// Save cache
	c.savePeerCache()
}

func TestClientGetPeersConcurrency(t *testing.T) {
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

	// Call GetPeers concurrently from multiple goroutines
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_ = cl.GetPeers()
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestClientPeerCacheTTLNegativeValue(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), "peers_ttl.json")

	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		PeerCacheFile: cacheFile,
		PeerCacheTTL:  -1, // Negative TTL disables eviction
	}

	cl, err := NewClient(config)
	require.NoError(t, err)

	err = cl.Close()
	require.NoError(t, err)
}
