package p2p

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
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
	// maxConsecutiveResets resets with no recovery, stop resetting it.
	for i := 0; i < maxConsecutiveResets; i++ {
		w.observe(now, wdPeer, wdTopic, true, false)
		now = now.Add(wdMinInterval + wdGrace)
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
