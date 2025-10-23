package p2p

import (
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to generate a valid peer ID for testing
func generateTestPeerID(t *testing.T) peer.ID {
	t.Helper()
	key, err := GeneratePrivateKey()
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(key)
	require.NoError(t, err)
	return id
}

func TestNewPeerTracker(t *testing.T) {
	tracker := newPeerTracker()

	require.NotNil(t, tracker)
	require.NotNil(t, tracker.names)
	require.NotNil(t, tracker.isRelaying)
	require.NotNil(t, tracker.topicPeers)
	require.NotNil(t, tracker.lastSeen)

	// Verify maps are empty
	assert.Empty(t, tracker.names)
	assert.Empty(t, tracker.isRelaying)
	assert.Empty(t, tracker.topicPeers)
	assert.Empty(t, tracker.lastSeen)
}

func TestPeerTrackerUpdateName(t *testing.T) {
	tests := []struct {
		name     string
		peerName string
	}{
		{
			name:     "update name for new peer",
			peerName: "alice",
		},
		{
			name:     "update name for another peer",
			peerName: "bob",
		},
		{
			name:     "update existing peer name",
			peerName: "alice-updated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newPeerTracker()
			peerID := generateTestPeerID(t)

			tracker.updateName(peerID, tt.peerName)

			// Verify name was stored
			storedName := tracker.getName(peerID)
			assert.Equal(t, tt.peerName, storedName)
		})
	}
}

func TestPeerTrackerGetName(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*peerTracker, *testing.T) peer.ID
		expectedName string
	}{
		{
			name: "get name for known peer",
			setupFunc: func(tracker *peerTracker, t *testing.T) peer.ID {
				peerID := generateTestPeerID(t)
				tracker.updateName(peerID, "charlie")
				return peerID
			},
			expectedName: "charlie",
		},
		{
			name: "get name for unknown peer returns unknown",
			setupFunc: func(_ *peerTracker, t *testing.T) peer.ID {
				peerID := generateTestPeerID(t)
				return peerID
			},
			expectedName: "unknown",
		},
		{
			name: "get name after multiple updates",
			setupFunc: func(tracker *peerTracker, t *testing.T) peer.ID {
				peerID := generateTestPeerID(t)
				tracker.updateName(peerID, "first")
				tracker.updateName(peerID, "second")
				tracker.updateName(peerID, "final")
				return peerID
			},
			expectedName: "final",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newPeerTracker()
			peerID := tt.setupFunc(tracker, t)

			name := tracker.getName(peerID)
			assert.Equal(t, tt.expectedName, name)
		})
	}
}

func TestPeerTrackerRecordMessageFrom(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "record message from new peer",
		},
		{
			name: "record message from different peer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newPeerTracker()
			peerID := generateTestPeerID(t)

			beforeTime := time.Now()
			tracker.recordMessageFrom(peerID)
			afterTime := time.Now()

			// Verify peer is in topicPeers
			assert.True(t, tracker.topicPeers[peerID])

			// Verify lastSeen is recent
			lastSeen, exists := tracker.lastSeen[peerID]
			require.True(t, exists)
			assert.True(t, lastSeen.After(beforeTime) || lastSeen.Equal(beforeTime))
			assert.True(t, lastSeen.Before(afterTime) || lastSeen.Equal(afterTime))
		})
	}
}

func TestPeerTrackerRecordMessageFromUpdatesTimestamp(t *testing.T) {
	tracker := newPeerTracker()
	peerID := generateTestPeerID(t)

	// Record first message
	tracker.recordMessageFrom(peerID)
	firstSeen := tracker.lastSeen[peerID]

	// Wait a bit
	time.Sleep(10 * time.Millisecond)

	// Record second message
	tracker.recordMessageFrom(peerID)
	secondSeen := tracker.lastSeen[peerID]

	// Second timestamp should be later
	assert.True(t, secondSeen.After(firstSeen))
}

