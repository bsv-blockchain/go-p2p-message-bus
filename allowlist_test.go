package p2p

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPeerID(t) is an existing helper in this package
// (client_peer_address_test.go:38): it generates a fresh keypair and returns
// its peer ID. Do not redefine it.

func TestPeerAllowlistDisabledWhenConfigEmpty(t *testing.T) {
	self := newTestPeerID(t)
	stranger := newTestPeerID(t)

	static := newTestPeerID(t)

	// StaticPeers alone must not switch filtering on.
	staticAddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/9905/p2p/%s", static)
	allowlist, err := newPeerAllowlist(
		Config{StaticPeers: []string{staticAddr}},
		self,
		parsePeerMultiaddrs([]string{staticAddr}, &captureLogger{}),
		&captureLogger{},
	)
	require.NoError(t, err)

	assert.False(t, allowlist.enabled())
	assert.True(t, allowlist.allows(stranger), "a disabled allowlist allows every peer")
	assert.True(t, allowlist.allows(static))
}

func TestPeerAllowlistNilReceiverAllowsEverything(t *testing.T) {
	var allowlist *peerAllowlist

	assert.False(t, allowlist.enabled())
	assert.True(t, allowlist.allows(newTestPeerID(t)))
}

func TestPeerAllowlistAllowsConfiguredAndSelfRejectsOthers(t *testing.T) {
	self := newTestPeerID(t)
	allowed := newTestPeerID(t)
	stranger := newTestPeerID(t)

	allowlist, err := newPeerAllowlist(
		Config{AllowedPublisherIDs: []string{allowed.String()}},
		self,
		nil,
		&captureLogger{},
	)
	require.NoError(t, err)

	assert.True(t, allowlist.enabled())
	assert.True(t, allowlist.allows(allowed), "configured peer is allowed")
	assert.True(t, allowlist.allows(self), "own ID is always allowed so local publishes validate")
	assert.False(t, allowlist.allows(stranger), "unlisted peer is rejected")
}

func TestPeerAllowlistIncludesStaticPeersButNotBootstrapPeers(t *testing.T) {
	self := newTestPeerID(t)
	allowed := newTestPeerID(t)
	static := newTestPeerID(t)
	bootstrap := newTestPeerID(t)

	staticAddrs := []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/9905/p2p/%s", static)}
	allowlist, err := newPeerAllowlist(
		Config{
			AllowedPublisherIDs: []string{allowed.String()},
			StaticPeers:         staticAddrs,
			BootstrapPeers:      []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/9906/p2p/%s", bootstrap)},
		},
		self,
		parsePeerMultiaddrs(staticAddrs, &captureLogger{}),
		&captureLogger{},
	)
	require.NoError(t, err)

	assert.True(t, allowlist.allows(static), "static peers are trusted by definition")
	assert.False(t, allowlist.allows(bootstrap), "bootstrap is a routing role, not a trust statement")
}

// TestPeerAllowlistIncludesStaticPeerIDFromRawEntryWhenResolutionFails pins
// R1: a /dnsaddr/ static peer whose DNS lookup fails at startup is dropped by
// parsePeerMultiaddrs (it logs and continues), so the resolved staticPeers
// list NewClient would pass in is empty here. The peer ID is still present in
// the raw multiaddr via an explicit /p2p/ component, so it must be learned
// from that string directly - no resolution needed.
func TestPeerAllowlistIncludesStaticPeerIDFromRawEntryWhenResolutionFails(t *testing.T) {
	self := newTestPeerID(t)
	static := newTestPeerID(t)

	staticAddr := fmt.Sprintf("/dnsaddr/example.invalid/p2p/%s", static)

	allowlist, err := newPeerAllowlist(
		Config{
			AllowedPublisherIDs: []string{newTestPeerID(t).String()},
			StaticPeers:         []string{staticAddr},
		},
		self,
		nil, // simulates: DNS resolution failed, nothing came out of parsePeerMultiaddrs
		&captureLogger{},
	)
	require.NoError(t, err)

	assert.True(t, allowlist.allows(static),
		"the peer ID was available in the raw multiaddr and needed no DNS resolution to learn")
}

// TestPeerAllowlistIgnoresRawStaticEntryWithoutPeerIDSuffix pins the
// complementary case: a /dnsaddr/ entry with no /p2p/ suffix genuinely has no
// ID to learn without resolution, so it must not appear in the allowlist and
// must not error.
func TestPeerAllowlistIgnoresRawStaticEntryWithoutPeerIDSuffix(t *testing.T) {
	self := newTestPeerID(t)

	allowlist, err := newPeerAllowlist(
		Config{
			AllowedPublisherIDs: []string{newTestPeerID(t).String()},
			StaticPeers:         []string{"/dnsaddr/example.invalid"},
		},
		self,
		nil,
		&captureLogger{},
	)
	require.NoError(t, err)
	assert.True(t, allowlist.enabled())
	assert.False(t, allowlist.allows(""),
		"an entry with no /p2p/ component must be skipped, never inserted as a zero peer.ID")
}

