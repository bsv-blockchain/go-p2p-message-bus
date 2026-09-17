package p2p

import (
	"context"
	"fmt"
	"testing"
	"time"

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
		Config{AllowedPeerIDs: []string{allowed.String()}},
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
			AllowedPeerIDs: []string{allowed.String()},
			StaticPeers:    staticAddrs,
			BootstrapPeers: []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/9906/p2p/%s", bootstrap)},
		},
		self,
		parsePeerMultiaddrs(staticAddrs, &captureLogger{}),
		&captureLogger{},
	)
	require.NoError(t, err)

	assert.True(t, allowlist.allows(static), "static peers are trusted by definition")
	assert.False(t, allowlist.allows(bootstrap), "bootstrap is a routing role, not a trust statement")
}

func TestPeerAllowlistRejectsInvalidPeerID(t *testing.T) {
	allowlist, err := newPeerAllowlist(
		Config{AllowedPeerIDs: []string{"not-a-peer-id"}},
		newTestPeerID(t),
		nil,
		&captureLogger{},
	)

	require.ErrorIs(t, err, ErrInvalidAllowedPeerID)
	require.Nil(t, allowlist)
}

func TestPeerAllowlistLogsSetSize(t *testing.T) {
	log := &captureLogger{}

	_, err := newPeerAllowlist(
		Config{AllowedPeerIDs: []string{newTestPeerID(t).String()}},
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
		Config{AllowedPeerIDs: []string{newTestPeerID(t).String()}},
		newTestPeerID(t),
		nil,
		log,
	)
	require.NoError(t, err)

	// Peer exchange is on by default and contributes one option; the allowlist
	// validator is the second.
	opts, err := buildPubSubOptions(Config{}, allowlist, log)
	require.NoError(t, err)
	require.Len(t, opts, 2)
}

func TestBuildPubSubOptionsNoValidatorWhenAllowlistDisabled(t *testing.T) {
	log := &captureLogger{}

	allowlist, err := newPeerAllowlist(Config{}, newTestPeerID(t), nil, log)
	require.NoError(t, err)

	opts, err := buildPubSubOptions(Config{}, allowlist, log)
	require.NoError(t, err)
	require.Len(t, opts, 1, "peer exchange only")
}

func TestNewClientRejectsInvalidAllowedPeerID(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:           testPeerName,
		PrivateKey:     privKey,
		AllowedPeerIDs: []string{"not-a-peer-id"},
	})

	require.ErrorIs(t, err, ErrInvalidAllowedPeerID)
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
		Name:           testPeerName,
		PrivateKey:     privKey,
		Port:           0,
		AllowedPeerIDs: []string{newTestPeerID(t).String()},
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