func TestPeerTrackerGetAllTopicPeers(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(*peerTracker, *testing.T) []peer.ID
	}{
		{
			name: "empty tracker returns empty slice",
			setupFunc: func(_ *peerTracker, _ *testing.T) []peer.ID {
				return []peer.ID{}
			},
		},
		{
			name: "single peer",
			setupFunc: func(tracker *peerTracker, t *testing.T) []peer.ID {
				peerID := generateTestPeerID(t)
				tracker.recordMessageFrom(peerID)
				return []peer.ID{peerID}
			},
		},
		{
			name: "multiple peers",
			setupFunc: func(tracker *peerTracker, t *testing.T) []peer.ID {
				peer1 := generateTestPeerID(t)
				peer2 := generateTestPeerID(t)
				peer3 := generateTestPeerID(t)

				tracker.recordMessageFrom(peer1)
				tracker.recordMessageFrom(peer2)
				tracker.recordMessageFrom(peer3)

				return []peer.ID{peer1, peer2, peer3}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newPeerTracker()
			expectedPeers := tt.setupFunc(tracker, t)

			peers := tracker.getAllTopicPeers()

			assert.Len(t, peers, len(expectedPeers))

			// Verify all expected peers are present
			for _, expectedPeer := range expectedPeers {
				found := false
				for _, peer := range peers {
					if peer == expectedPeer {
						found = true
						break
					}
				}
				assert.True(t, found, "expected peer %s not found in result", expectedPeer)
			}
		})
	}
}

func TestPeerTrackerConcurrentUpdateName(t *testing.T) {
	tracker := newPeerTracker()
	peerID := generateTestPeerID(t)

	const goroutines = 100
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Launch multiple goroutines updating the same peer's name
	for i := 0; i < goroutines; i++ {
		go func(_ int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				tracker.updateName(peerID, "name")
			}
		}(i)
	}

	wg.Wait()

	// Should not panic and should have a valid name
	name := tracker.getName(peerID)
	assert.Equal(t, "name", name)
}

func TestPeerTrackerConcurrentGetName(t *testing.T) {
	tracker := newPeerTracker()
	peerID := generateTestPeerID(t)

	tracker.updateName(peerID, "testpeer")

	const goroutines = 100
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Launch multiple goroutines reading the same peer's name
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				name := tracker.getName(peerID)
				assert.NotEmpty(t, name)
			}
		}()
	}

	wg.Wait()
}

func TestPeerTrackerConcurrentRecordMessage(t *testing.T) {
	tracker := newPeerTracker()
	testPeerID := generateTestPeerID(t)

	const goroutines = 50
	const iterations = 10

	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Launch multiple goroutines recording messages from the same peer
	for i := 0; i < goroutines; i++ {
		go func(_ int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				tracker.recordMessageFrom(testPeerID)
			}
		}(i)
	}

	wg.Wait()

	// Should not panic and should have recorded peer
	peers := tracker.getAllTopicPeers()
	assert.NotEmpty(t, peers)
	assert.Len(t, peers, 1) // Should only have one unique peer
}

func TestPeerTrackerConcurrentMixedOperations(t *testing.T) {
	tracker := newPeerTracker()
	peerID1 := generateTestPeerID(t)
	peerID2 := generateTestPeerID(t)

	const goroutines = 30
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 4) // 4 types of operations

	// Concurrent updateName operations
	for i := 0; i < goroutines; i++ {
		go func(_ int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				tracker.updateName(peerID1, "peer1")
			}
		}(i)
	}

	// Concurrent getName operations
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = tracker.getName(peerID1)
			}
		}()
	}

	// Concurrent recordMessageFrom operations
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				tracker.recordMessageFrom(peerID2)
			}
		}()
	}

	// Concurrent getAllTopicPeers operations
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = tracker.getAllTopicPeers()
			}
		}()
	}

	wg.Wait()

	// Verify final state is consistent
	name := tracker.getName(peerID1)
	assert.Equal(t, "peer1", name)

	peers := tracker.getAllTopicPeers()
	assert.NotEmpty(t, peers)
}

func TestPeerTrackerGetAllTopicPeersDoesNotReturnDuplicates(t *testing.T) {
	tracker := newPeerTracker()
	peerID := generateTestPeerID(t)

	// Record message multiple times from same peer
	tracker.recordMessageFrom(peerID)
	tracker.recordMessageFrom(peerID)
	tracker.recordMessageFrom(peerID)

	peers := tracker.getAllTopicPeers()

	// Should only return one peer
	assert.Len(t, peers, 1)
	assert.Equal(t, peerID, peers[0])
}