// TestPeerAllowlistIgnoresMalformedRawStaticEntry pins that a malformed
// static entry stays non-fatal for the allowlist, matching
// parsePeerMultiaddrs' existing non-fatal handling of the same entries.
func TestPeerAllowlistIgnoresMalformedRawStaticEntry(t *testing.T) {
	self := newTestPeerID(t)

	allowlist, err := newPeerAllowlist(
		Config{
			AllowedPublisherIDs: []string{newTestPeerID(t).String()},
			StaticPeers:         []string{"not-a-multiaddr"},
		},
		self,
		nil,
		&captureLogger{},
	)
	require.NoError(t, err)
	assert.True(t, allowlist.enabled())
	assert.False(t, allowlist.allows(""),
		"a malformed entry must be skipped, never inserted as a zero peer.ID")
}

func TestPeerAllowlistRejectsInvalidPeerID(t *testing.T) {
	allowlist, err := newPeerAllowlist(
		Config{AllowedPublisherIDs: []string{"not-a-peer-id"}},
		newTestPeerID(t),
		nil,
		&captureLogger{},
	)

	require.ErrorIs(t, err, ErrInvalidAllowedPublisherID)
	require.Nil(t, allowlist)
}

func TestPeerAllowlistLogsSetSize(t *testing.T) {
	log := &captureLogger{}

	_, err := newPeerAllowlist(
		Config{AllowedPublisherIDs: []string{newTestPeerID(t).String()}},
		newTestPeerID(t),
		nil,
		log,
	)
	require.NoError(t, err)

	assert.Contains(t, log.String(), "Peer allowlist enabled")
}

func TestBuildPubSubOptionsAddsValidatorWhenAllowlistEnabled(t *testing.T) {
	log := &captureLogger{}

	allowlist, err := newPeerAllowlist(
		Config{AllowedPublisherIDs: []string{newTestPeerID(t).String()}},
		newTestPeerID(t),
		nil,
		log,
	)
	require.NoError(t, err)

	// Peer exchange is on by default and contributes one option; the allowlist
	// validator is the second.
	opts, err := buildPubSubOptions(Config{}, allowlist, nil, log)
	require.NoError(t, err)
	require.Len(t, opts, 2)
}

func TestBuildPubSubOptionsNoValidatorWhenAllowlistDisabled(t *testing.T) {
	log := &captureLogger{}

	allowlist, err := newPeerAllowlist(Config{}, newTestPeerID(t), nil, log)
	require.NoError(t, err)

	opts, err := buildPubSubOptions(Config{}, allowlist, nil, log)
	require.NoError(t, err)
	require.Len(t, opts, 1, "peer exchange only")
}

func TestNewClientRejectsInvalidAllowedPublisherID(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:                testPeerName,
		PrivateKey:          privKey,
		AllowedPublisherIDs: []string{"not-a-peer-id"},
	})

	require.ErrorIs(t, err, ErrInvalidAllowedPublisherID)
	require.Nil(t, cl)
}

// TestPublishSucceedsWithAllowlistEnabled pins the trap in this feature:
// Topic.Publish runs the default validators synchronously via
// validation.ValidateLocal, so a node missing from its own allowlist cannot
// publish anything.
func TestPublishSucceedsWithAllowlistEnabled(t *testing.T) {
	const topicName = "allowlist-local-publish-test"

	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:                testPeerName,
		PrivateKey:          privKey,
		Port:                0,
		AllowedPublisherIDs: []string{newTestPeerID(t).String()},
	})
	require.NoError(t, err)

	defer func() {
		require.NoError(t, cl.Close())
	}()

	ch := cl.Subscribe(topicName)
	require.NotNil(t, ch)

	// Subscribe joins the topic on a goroutine; wait for it before publishing.
	c := cl.(*client)
	require.Eventually(t, func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		_, ok := c.topics[topicName]

		return ok
	}, 5*time.Second, 50*time.Millisecond, "topic was never joined")

	require.NoError(t, cl.Publish(context.Background(), topicName, []byte("hello")),
		"publish must succeed: the allowlist must contain the node's own peer ID")
}

// TestDropReporterFirstDropLogsAndResets pins R2: the very first drop always
// logs immediately - lastLog's zero value is recordDrop's "never logged yet"
// sentinel, bypassing the elapsed-time check rather than relying on it - and
// logging resets the counter.
func TestDropReporterFirstDropLogsAndResets(t *testing.T) {
	r := newDropReporter()
	log := &captureLogger{}

	r.recordDrop(log)

	assert.Contains(t, log.String(), "Dropped 1 message(s) from non-allowlisted peers since the last report")
	assert.Equal(t, uint64(0), r.dropped.Load(), "the winning call resets the counter")
}

