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
	require.NotNil(t, tracker.topicPeers)
	require.NotNil(t, tracker.lastSeen)
	require.NotNil(t, tracker.malformed)
	require.NotNil(t, tracker.skipMalformed)

	// Verify maps are empty
	assert.Empty(t, tracker.names)
	assert.Empty(t, tracker.topicPeers)
	assert.Empty(t, tracker.lastSeen)
	assert.Empty(t, tracker.malformed)
	assert.Empty(t, tracker.skipMalformed)
}

func TestPeerTrackerRecordMalformed(t *testing.T) {
	tracker := newPeerTracker()
	peerID := generateTestPeerID(t)

	assert.False(t, tracker.shouldSkipMalformed(peerID), "fresh peer must not be skipped")

	for i := 1; i < malformedThreshold; i++ {
		count, justSkipped := tracker.recordMalformed(peerID)
		assert.Equal(t, i, count)
		assert.False(t, justSkipped, "should not trip skip before threshold")
		assert.False(t, tracker.shouldSkipMalformed(peerID))
	}

	count, justSkipped := tracker.recordMalformed(peerID)
	assert.Equal(t, malformedThreshold, count)
	assert.True(t, justSkipped, "should trip skip at threshold")
	assert.True(t, tracker.shouldSkipMalformed(peerID))

	// Subsequent calls must not re-trigger justSkipped.
	count, justSkipped = tracker.recordMalformed(peerID)
	assert.Equal(t, malformedThreshold+1, count)
	assert.False(t, justSkipped, "skip transition should fire only once")
	assert.True(t, tracker.shouldSkipMalformed(peerID))
}

