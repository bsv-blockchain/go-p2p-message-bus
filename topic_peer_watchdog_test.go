package p2p

import (
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	wdTopic       = "subtree"
	wdGrace       = 30 * time.Second
	wdMinInterval = 2 * time.Minute
)

const wdPeer = peer.ID("peer-b")

func newTestWatchdog(assumeShared bool) *topicPeerWatchdog {
	return newTopicPeerWatchdog(wdGrace, wdMinInterval, assumeShared)
}

func TestWatchdogNeverResetsPeerNeverSeenInTopic(t *testing.T) {
	w := newTestWatchdog(false)
	t0 := time.Unix(1000, 0)

	// A pure bootstrap server that never joins our topics must be left alone.
	assert.False(t, w.observe(t0, wdPeer, wdTopic, true, false))
	assert.False(t, w.observe(t0.Add(10*time.Minute), wdPeer, wdTopic, true, false))
}

func TestWatchdogResetsSeenPeerMissingPastGrace(t *testing.T) {
	w := newTestWatchdog(false)
	t0 := time.Unix(1000, 0)

	assert.False(t, w.observe(t0, wdPeer, wdTopic, true, true), "present in topic")
	assert.False(t, w.observe(t0.Add(time.Second), wdPeer, wdTopic, true, false), "missing, grace starts")
	assert.False(t, w.observe(t0.Add(time.Second+wdGrace-time.Millisecond), wdPeer, wdTopic, true, false), "still within grace")
	assert.True(t, w.observe(t0.Add(time.Second+wdGrace), wdPeer, wdTopic, true, false), "grace elapsed")
}

func TestWatchdogIgnoresDisconnectedPeer(t *testing.T) {
	w := newTestWatchdog(false)
	t0 := time.Unix(1000, 0)

	w.observe(t0, wdPeer, wdTopic, true, true)

	// Not connected: the reconnect loop owns this case, and the grace timer must
	// not keep running while the peer is away.
	assert.False(t, w.observe(t0.Add(time.Minute), wdPeer, wdTopic, false, false))
	assert.False(t, w.observe(t0.Add(time.Minute+time.Second), wdPeer, wdTopic, true, false), "reconnected, grace restarts")
	assert.True(t, w.observe(t0.Add(time.Minute+time.Second+wdGrace), wdPeer, wdTopic, true, false))
}

func TestWatchdogClearsTimerWhenPeerReturns(t *testing.T) {
	w := newTestWatchdog(false)
	t0 := time.Unix(1000, 0)

	w.observe(t0, wdPeer, wdTopic, true, true)
	w.observe(t0.Add(time.Second), wdPeer, wdTopic, true, false)
	w.observe(t0.Add(20*time.Second), wdPeer, wdTopic, true, true)

	assert.False(t, w.observe(t0.Add(40*time.Second), wdPeer, wdTopic, true, false), "missing again, fresh grace")
}

func TestWatchdogRateLimitsResetsPerPeer(t *testing.T) {
	w := newTestWatchdog(false)
	t0 := time.Unix(1000, 0)

	w.observe(t0, wdPeer, wdTopic, true, true)
	w.observe(t0.Add(time.Second), wdPeer, wdTopic, true, false)

	resetAt := t0.Add(time.Second + wdGrace)
	assert.True(t, w.observe(resetAt, wdPeer, wdTopic, true, false))
	w.recordReset(resetAt, wdPeer, []string{wdTopic})

	// Still missing right after the reset: wait out the minimum interval.
	assert.False(t, w.observe(resetAt.Add(wdGrace+time.Second), wdPeer, wdTopic, true, false))
	assert.False(t, w.observe(resetAt.Add(wdMinInterval-time.Millisecond), wdPeer, wdTopic, true, false))
	assert.True(t, w.observe(resetAt.Add(wdMinInterval), wdPeer, wdTopic, true, false))
}

func TestWatchdogAssumeSharedTopicsSkipsSeenGuard(t *testing.T) {
	w := newTestWatchdog(true)
	t0 := time.Unix(1000, 0)

	// With the deployment declaring that managed peers share our topics, a
	// peer this process never saw in the topic still counts.
	assert.False(t, w.observe(t0, wdPeer, wdTopic, true, false))
	assert.True(t, w.observe(t0.Add(wdGrace), wdPeer, wdTopic, true, false))
}