// TestDropReporterSuppressesWithinInterval pins that once a window has been
// claimed, further drops within dropLogInterval accumulate in the counter
// without emitting another log line.
func TestDropReporterSuppressesWithinInterval(t *testing.T) {
	r := newDropReporter()
	log := &captureLogger{}

	// Anchor start in the past and force lastLog to "just now" (relative to
	// start) so the next calls fall inside the window without depending on
	// real elapsed time. lastLog is deliberately non-zero here: zero is the
	// reporter's own "never logged" sentinel, which this test is not
	// exercising.
	r.start = time.Now().Add(-time.Minute)
	r.lastLog.Store(int64(time.Minute))

	r.recordDrop(log)
	r.recordDrop(log)
	r.recordDrop(log)

	assert.Empty(t, log.String(), "no line should be logged inside the interval")
	assert.Equal(t, uint64(3), r.dropped.Load(), "drops still aggregate while suppressed")
}

// TestDropReporterLogsAgainAfterIntervalElapses pins that a new window opens,
// and logs, once dropLogInterval has passed since the last emitted line.
func TestDropReporterLogsAgainAfterIntervalElapses(t *testing.T) {
	r := newDropReporter()
	log := &captureLogger{}

	// The prior line was emitted 1ns after start (non-zero, so this exercises
	// the elapsed-time comparison rather than the "never logged" sentinel),
	// and start itself is far enough in the past that dropLogInterval has
	// since elapsed.
	r.start = time.Now().Add(-dropLogInterval - time.Second)
	r.lastLog.Store(1)
	r.dropped.Store(2) // as if two drops had already accumulated in the prior window

	r.recordDrop(log)

	assert.Contains(t, log.String(), "Dropped 3 message(s) from non-allowlisted peers since the last report")
	assert.Equal(t, uint64(0), r.dropped.Load())
}

// TestDropReporterConcurrentDropsAreRaceSafeAndNeverLoseACount pins the
// concurrency contract under the race detector: every concurrent call
// increments the shared counter (no lost counts), and exactly one goroutine
// wins the right to log for the reporter's first window.
func TestDropReporterConcurrentDropsAreRaceSafeAndNeverLoseACount(t *testing.T) {
	r := newDropReporter()
	log := &captureLogger{}

	const goroutines = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			r.recordDrop(log)
		}()
	}
	wg.Wait()

	output := log.String()
	assert.Equal(t, 1, strings.Count(output, "Dropped"), "exactly one goroutine logs the window")

	var logged uint64
	_, err := fmt.Sscanf(output, "[INFO] Dropped %d message(s)", &logged)
	require.NoError(t, err)

	remaining := r.dropped.Load()
	assert.Equal(t, uint64(goroutines), logged+remaining,
		"every call's increment must be accounted for: none lost to the race")
}

// TestPeerAllowlistCircuitAddrAllowsTargetNotRelay pins the p2p-circuit
// hazard: a relayed static peer address carries two /p2p/ components, the
// relay's first and the target's last. The dialing path resolves such an
// address to the TARGET (peer.AddrInfoFromP2pAddr -> peer.SplitAddr, which
// takes the last component), so the allowlist must agree. Taking the first
// component instead would silently admit the relay - which in this library is
// a bootstrap peer, i.e. exactly the public infrastructure the design
// excludes.
func TestPeerAllowlistCircuitAddrAllowsTargetNotRelay(t *testing.T) {
	self := newTestPeerID(t)
	relay := newTestPeerID(t)
	target := newTestPeerID(t)

	staticAddrs := []string{
		fmt.Sprintf("/ip4/1.2.3.4/tcp/4001/p2p/%s/p2p-circuit/p2p/%s", relay, target),
	}

	allowlist, err := newPeerAllowlist(
		Config{
			AllowedPublisherIDs: []string{newTestPeerID(t).String()},
			StaticPeers:         staticAddrs,
		},
		self,
		parsePeerMultiaddrs(staticAddrs, &captureLogger{}),
		&captureLogger{},
	)
	require.NoError(t, err)

	assert.True(t, allowlist.allows(target),
		"the relayed target is the configured static peer and must be allowed")
	assert.False(t, allowlist.allows(relay),
		"the relay merely carries the traffic and must not be admitted as a publisher")
}