func TestPeerTrackerRecordMalformedIsolatesPeers(t *testing.T) {
	tracker := newPeerTracker()
	bad := generateTestPeerID(t)
	good := generateTestPeerID(t)

	for i := 0; i < malformedThreshold; i++ {
		tracker.recordMalformed(bad)
	}

	assert.True(t, tracker.shouldSkipMalformed(bad))
	assert.False(t, tracker.shouldSkipMalformed(good), "unrelated peer must not be skipped")
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

// assertPeersMatch verifies that all expected peers are present in the actual peer list
func assertPeersMatch(t *testing.T, expected, actual []peer.ID) {
	t.Helper()

	assert.Len(t, actual, len(expected))

	for _, expectedPeer := range expected {
		found := false
		for _, peer := range actual {
			if peer == expectedPeer {
				found = true
				break
			}
		}
		assert.True(t, found, "expected peer %s not found in result", expectedPeer)
	}
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
			assertPeersMatch(t, expectedPeers, peers)
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

// newBoundedTestTracker returns a tracker with a small cap, a fake clock and a
// connectedness oracle backed by the returned set.
func newBoundedTestTracker(maxPeers int, ttl time.Duration) (*peerTracker, *time.Time, map[peer.ID]bool) {
	tracker := newPeerTracker()
	clock := time.Unix(1_700_000_000, 0)
	connected := make(map[peer.ID]bool)
	tracker.maxPeers = maxPeers
	tracker.ttl = ttl
	tracker.now = func() time.Time { return clock }
	tracker.isConnected = func(id peer.ID) bool { return connected[id] }
	return tracker, &clock, connected
}

// requireForgotten asserts that no per-peer map still holds id.
func requireForgotten(t *testing.T, tracker *peerTracker, id peer.ID) {
	t.Helper()
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	require.NotContains(t, tracker.names, id)
	require.NotContains(t, tracker.topicPeers, id)
	require.NotContains(t, tracker.lastSeen, id)
	require.NotContains(t, tracker.malformed, id)
	require.NotContains(t, tracker.skipMalformed, id)
	require.NotContains(t, tracker.elems, id)
}

// requireTrackedAtMost asserts every per-peer map holds at most n peers.
func requireTrackedAtMost(t *testing.T, tracker *peerTracker, n int) {
	t.Helper()
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	require.LessOrEqual(t, len(tracker.names), n)
	require.LessOrEqual(t, len(tracker.topicPeers), n)
	require.LessOrEqual(t, len(tracker.lastSeen), n)
	require.LessOrEqual(t, len(tracker.malformed), n)
	require.LessOrEqual(t, len(tracker.skipMalformed), n)
	require.LessOrEqual(t, len(tracker.elems), n)
	require.LessOrEqual(t, tracker.order.Len(), n)
}

// A flood of messages from freshly minted author IDs must not grow the tracker
// past its cap, whether the messages are valid or malformed.
func TestPeerTrackerBoundedUnderAuthorFlood(t *testing.T) {
	const maxPeers = 8
	tracker, _, _ := newBoundedTestTracker(maxPeers, time.Hour)

	for i := 0; i < 10*maxPeers; i++ {
		id := generateTestPeerID(t)
		if i%2 == 0 {
			tracker.updateName(id, "sybil")
			tracker.recordMessageFrom(id)
		} else {
			tracker.recordMalformed(id)
		}
		requireTrackedAtMost(t, tracker, maxPeers)
	}
	require.Len(t, tracker.getAllTopicPeers(), maxPeers/2)
}

// At the cap the least recently active peer is evicted from every map.
func TestPeerTrackerCapEvictsLeastRecentlyActive(t *testing.T) {
	tracker, clock, _ := newBoundedTestTracker(3, time.Hour)
	a, b, c, d := generateTestPeerID(t), generateTestPeerID(t), generateTestPeerID(t), generateTestPeerID(t)

	for _, id := range []peer.ID{a, b, c} {
		tracker.updateName(id, "n")
		tracker.recordMessageFrom(id)
		*clock = clock.Add(time.Second)
	}
	tracker.recordMalformed(b)

	// a becomes the most recently active, leaving b as the oldest.
	*clock = clock.Add(time.Second)
	tracker.recordMessageFrom(a)
	*clock = clock.Add(time.Second)
	tracker.recordMessageFrom(c)

	*clock = clock.Add(time.Second)
	tracker.recordMessageFrom(d)

	requireForgotten(t, tracker, b)
	assertPeersMatch(t, []peer.ID{a, c, d}, tracker.getAllTopicPeers())
	require.Equal(t, "n", tracker.getName(a))
	require.Equal(t, "unknown", tracker.getName(b))
}

// A peer silent for longer than the TTL is forgotten on the next insert.
func TestPeerTrackerExpiresIdlePeers(t *testing.T) {
	tracker, clock, _ := newBoundedTestTracker(100, time.Minute)
	idle, fresh := generateTestPeerID(t), generateTestPeerID(t)

	tracker.updateName(idle, "idle")
	tracker.recordMessageFrom(idle)

	*clock = clock.Add(time.Minute)
	tracker.recordMessageFrom(fresh)

	requireForgotten(t, tracker, idle)
	assertPeersMatch(t, []peer.ID{fresh}, tracker.getAllTopicPeers())
}

// Expired peers are not reported even if no insert has evicted them yet.
func TestPeerTrackerGetAllTopicPeersSkipsExpired(t *testing.T) {
	tracker, clock, _ := newBoundedTestTracker(100, time.Minute)
	idle := generateTestPeerID(t)
	tracker.recordMessageFrom(idle)

	*clock = clock.Add(time.Minute)
	require.Empty(t, tracker.getAllTopicPeers())
}

// Connected peers are never evicted, neither by the TTL nor by the cap: callers
// rely on GetPeers listing every connected author.
func TestPeerTrackerKeepsConnectedPeers(t *testing.T) {
	tracker, clock, connected := newBoundedTestTracker(2, time.Minute)
	neighbour := generateTestPeerID(t)
	connected[neighbour] = true

	tracker.updateName(neighbour, "neighbour")
	tracker.recordMessageFrom(neighbour)

	// Long past the TTL, and pushed against the cap by a flood.
	*clock = clock.Add(time.Hour)
	for i := 0; i < 10; i++ {
		tracker.recordMessageFrom(generateTestPeerID(t))
		requireTrackedAtMost(t, tracker, 2)
	}

	require.Contains(t, tracker.getAllTopicPeers(), neighbour)
	require.Equal(t, "neighbour", tracker.getName(neighbour))
}

// When every tracked peer is connected the cap yields rather than evicting a
// connected peer, and the eviction loop still terminates.
func TestPeerTrackerAllConnectedOverCap(t *testing.T) {
	tracker, _, connected := newBoundedTestTracker(2, time.Minute)
	ids := []peer.ID{generateTestPeerID(t), generateTestPeerID(t), generateTestPeerID(t)}
	for _, id := range ids {
		connected[id] = true
		tracker.recordMessageFrom(id)
	}
	assertPeersMatch(t, ids, tracker.getAllTopicPeers())
}

// A new peer arriving when every other tracked peer is connected must stay
// fully tracked, so its entries can still be evicted later.
func TestPeerTrackerNewPeerSurvivesConnectedCap(t *testing.T) {
	tracker, clock, connected := newBoundedTestTracker(2, time.Minute)
	c1, c2 := generateTestPeerID(t), generateTestPeerID(t)
	for _, id := range []peer.ID{c1, c2} {
		connected[id] = true
		tracker.recordMessageFrom(id)
	}

	newcomer := generateTestPeerID(t)
	tracker.updateName(newcomer, "newcomer")
	tracker.recordMessageFrom(newcomer)
	require.Contains(t, tracker.elems, newcomer, "newcomer must be in the eviction order")

	// Once a connected peer leaves, the newcomer is evictable again.
	delete(connected, c1)
	*clock = clock.Add(time.Minute)
	tracker.recordMessageFrom(c2)
	requireForgotten(t, tracker, newcomer)
	requireForgotten(t, tracker, c1)
}

// recordMessageFrom reports the first valid message of a peer, including
// after the peer was evicted and comes back.
func TestPeerTrackerRecordMessageFromReportsFirstAuthored(t *testing.T) {
	tracker, clock, _ := newBoundedTestTracker(100, time.Minute)
	id := generateTestPeerID(t)

	tracker.recordMalformed(id)
	require.True(t, tracker.recordMessageFrom(id), "malformed messages do not make an author")
	require.False(t, tracker.recordMessageFrom(id))

	*clock = clock.Add(time.Minute)
	tracker.recordMessageFrom(generateTestPeerID(t)) // evicts id
	requireForgotten(t, tracker, id)
	require.True(t, tracker.recordMessageFrom(id))
}
