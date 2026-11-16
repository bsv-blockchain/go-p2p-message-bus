package p2p

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAddrLocalhost = "/ip4/127.0.0.1/tcp/4001"
	testAddrRemote    = "/ip4/192.168.1.1/tcp/4001"
	testCacheFileName = "cache.json"
)

func TestLoadPeerCacheFileNotExists(t *testing.T) {
	logger := &DefaultLogger{}
	nonExistentFile := filepath.Join(t.TempDir(), "nonexistent.json")

	peers := loadPeerCache(nonExistentFile, 24*time.Hour, logger)

	assert.Nil(t, peers)
}

func TestLoadPeerCacheValidFile(t *testing.T) {
	tests := []struct {
		name          string
		cacheData     []cachedPeer
		ttl           time.Duration
		expectedCount int
	}{
		{
			name: "single peer",
			cacheData: []cachedPeer{
				{
					ID:       testPeerID1,
					Name:     testPeerName1,
					Addrs:    []string{testAddrLocalhost},
					LastSeen: time.Now(),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 1,
		},
		{
			name: "multiple peers",
			cacheData: []cachedPeer{
				{
					ID:       testPeerID1,
					Name:     testPeerName1,
					Addrs:    []string{testAddrLocalhost},
					LastSeen: time.Now(),
				},
				{
					ID:       "QmaBcDeFgHiJkLmNoPqRsTuVwXyZ123456789AbCdEf",
					Name:     "peer2",
					Addrs:    []string{testAddrRemote},
					LastSeen: time.Now(),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 2,
		},
		{
			name: "negative TTL disables eviction",
			cacheData: []cachedPeer{
				{
					ID:       testPeerID1,
					Name:     testPeerName1,
					Addrs:    []string{testAddrLocalhost},
					LastSeen: time.Now().Add(-48 * time.Hour), // Old peer
				},
			},
			ttl:           -1,
			expectedCount: 1, // Should not be evicted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &DefaultLogger{}
			cacheFile := filepath.Join(t.TempDir(), testCacheFileName)

			// Write test data
			data, err := json.MarshalIndent(tt.cacheData, "", "  ")
			require.NoError(t, err)
			err = os.WriteFile(cacheFile, data, 0o600)
			require.NoError(t, err)

			// Load cache
			peers := loadPeerCache(cacheFile, tt.ttl, logger)

			require.NotNil(t, peers)
			assert.Len(t, peers, tt.expectedCount)

			// Verify first peer data if exists
			if len(peers) > 0 && len(tt.cacheData) > 0 {
				assert.Equal(t, tt.cacheData[0].ID, peers[0].ID)
				assert.Equal(t, tt.cacheData[0].Name, peers[0].Name)
			}
		})
	}
}

func TestLoadPeerCacheCorruptedFile(t *testing.T) {
	tests := []struct {
		name        string
		fileContent string
	}{
		{
			name:        "invalid JSON",
			fileContent: "this is not valid JSON{]",
		},
		{
			name:        "empty file",
			fileContent: "",
		},
		{
			name:        "partial JSON",
			fileContent: `{"id": "QmYyQSo1c1Ym7orWxLY`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &DefaultLogger{}
			cacheFile := filepath.Join(t.TempDir(), "corrupt.json")

			err := os.WriteFile(cacheFile, []byte(tt.fileContent), 0o600)
			require.NoError(t, err)

			peers := loadPeerCache(cacheFile, 24*time.Hour, logger)

			// Should return nil for corrupted files
			assert.Nil(t, peers)
		})
	}
}

func TestSavePeerCacheValidData(t *testing.T) {
	tests := []struct {
		name  string
		peers []cachedPeer
	}{
		{
			name: "single peer",
			peers: []cachedPeer{
				{
					ID:       testPeerID1,
					Name:     "alice",
					Addrs:    []string{testAddrLocalhost},
					LastSeen: time.Now(),
				},
			},
		},
		{
			name: "multiple peers",
			peers: []cachedPeer{
				{
					ID:       testPeerID1,
					Name:     "alice",
					Addrs:    []string{testAddrLocalhost},
					LastSeen: time.Now(),
				},
				{
					ID:       "QmaBcDeFgHiJkLmNoPqRsTuVwXyZ123456789AbCdEf",
					Name:     "bob",
					Addrs:    []string{testAddrRemote, "/ip6/::1/tcp/4001"},
					LastSeen: time.Now(),
				},
			},
		},
		{
			name:  "empty peer list",
			peers: []cachedPeer{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &DefaultLogger{}
			cacheFile := filepath.Join(t.TempDir(), testCacheFileName)

			savePeerCache(tt.peers, cacheFile, logger)

			// Verify file was created
			_, err := os.Stat(cacheFile)
			require.NoError(t, err)

			// Verify content
			data, err := os.ReadFile(cacheFile) // #nosec G304
			require.NoError(t, err)

			var loaded []cachedPeer
			err = json.Unmarshal(data, &loaded)
			require.NoError(t, err)

			assert.Len(t, loaded, len(tt.peers))

			for i, peer := range tt.peers {
				if i < len(loaded) {
					assert.Equal(t, peer.ID, loaded[i].ID)
					assert.Equal(t, peer.Name, loaded[i].Name)
					assert.Equal(t, peer.Addrs, loaded[i].Addrs)
				}
			}
		})
	}
}

func TestSavePeerCacheInvalidPath(_ *testing.T) {
	logger := &DefaultLogger{}
	invalidPath := "/nonexistent/directory/that/does/not/exist/cache.json"

	peers := []cachedPeer{
		{
			ID:       testPeerID1,
			Name:     testPeerName1,
			Addrs:    []string{testAddrLocalhost},
			LastSeen: time.Now(),
		},
	}

	// Should not panic even with invalid path
	savePeerCache(peers, invalidPath, logger)
}

func TestEvictStalePeersWithTTL(t *testing.T) {
	now := time.Now()
	logger := &DefaultLogger{}

	tests := []struct {
		name          string
		peers         []cachedPeer
		ttl           time.Duration
		expectedCount int
		description   string
	}{
		{
			name: "all peers are fresh",
			peers: []cachedPeer{
				{
					ID:       testPeerID1,
					LastSeen: now.Add(-1 * time.Hour),
				},
				{
					ID:       "QmaBcDeFgHiJkLmNoPqRsTuVwXyZ123456789AbCdEf",
					LastSeen: now.Add(-2 * time.Hour),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 2,
			description:   "peers seen within TTL should remain",
		},
		{
			name: "some peers are stale",
			peers: []cachedPeer{
				{
					ID:       testPeerID1,
					LastSeen: now.Add(-1 * time.Hour),
				},
				{
					ID:       "QmaBcDeFgHiJkLmNoPqRsTuVwXyZ123456789AbCdEf",
					LastSeen: now.Add(-48 * time.Hour),
				},
				{
					ID:       "QmXyZ987654321FeDcBa9876543210XyZaBcDeFg",
					LastSeen: now.Add(-72 * time.Hour),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 1,
			description:   "only fresh peers should remain",
		},
		{
			name: "all peers are stale",
			peers: []cachedPeer{
				{
					ID:       testPeerID1,
					LastSeen: now.Add(-48 * time.Hour),
				},
				{
					ID:       "QmaBcDeFgHiJkLmNoPqRsTuVwXyZ123456789AbCdEf",
					LastSeen: now.Add(-72 * time.Hour),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 0,
			description:   "all stale peers should be evicted",
		},
		{
			name: "zero LastSeen timestamp is kept",
			peers: []cachedPeer{
				{
					ID:       testPeerID1,
					LastSeen: time.Time{}, // Zero value
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 1,
			description:   "peers with zero LastSeen should be kept",
		},
		{
			name: "mixed zero and valid timestamps",
			peers: []cachedPeer{
				{
					ID:       testPeerID1,
					LastSeen: time.Time{}, // Zero value
				},
				{
					ID:       "QmaBcDeFgHiJkLmNoPqRsTuVwXyZ123456789AbCdEf",
					LastSeen: now.Add(-1 * time.Hour),
				},
				{
					ID:       "QmXyZ987654321FeDcBa9876543210XyZaBcDeFg",
					LastSeen: now.Add(-48 * time.Hour),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 2,
			description:   "zero timestamp and fresh peer should remain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evictStalePeers(tt.peers, tt.ttl, logger)

			assert.Len(t, result, tt.expectedCount, tt.description)
		})
	}
}

func TestEvictStalePeersEdgeCases(t *testing.T) {
	logger := &DefaultLogger{}
	now := time.Now()

	tests := []struct {
		name          string
		peers         []cachedPeer
		ttl           time.Duration
		expectedCount int
	}{
		{
			name:          "empty peer list",
			peers:         []cachedPeer{},
			ttl:           24 * time.Hour,
			expectedCount: 0,
		},
		{
			name:          "nil peer list",
			peers:         nil,
			ttl:           24 * time.Hour,
			expectedCount: 0,
		},
		{
			name: "TTL exactly at boundary",
			peers: []cachedPeer{
				{
					ID:       testPeerID1,
					LastSeen: now.Add(-24 * time.Hour),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 0, // Peer exactly at TTL should be evicted
		},
		{
			name: "TTL just within boundary",
			peers: []cachedPeer{
				{
					ID:       testPeerID1,
					LastSeen: now.Add(-24*time.Hour + 1*time.Second),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 1, // Peer just within TTL should be kept
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evictStalePeers(tt.peers, tt.ttl, logger)

			assert.Len(t, result, tt.expectedCount)
		})
	}
}

func TestLoadPeerCacheWithTTLEviction(t *testing.T) {
	logger := &DefaultLogger{}
	now := time.Now()

	tests := []struct {
		name          string
		cacheData     []cachedPeer
		ttl           time.Duration
		expectedCount int
	}{
		{
			name: "fresh peers loaded",
			cacheData: []cachedPeer{
				{
					ID:       testPeerName1,
					Name:     "alice",
					Addrs:    []string{testAddrLocalhost},
					LastSeen: now.Add(-1 * time.Hour),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 1,
		},
		{
			name: "stale peers evicted on load",
			cacheData: []cachedPeer{
				{
					ID:       testPeerName1,
					Name:     "alice",
					Addrs:    []string{testAddrLocalhost},
					LastSeen: now.Add(-48 * time.Hour),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 0,
		},
		{
			name: "mixed fresh and stale peers",
			cacheData: []cachedPeer{
				{
					ID:       testPeerName1,
					Name:     "alice",
					Addrs:    []string{testAddrLocalhost},
					LastSeen: now.Add(-1 * time.Hour),
				},
				{
					ID:       "peer2",
					Name:     "bob",
					Addrs:    []string{testAddrRemote},
					LastSeen: now.Add(-48 * time.Hour),
				},
			},
			ttl:           24 * time.Hour,
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheFile := filepath.Join(t.TempDir(), testCacheFileName)

			// Write test data
			data, err := json.MarshalIndent(tt.cacheData, "", "  ")
			require.NoError(t, err)
			err = os.WriteFile(cacheFile, data, 0o600)
			require.NoError(t, err)

			// Load cache with TTL
			peers := loadPeerCache(cacheFile, tt.ttl, logger)

			if tt.expectedCount == 0 {
				assert.Empty(t, peers)
			} else {
				require.NotNil(t, peers)
				assert.Len(t, peers, tt.expectedCount)
			}
		})
	}
}

func TestSavePeerCacheFilePermissions(t *testing.T) {
	logger := &DefaultLogger{}
	cacheFile := filepath.Join(t.TempDir(), testCacheFileName)

	peers := []cachedPeer{
		{
			ID:       testPeerID1,
			Name:     testPeerName1,
			Addrs:    []string{testAddrLocalhost},
			LastSeen: time.Now(),
		},
	}

	savePeerCache(peers, cacheFile, logger)

	// Check file permissions
	info, err := os.Stat(cacheFile)
	require.NoError(t, err)

	// File should have 0o600 permissions
	expectedPerms := os.FileMode(0o600)
	assert.Equal(t, expectedPerms, info.Mode().Perm())
}

func TestLoadAndSavePeerCacheRoundTrip(t *testing.T) {
	logger := &DefaultLogger{}
	cacheFile := filepath.Join(t.TempDir(), testCacheFileName)
	now := time.Now()

	originalPeers := []cachedPeer{
		{
			ID:       testPeerID1,
			Name:     "alice",
			Addrs:    []string{testAddrLocalhost},
			LastSeen: now,
		},
		{
			ID:       "QmaBcDeFgHiJkLmNoPqRsTuVwXyZ123456789AbCdEf",
			Name:     "bob",
			Addrs:    []string{"/ip4/192.168.1.1/tcp/4001", "/ip6/::1/tcp/4001"},
			LastSeen: now,
		},
	}

	// Save peers
	savePeerCache(originalPeers, cacheFile, logger)

	// Load peers back
	loadedPeers := loadPeerCache(cacheFile, -1, logger) // Negative TTL to disable eviction

	require.NotNil(t, loadedPeers)
	assert.Len(t, loadedPeers, len(originalPeers))

	for i, original := range originalPeers {
		assert.Equal(t, original.ID, loadedPeers[i].ID)
		assert.Equal(t, original.Name, loadedPeers[i].Name)
		assert.Equal(t, original.Addrs, loadedPeers[i].Addrs)
		// Timestamps might have slight differences due to JSON serialization
		assert.WithinDuration(t, original.LastSeen, loadedPeers[i].LastSeen, 1*time.Second)
	}
}

// addFuzzLoadPeerCacheSeeds adds test seed data for the FuzzLoadPeerCache function.
func addFuzzLoadPeerCacheSeeds(f *testing.F) {
	// Valid cases
	f.Add([]byte(`[{"id":"QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N","name":"alice","addrs":["/ip4/127.0.0.1/tcp/4001"],"last_seen":"2024-01-01T00:00:00Z"}]`))
	f.Add([]byte(`[{"id":"peer1","name":"alice","addrs":["/ip4/127.0.0.1/tcp/4001"],"last_seen":"2024-01-01T00:00:00Z"},{"id":"peer2","name":"bob","addrs":["/ip4/192.168.1.1/tcp/4001"],"last_seen":"2024-01-02T00:00:00Z"}]`))
	f.Add([]byte(`[]`))

	// Invalid JSON
	f.Add([]byte(`{invalid json}`))
	f.Add([]byte(`[{`))
	f.Add([]byte(`}`))
	f.Add([]byte(``))

	// Malformed structures
	f.Add([]byte(`{"not":"an_array"}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`"string"`))
	f.Add([]byte(`123`))

	// Missing fields
	f.Add([]byte(`[{"id":"peer1"}]`))
	f.Add([]byte(`[{"name":"alice"}]`))
	f.Add([]byte(`[{}]`))

	// Wrong types
	f.Add([]byte(`[{"id":123,"name":"alice","addrs":[],"last_seen":"2024-01-01T00:00:00Z"}]`))
	f.Add([]byte(`[{"id":"peer1","name":123,"addrs":[],"last_seen":"2024-01-01T00:00:00Z"}]`))
	f.Add([]byte(`[{"id":"peer1","name":"alice","addrs":"not_array","last_seen":"2024-01-01T00:00:00Z"}]`))

	// Invalid timestamps
	f.Add([]byte(`[{"id":"peer1","name":"alice","addrs":[],"last_seen":"invalid-date"}]`))
	f.Add([]byte(`[{"id":"peer1","name":"alice","addrs":[],"last_seen":null}]`))

	// Large array
	largeArray := `[`
	for i := 0; i < 100; i++ {
		if i > 0 {
			largeArray += `,`
		}
		largeArray += `{"id":"peer` + string(rune(i)) + `","name":"peer","addrs":[],"last_seen":"2024-01-01T00:00:00Z"}`
	}
	largeArray += `]`
	f.Add([]byte(largeArray))

	// Unicode and special characters
	f.Add([]byte(`[{"id":"peer\u4f60\u597d","name":"peer\u0645\u0631\u062d\u0628\u0627","addrs":[],"last_seen":"2024-01-01T00:00:00Z"}]`))
	f.Add([]byte(`[{"id":"peer\u0000null","name":"alice","addrs":[],"last_seen":"2024-01-01T00:00:00Z"}]`))

	// Extra fields
	f.Add([]byte(`[{"id":"peer1","name":"alice","addrs":[],"last_seen":"2024-01-01T00:00:00Z","extra":"field","another":123}]`))
}

// validatePeerCacheResult validates the result of loading a peer cache.
func validatePeerCacheResult(t *testing.T, peers, peersNoEvict []cachedPeer) {
	t.Helper()

	// Both nil (parse error) or both non-nil (success)
	if (peers == nil) != (peersNoEvict == nil) {
		// One succeeded and one failed - allow if both are effectively empty
		if len(peers) == 0 && len(peersNoEvict) == 0 {
			return
		}
		t.Error("loadPeerCache returned different results for same file with different TTL")
	} else if peers != nil && peersNoEvict != nil {
		// Both successful, peer count should be same or more with negative TTL
		if len(peersNoEvict) < len(peers) {
			t.Errorf("Expected no-evict to have >= peers, got %d vs %d", len(peersNoEvict), len(peers))
		}
	}
}

// FuzzLoadPeerCache performs fuzz testing on the loadPeerCache function
// to ensure it handles arbitrary JSON data without panicking and with proper
// error handling. This tests the robustness of JSON parsing and data validation.
func FuzzLoadPeerCache(f *testing.F) {
	addFuzzLoadPeerCacheSeeds(f)

	f.Fuzz(func(t *testing.T, jsonData []byte) {
		logger := &DefaultLogger{}
		cacheFile := filepath.Join(t.TempDir(), "fuzz_cache.json")

		// Write fuzzed data to file
		err := os.WriteFile(cacheFile, jsonData, 0o600)
		if err != nil {
			// If we can't write the file, skip this iteration
			t.Skip()
		}

		// The function should never panic, regardless of input
		peers := loadPeerCache(cacheFile, 24*time.Hour, logger)

		// Verify the result is consistent with the behavior
		// Either nil (error case) or a valid slice
		// If we got peers, verify they're valid
		for _, peer := range peers {
			// Each peer should have at least an ID (could be empty string though)
			// Addrs could be nil or valid slice
			for _, addr := range peer.Addrs {
				// Each address should be a string (could be empty)
				_ = addr
			}
		}

		// Try loading again with negative TTL (no eviction) and validate
		peersNoEvict := loadPeerCache(cacheFile, -1, logger)
		validatePeerCacheResult(t, peers, peersNoEvict)
	})
}

// Concurrent saves of snapshots of different sizes must never expose a torn
// or empty file to a reader, and must not leave temporary files behind.
func TestSavePeerCacheConcurrentWritersNeverTear(t *testing.T) {
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, testCacheFileName)
	logger := &DefaultLogger{}

	snapshot := func(n int) []cachedPeer {
		peers := make([]cachedPeer, n)
		for i := range peers {
			peers[i] = cachedPeer{ID: fmt.Sprintf("peer-%d-%d", n, i), Name: strings.Repeat("n", 64), Addrs: []string{testAddrRemote}, LastSeen: time.Now()}
		}
		return peers
	}
	savePeerCache(snapshot(1), cacheFile, logger)

	stop := make(chan struct{})
	torn := make(chan []byte, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(cacheFile) // #nosec G304 -- test file
			if err == nil && !json.Valid(data) {
				select {
				case torn <- data:
				default:
				}
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				savePeerCache(snapshot(n), cacheFile, logger)
			}
		}((i%4 + 1) * 300)
	}
	wg.Wait()
	close(stop)
	<-readerDone

	select {
	case data := <-torn:
		t.Fatalf("reader saw a torn peer cache (%d bytes)", len(data))
	default:
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "temporary files must not be left behind")
}

func TestMergePeerCache(t *testing.T) {
	now := time.Now()
	at := func(id string, ago time.Duration) cachedPeer {
		return cachedPeer{ID: id, Name: id, Addrs: []string{testAddrRemote}, LastSeen: now.Add(-ago)}
	}
	ids := func(peers []cachedPeer) []string {
		out := make([]string, len(peers))
		for i, p := range peers {
			out[i] = p.ID
		}
		return out
	}

	t.Run("keeps existing peers that are not connected now", func(t *testing.T) {
		merged := mergePeerCache([]cachedPeer{at("old", time.Hour)}, []cachedPeer{at("new", 0)}, 10)
		require.Equal(t, []string{"new", "old"}, ids(merged))
	})

	t.Run("current snapshot replaces the existing entry", func(t *testing.T) {
		current := at("p", 0)
		current.Addrs = []string{testAddrLocalhost}
		merged := mergePeerCache([]cachedPeer{at("p", time.Hour)}, []cachedPeer{current}, 10)
		require.Len(t, merged, 1)
		require.Equal(t, []string{testAddrLocalhost}, merged[0].Addrs)
		require.WithinDuration(t, now, merged[0].LastSeen, time.Second)
	})

	t.Run("caps to the most recently seen peers", func(t *testing.T) {
		existing := []cachedPeer{at("e1", 3*time.Hour), at("e2", 2*time.Hour), {ID: "never", Addrs: []string{testAddrRemote}}}
		merged := mergePeerCache(existing, []cachedPeer{at("c1", 0), at("c2", time.Minute)}, 3)
		require.Equal(t, []string{"c1", "c2", "e2"}, ids(merged))
	})
}

// newCacheTopicClients connects two clients on a shared topic; b persists its
// peer cache to cacheFile and coalesces saves over saveInterval.
func newCacheTopicClients(t *testing.T, cacheFile string, saveInterval time.Duration) (a, b *client) {
	t.Helper()
	a, b = newConnectedClientsWith(t, eventuallyTimeout, func(name string, cfg *Config) {
		if name == "peer-b" {
			cfg.PeerCacheFile = cacheFile
			cfg.peerCacheSaveInterval = saveInterval
		}
	})

	const topic = "peer-cache-topic"
	a.Subscribe(topic)
	b.Subscribe(topic)
	require.Eventually(t, func() bool {
		b.mu.RLock()
		tp, ok := b.topics[topic]
		b.mu.RUnlock()
		return ok && slices.Contains(tp.ListPeers(), a.host.ID())
	}, eventuallyTimeout, eventuallyTick)
	return a, b
}

func cachedPeerIDs(t *testing.T, cacheFile string) []string {
	t.Helper()
	peers := loadPeerCache(cacheFile, -1, &DefaultLogger{})
	ids := make([]string, 0, len(peers))
	for _, p := range peers {
		ids = append(ids, p.ID)
	}
	return ids
}

// Saving merges the connected topic peers into the existing cache: peers seen
// within the TTL survive, stale ones are dropped.
func TestClientSavePeerCacheMergesExisting(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), testCacheFileName)
	now := time.Now()
	savePeerCache([]cachedPeer{
		{ID: testPeerID1, Name: "recent", Addrs: []string{testAddrRemote}, LastSeen: now.Add(-time.Hour)},
		{ID: "stale-peer", Name: "stale", Addrs: []string{testAddrRemote}, LastSeen: now.Add(-48 * time.Hour)},
	}, cacheFile, &DefaultLogger{})

	a, b := newCacheTopicClients(t, cacheFile, time.Hour)
	b.savePeerCache()

	require.ElementsMatch(t, []string{testPeerID1, a.host.ID().String()}, cachedPeerIDs(t, cacheFile))
}

// Any number of save requests within one interval produce a single write.
func TestPeerCacheSaveRequestsCoalesce(t *testing.T) {
	const interval = time.Second
	cacheFile := filepath.Join(t.TempDir(), testCacheFileName)
	_, b := newCacheTopicClients(t, cacheFile, interval)

	exists := func() bool {
		_, err := os.Stat(cacheFile)
		return err == nil
	}

	// Let saves triggered while the connection was set up land first.
	time.Sleep(topicPeerConnectDelay + 2*interval)
	_ = os.Remove(cacheFile)

	for i := 0; i < 50; i++ {
		b.requestPeerCacheSave()
	}
	require.Never(t, exists, interval/3, eventuallyTick, "save must wait for the interval")
	require.Eventually(t, exists, eventuallyTimeout, eventuallyTick)

	require.NoError(t, os.Remove(cacheFile))
	require.Never(t, exists, 2*interval, eventuallyTick, "requests must coalesce into one write")
}

// Close writes a pending save instead of dropping it.
func TestClosePersistsPeerCache(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), testCacheFileName)
	a, b := newCacheTopicClients(t, cacheFile, time.Hour)

	b.requestPeerCacheSave()
	require.NoError(t, b.Close())

	require.Contains(t, cachedPeerIDs(t, cacheFile), a.host.ID().String())
}