func TestWatchdogTracksTopicsIndependently(t *testing.T) {
	w := newTestWatchdog(false)
	t0 := time.Unix(1000, 0)

	w.observe(t0, wdPeer, "block", true, true)
	w.observe(t0, wdPeer, wdTopic, true, true)

	// Present in "block", missing from "subtree": the subtree gap alone must
	// trigger, as in the 2026-10-19 incident where blocks still flowed.
	w.observe(t0.Add(time.Second), wdPeer, "block", true, true)
	w.observe(t0.Add(time.Second), wdPeer, wdTopic, true, false)
	assert.False(t, w.observe(t0.Add(time.Second+wdGrace), wdPeer, "block", true, true))
	assert.True(t, w.observe(t0.Add(time.Second+wdGrace), wdPeer, wdTopic, true, false))
}

func TestWatchdogGivesUpAfterRepeatedResetsWithoutRecovery(t *testing.T) {
	w := newTestWatchdog(false)
	now := time.Unix(1000, 0)

	w.observe(now, wdPeer, wdTopic, true, true)

	// A peer that left the topic on purpose never comes back: after
	// maxConsecutiveResets resets with no recovery, stop resetting it. Resets
	// are spaced past the per-peer cooldown so only the give-up is exercised.
	for i := 0; i < maxConsecutiveResets; i++ {
		w.observe(now, wdPeer, wdTopic, true, false)
		now = now.Add(resetCooldown + wdGrace)
		assert.True(t, w.observe(now, wdPeer, wdTopic, true, false), "reset %d", i+1)
		w.recordReset(now, wdPeer, []string{wdTopic})
	}

	w.observe(now, wdPeer, wdTopic, true, false)
	assert.False(t, w.observe(now.Add(time.Hour), wdPeer, wdTopic, true, false), "must give up")

	// Seeing it in the topic again re-arms the watchdog.
	now = now.Add(time.Hour)
	w.observe(now, wdPeer, wdTopic, true, true)
	w.observe(now, wdPeer, wdTopic, true, false)
	assert.True(t, w.observe(now.Add(wdGrace), wdPeer, wdTopic, true, false), "re-armed after recovery")
}

func TestRecoveryPeersUseOnlyExplicitlyConfiguredPeers(t *testing.T) {
	staticPeers := []peer.AddrInfo{{ID: "static-peer"}}
	defaults := []peer.AddrInfo{{ID: "ipfs-default-bootstrap"}}

	// No BootstrapPeers configured: NewClient fell back to the public IPFS
	// bootstrap nodes, which never join our topics and must not be watched.
	got := recoveryPeers(Config{}, defaults, staticPeers)
	assert.Equal(t, staticPeers, got)
}

func TestRecoveryPeersUseTheResolvedBootstrapSnapshot(t *testing.T) {
	staticPeers := []peer.AddrInfo{{ID: "static-peer"}}
	resolved := []peer.AddrInfo{{ID: "resolved-bootstrap"}}

	// The explicit bootstrap set is resolved once by NewClient. A second DNS
	// lookup could fail or return a different set and drop a connected
	// bootstrap peer from recovery for the client's lifetime, so the snapshot
	// must be used as-is.
	cfg := Config{BootstrapPeers: []string{"/dnsaddr/bootstrap.invalid"}}
	got := recoveryPeers(cfg, resolved, staticPeers)
	assert.Equal(t, []peer.AddrInfo{resolved[0], staticPeers[0]}, got)
}

func TestWatchdogHealthyTopicDoesNotRearmResetsForLeftTopic(t *testing.T) {
	w := newTestWatchdog(false)
	now := time.Unix(1000, 0)

	w.observe(now, wdPeer, "block", true, true)
	w.observe(now, wdPeer, wdTopic, true, true)

	// The peer left wdTopic on purpose and stays in "block". Its presence in
	// "block" every round must not re-arm resets over wdTopic.
	resets := 0
	for i := 0; i < 10; i++ {
		w.observe(now, wdPeer, "block", true, true)
		w.observe(now, wdPeer, wdTopic, true, false)
		now = now.Add(wdMinInterval + wdGrace)
		w.observe(now, wdPeer, "block", true, true)
		if w.observe(now, wdPeer, wdTopic, true, false) {
			w.recordReset(now, wdPeer, []string{wdTopic})
			resets++
		}
	}

	assert.Equal(t, maxConsecutiveResets, resets)
}

