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
	w.recordReset(resetAt, wdPeer)

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
