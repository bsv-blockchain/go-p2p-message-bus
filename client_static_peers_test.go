package p2p

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Coverage note: there is intentionally no table test mirroring
// TestNewClientWithCustomBootstrapPeers for the StaticPeers field. The two
// would be near-identical scaffolding (only the field name differs) and dupl
// flags it as duplicate test code. Tolerance of empty/invalid multiaddrs
// is handled inside parsePeerMultiaddrs and already covered there; the two
// integration tests below exercise the static-peer-specific wiring.

// startStaticPeerPair brings up two clients where cl2 has cl1 configured as a
// static peer, and returns the underlying *client for both. The Client
// interfaces are owned by t.Cleanup callbacks so callers don't need them.
// All shared scaffolding lives here so the actual tests can assert on behavior
// without repeating setup.
func startStaticPeerPair(t *testing.T) (c1, c2 *client) {
	t.Helper()

	privKey1, err := GeneratePrivateKey()
	require.NoError(t, err)
	privKey2, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl1, err := NewClient(Config{
		Name:            "peer-a",
		PrivateKey:      privKey1,
		Port:            0,
		AllowPrivateIPs: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cl1.Close()) })

	c1 = cl1.(*client)
	addrs := c1.host.Addrs()
	require.NotEmpty(t, addrs, "peer1 should have listen addresses")

	// Prefer loopback to dodge flaky non-loopback interfaces in CI sandboxes.
	addrBase := addrs[0]
	for _, a := range addrs {
		if strings.Contains(a.String(), "127.0.0.1") {
			addrBase = a
			break
		}
	}
	staticAddr := fmt.Sprintf("%s/p2p/%s", addrBase.String(), c1.host.ID().String())

	cl2, err := NewClient(Config{
		Name:            "peer-b",
		PrivateKey:      privKey2,
		Port:            0,
		AllowPrivateIPs: true,
		StaticPeers:     []string{staticAddr},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cl2.Close()) })

	c2 = cl2.(*client)
	return c1, c2
}

// connectedness returns a function suitable for require.Eventually /
// assert.Eventually that reports whether `from` currently sees `to` as
// connected at the libp2p network layer.
func connectedness(from, to *client) func() bool {
	return func() bool {
		return from.host.Network().Connectedness(to.host.ID()) == network.Connected
	}
}

// TestStaticPeersConnectAtStartup brings up two clients with cl2 configured to
// dial cl1 as a static peer, then asserts cl2 ends up directly connected to
// cl1 without any explicit Connect call from the test.
func TestStaticPeersConnectAtStartup(t *testing.T) {
	c1, c2 := startStaticPeerPair(t)

	// connectToManagedPeers fires goroutines on startup; allow a moment for
	// the dial to land. The maintenance loop's fast-retry kicks in at 5s if
	// the initial dial happened to miss, so 6s is a safe upper bound.
	require.Eventually(t, connectedness(c2, c1),
		6*time.Second, 100*time.Millisecond,
		"peer2 should auto-connect to peer1 via StaticPeers")
}

// TestStaticPeersReconnectAfterClose verifies the maintenance loop dials again
// after a transient disconnect. Closes the inbound side of the connection on
// peer1's host, then waits for the loop to re-establish it.
func TestStaticPeersReconnectAfterClose(t *testing.T) {
	if testing.Short() {
		t.Skip("reconnect window is several seconds; skip in -short")
	}

	c1, c2 := startStaticPeerPair(t)

	require.Eventually(t, connectedness(c2, c1), 6*time.Second, 100*time.Millisecond)

	// Yank the connection from peer1's side.
	for _, conn := range c1.host.Network().ConnsToPeer(c2.host.ID()) {
		_ = conn.Close()
	}

	// The fast-retry loop runs at 5s during the first 2 minutes; allow up to
	// 10s for the dial to land.
	assert.Eventually(t, connectedness(c2, c1),
		10*time.Second, 200*time.Millisecond,
		"static peer should reconnect via maintenance loop")
}