func TestWatchdogCapsResetsPerPeerWithinCooldown(t *testing.T) {
	w := newTestWatchdog(false)
	now := time.Unix(1000, 0)

	// A peer that flaps in and out of a topic would otherwise be reset every
	// minimum interval. Each reset also costs the remote one of pubsub's four
	// writer respawns, so cap resets per peer and keep the cap when the peer
	// reappears.
	resets := 0
	for i := 0; i < 4; i++ {
		w.observe(now, wdPeer, wdTopic, true, true)
		w.observe(now, wdPeer, wdTopic, true, false)
		now = now.Add(wdMinInterval + wdGrace)
		if w.observe(now, wdPeer, wdTopic, true, false) {
			w.recordReset(now, wdPeer, []string{wdTopic})
			resets++
		}
	}
	assert.Equal(t, maxResetsPerCooldown, resets, "resets within one cooldown")

	// Once the cooldown has passed since the latest reset, recovery is allowed again.
	now = now.Add(resetCooldown)
	w.observe(now, wdPeer, wdTopic, true, true)
	w.observe(now, wdPeer, wdTopic, true, false)
	assert.True(t, w.observe(now.Add(wdGrace), wdPeer, wdTopic, true, false))
}

func TestWatchdogDisconnectRearmsGivenUpTopic(t *testing.T) {
	w := newTestWatchdog(false)
	now := time.Unix(1000, 0)

	w.observe(now, wdPeer, wdTopic, true, true)

	// Exhaust the per-topic give-up, spacing resets past the window cap.
	for i := 0; i < maxConsecutiveResets; i++ {
		w.observe(now, wdPeer, wdTopic, true, false)
		now = now.Add(resetCooldown + wdGrace)
		assert.True(t, w.observe(now, wdPeer, wdTopic, true, false), "reset %d", i+1)
		w.recordReset(now, wdPeer, []string{wdTopic})
	}

	// The disconnect caused by our own last reset does not re-arm the topic.
	w.observe(now.Add(time.Second), wdPeer, wdTopic, false, false)

	w.observe(now, wdPeer, wdTopic, true, false)
	now = now.Add(resetCooldown + wdGrace)
	assert.False(t, w.observe(now, wdPeer, wdTopic, true, false), "given up")

	// A disconnect ends that session; the next connection is a new one and a
	// later stale state must be repairable even if the peer was not seen in the
	// topic in between.
	w.observe(now, wdPeer, wdTopic, false, false)
	now = now.Add(resetCooldown)
	w.observe(now, wdPeer, wdTopic, true, false)
	assert.True(t, w.observe(now.Add(wdGrace), wdPeer, wdTopic, true, false), "re-armed by reconnect")
}

func TestWatchdogCooldownRunsFromLatestReset(t *testing.T) {
	w := newTestWatchdog(false)
	start := time.Unix(1000, 0)

	// tryReset makes the peer reappear and then go missing so that its grace
	// period ends at offset at, and reports whether the watchdog resets it then.
	tryReset := func(at time.Duration) bool {
		now := start.Add(at)
		w.observe(now.Add(-wdGrace), wdPeer, wdTopic, true, true)
		w.observe(now.Add(-wdGrace), wdPeer, wdTopic, true, false)

		if !w.observe(now, wdPeer, wdTopic, true, false) {
			return false
		}

		w.recordReset(now, wdPeer, []string{wdTopic})

		return true
	}

	// go-libp2p-pubsub starts its writer-respawn count over only after ten idle
	// minutes since the latest death, not as each death ages out. With a sliding
	// window, resets at 0, 150, 605, 755 and 1210s would all pass while the
	// remote never sees ten idle minutes, and the fifth would exhaust it.
	assert.True(t, tryReset(0), "first reset")
	assert.True(t, tryReset(150*time.Second), "second reset")
	assert.False(t, tryReset(605*time.Second), "the first reset has aged ten minutes, but the second has not")
	assert.False(t, tryReset(755*time.Second))
	assert.False(t, tryReset(150*time.Second+resetCooldown-time.Second), "just before the cooldown after the latest reset")
	// The grace timer does not run while the peer is capped, so the next reset
	// comes one grace period after the cooldown ends.
	assert.True(t, tryReset(150*time.Second+resetCooldown+wdGrace), "once the cooldown has passed since the latest reset")
}