// TestDropReporterFromNewPeerAllowlistLogsAcrossWindows covers the production
// anchor, which no other reporter test does: it builds the reporter the way
// NewClient does, through newPeerAllowlist, and proves it still logs in a
// second window. A zero-valued start saturates time.Since at math.MaxInt64, so
// the reporter would log exactly once and then stay silent for the life of the
// process; this test fails outright in that configuration.
func TestDropReporterFromNewPeerAllowlistLogsAcrossWindows(t *testing.T) {
	log := &captureLogger{}

	allowlist, err := newPeerAllowlist(
		Config{AllowedPublisherIDs: []string{newTestPeerID(t).String()}},
		newTestPeerID(t),
		nil,
		log,
	)
	require.NoError(t, err)

	require.False(t, allowlist.allows(newTestPeerID(t)), "precondition: the reporter is on a live rejection path")

	// First window: the first drop always logs.
	allowlist.drops.recordDrop(log)

	// Open a second window without sleeping by rewinding the anchor one
	// interval, which advances time.Since(start) by exactly that much. lastLog
	// is left exactly as recordDrop set it. Under a zero-valued anchor this has
	// no effect at all, because the elapsed time is already saturated.
	allowlist.drops.start = allowlist.drops.start.Add(-dropLogInterval)
	allowlist.drops.recordDrop(log)

	assert.Equal(t, 2, strings.Count(log.String(), "Dropped"),
		"a reporter built the production way must keep logging in later windows")
}

// TestPeerAllowlistWarnsAboutStaticPeerThatContributedNoID pins that a static
// peer which ends up in neither route - a bare /dnsaddr/ whose startup DNS
// lookup failed, so it is absent from the resolved list, and which names no
// /p2p/ component for the raw scan to find - is reported rather than silently
// dropped from the publisher set for the process lifetime.
func TestPeerAllowlistWarnsAboutStaticPeerThatContributedNoID(t *testing.T) {
	log := &captureLogger{}

	_, err := newPeerAllowlist(
		Config{
			AllowedPublisherIDs: []string{newTestPeerID(t).String()},
			StaticPeers:         []string{"/dnsaddr/example.invalid"},
		},
		newTestPeerID(t),
		nil, // simulates: DNS resolution failed, nothing came out of parsePeerMultiaddrs
		log,
	)
	require.NoError(t, err)

	assert.Contains(t, log.String(), "contributed no publisher ID")
	assert.Contains(t, log.String(), "/dnsaddr/example.invalid")
}

// TestPeerAllowlistDoesNotWarnWhenStaticPeerIDsAreKnown pins the negative: an
// entry that names its ID, and one that resolved, are both accounted for, so
// no warning is emitted.
func TestPeerAllowlistDoesNotWarnWhenStaticPeerIDsAreKnown(t *testing.T) {
	log := &captureLogger{}

	staticAddrs := []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/9905/p2p/%s", newTestPeerID(t))}

	_, err := newPeerAllowlist(
		Config{
			AllowedPublisherIDs: []string{newTestPeerID(t).String()},
			StaticPeers:         staticAddrs,
		},
		newTestPeerID(t),
		parsePeerMultiaddrs(staticAddrs, &captureLogger{}),
		log,
	)
	require.NoError(t, err)

	assert.NotContains(t, log.String(), "contributed no publisher ID")
}

// TestPeerAllowlistDoesNotWarnWhenABareEntryResolved pins that a bare
// /dnsaddr/ entry which did resolve is not reported: its ID reached the
// publisher set through the resolved list even though the raw scan could not
// see it.
func TestPeerAllowlistDoesNotWarnWhenABareEntryResolved(t *testing.T) {
	log := &captureLogger{}

	resolvedID := newTestPeerID(t)

	_, err := newPeerAllowlist(
		Config{
			AllowedPublisherIDs: []string{newTestPeerID(t).String()},
			StaticPeers:         []string{"/dnsaddr/example.invalid"},
		},
		newTestPeerID(t),
		[]peer.AddrInfo{{ID: resolvedID}}, // as if the dnsaddr TXT lookup had succeeded
		log,
	)
	require.NoError(t, err)

	assert.NotContains(t, log.String(), "contributed no publisher ID")
}

// TestPeerAllowlistLogsConfiguredAndAugmentedCountsSeparately pins that the
// startup line distinguishes the configured publisher IDs from what the
// implicit augmentation added, so an operator who configures one ID is not
// left wondering why the node reports three.
func TestPeerAllowlistLogsConfiguredAndAugmentedCountsSeparately(t *testing.T) {
	log := &captureLogger{}

	staticAddrs := []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/9905/p2p/%s", newTestPeerID(t))}

	_, err := newPeerAllowlist(
		Config{
			AllowedPublisherIDs: []string{newTestPeerID(t).String()},
			StaticPeers:         staticAddrs,
		},
		newTestPeerID(t),
		parsePeerMultiaddrs(staticAddrs, &captureLogger{}),
		log,
	)
	require.NoError(t, err)

	assert.Contains(t, log.String(),
		"accepting pubsub messages from 3 peer(s) - 1 configured, 1 for this node, 1 from StaticPeers")
}
