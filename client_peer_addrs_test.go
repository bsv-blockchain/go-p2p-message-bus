package p2p

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A connected peer that never authored a message is absent from GetPeers but
// PeerAddrs still reports its connection.
func TestPeerAddrsConnectedNonAuthor(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)

	assert.Empty(t, a.GetPeers())
	addrs := a.PeerAddrs(b.host.ID().String())
	require.NotEmpty(t, addrs)
	assert.ElementsMatch(t, remoteAddrs(a.host.Network().ConnsToPeer(b.host.ID())), addrs)
}

// For a tracked author, PeerAddrs reports the same strings as GetPeers.
func TestPeerAddrsMatchesGetPeers(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	joinCacheTopic(t, a, b)
	makeAuthor(t, a, b)

	var fromGetPeers []string
	found := false
	for _, p := range b.GetPeers() {
		if p.ID == a.host.ID().String() {
			fromGetPeers, found = p.Addrs, true
		}
	}
	require.True(t, found, "a must be listed by b.GetPeers")
	require.NotEmpty(t, fromGetPeers)
	assert.ElementsMatch(t, fromGetPeers, b.PeerAddrs(a.host.ID().String()))
}

func TestPeerAddrsNil(t *testing.T) {
	a, _ := newConnectedClients(t, eventuallyTimeout)

	for name, id := range map[string]string{
		"undecodable":   "not-a-peer-id",
		"empty":         "",
		"unknown valid": newTestPeerID(t).String(),
		"self":          a.host.ID().String(),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, a.PeerAddrs(id))
		})
	}
}

func TestPeerAddrsStopsAfterDisconnect(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	bID := b.host.ID()
	require.NotEmpty(t, a.PeerAddrs(bID.String()))

	require.NoError(t, b.Close())
	require.Eventually(t, func() bool {
		return a.PeerAddrs(bID.String()) == nil
	}, eventuallyTimeout, eventuallyTick)
}

// PeerAddrs must neither read nor grow the topic-author tracker: it answers
// while the tracker lock is held for writing, and leaves the tracker as it was.
func TestPeerAddrsDoesNotTouchTracker(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	bID := b.host.ID().String()
	unknown := newTestPeerID(t).String()

	pt := a.peerTracker
	pt.mu.RLock()
	before := [...]int{len(pt.names), len(pt.topicPeers), len(pt.lastSeen), len(pt.elems)}
	pt.mu.RUnlock()

	pt.mu.Lock()
	done := make(chan []string, 1)
	go func() {
		_ = a.PeerAddrs(unknown)
		done <- a.PeerAddrs(bID)
	}()
	select {
	case addrs := <-done:
		pt.mu.Unlock()
		assert.NotEmpty(t, addrs)
	case <-time.After(eventuallyTimeout):
		pt.mu.Unlock()
		<-done
		t.Fatal("PeerAddrs blocked on the peer tracker lock")
	}

	pt.mu.RLock()
	after := [...]int{len(pt.names), len(pt.topicPeers), len(pt.lastSeen), len(pt.elems)}
	pt.mu.RUnlock()
	assert.Equal(t, before, after)
}