func TestWatchdogNewConnectionRearmsGivenUpTopic(t *testing.T) {
	w := newTestWatchdog(false)
	now := time.Unix(1000, 0)

	w.observeConns(now, wdPeer, []string{"conn-1"})
	w.observe(now, wdPeer, wdTopic, true, true)

	// Exhaust the per-topic give-up. Each reset closes the connection and
	// redials it, which brings a new connection that must not re-arm the topic.
	for i := 0; i < maxConsecutiveResets; i++ {
		w.observe(now, wdPeer, wdTopic, true, false)
		now = now.Add(resetCooldown + wdGrace)
		require.True(t, w.observe(now, wdPeer, wdTopic, true, false), "reset %d", i+1)
		w.recordReset(now, wdPeer, []string{wdTopic})
		w.observeConns(now.Add(time.Second), wdPeer, []string{fmt.Sprintf("redial-%d", i)})
	}

	w.observe(now, wdPeer, wdTopic, true, false)
	now = now.Add(resetCooldown + wdGrace)
	assert.False(t, w.observe(now, wdPeer, wdTopic, true, false), "given up")

	// The peer reconnects between two rounds, so no round ever sees it
	// disconnected; the new connection alone must re-arm the topic.
	w.observeConns(now, wdPeer, []string{"conn-2"})
	w.observe(now, wdPeer, wdTopic, true, false)
	assert.True(t, w.observe(now.Add(wdGrace), wdPeer, wdTopic, true, false), "re-armed by the new connection")
}

func TestRelayInUseDetectsCircuitThroughPeer(t *testing.T) {
	ma := func(s string) multiaddr.Multiaddr {
		m, err := multiaddr.NewMultiaddr(s)
		require.NoError(t, err)
		return m
	}

	relayKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	relayID, err := peer.IDFromPrivateKey(relayKey)
	require.NoError(t, err)

	otherKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	otherID, err := peer.IDFromPrivateKey(otherKey)
	require.NoError(t, err)

	direct := ma("/ip4/10.0.0.1/tcp/9905")
	ourCircuit := ma("/ip4/10.0.0.2/tcp/9905/p2p/" + relayID.String() + "/p2p-circuit")
	relayedConn := ma("/ip4/10.0.0.2/tcp/9905/p2p/" + relayID.String() + "/p2p-circuit/p2p/" + otherID.String())

	assert.False(t, relayInUse(relayID, []multiaddr.Multiaddr{direct}, []multiaddr.Multiaddr{direct}), "plain direct peer")
	assert.True(t, relayInUse(relayID, []multiaddr.Multiaddr{direct, ourCircuit}, nil), "we listen via a reservation on it")
	assert.True(t, relayInUse(relayID, nil, []multiaddr.Multiaddr{relayedConn}), "a connection is relayed through it")
	assert.False(t, relayInUse(otherID, []multiaddr.Multiaddr{ourCircuit}, []multiaddr.Multiaddr{relayedConn}), "relayed peer is not the relay")
}

func TestRecoveryPeersDeduplicatesByID(t *testing.T) {
	a1 := multiaddr.StringCast("/ip4/10.0.0.1/tcp/9905")
	a2 := multiaddr.StringCast("/ip4/10.0.0.1/udp/9905/quic-v1")
	a3 := multiaddr.StringCast("/ip4/10.0.0.9/tcp/9905")

	bootstrap := []peer.AddrInfo{{ID: "peer-a", Addrs: []multiaddr.Multiaddr{a1}}, {ID: "peer-a", Addrs: []multiaddr.Multiaddr{a2}}}
	static := []peer.AddrInfo{{ID: "peer-a", Addrs: []multiaddr.Multiaddr{a1}}, {ID: "peer-b", Addrs: []multiaddr.Multiaddr{a3}}}

	got := recoveryPeers(Config{BootstrapPeers: []string{"x"}}, bootstrap, static)
	assert.Len(t, got, 2)
	assert.Equal(t, peer.ID("peer-a"), got[0].ID)
	assert.ElementsMatch(t, []multiaddr.Multiaddr{a1, a2}, got[0].Addrs)
	assert.Equal(t, peer.ID("peer-b"), got[1].ID)
}

func TestWriterRepairBackoff(t *testing.T) {
	b := newWriterRepairBackoff()
	now := time.Unix(1000, 0)

	assert.True(t, b.allow(now, wdPeer), "first repair goes immediately")
	b.record(now, wdPeer)

	assert.False(t, b.allow(now.Add(writerRepairInitialDelay-time.Millisecond), wdPeer))
	assert.True(t, b.allow(now.Add(writerRepairInitialDelay), wdPeer))
	b.record(now.Add(writerRepairInitialDelay), wdPeer)

	// The delay doubles while the writer stays missing.
	next := now.Add(writerRepairInitialDelay)
	assert.False(t, b.allow(next.Add(2*writerRepairInitialDelay-time.Millisecond), wdPeer))
	assert.True(t, b.allow(next.Add(2*writerRepairInitialDelay), wdPeer))

	// A healthy writer clears the backoff.
	b.clear(wdPeer)
	assert.True(t, b.allow(next, wdPeer))
}
