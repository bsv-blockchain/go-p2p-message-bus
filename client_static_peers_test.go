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

// TestNewClientWithStaticPeers parallels TestNewClientWithCustomBootstrapPeers:
// the client should accept StaticPeers config without erroring, and an invalid
// entry should be logged-and-skipped rather than failing client creation.
func TestNewClientWithStaticPeers(t *testing.T) {
	tests := []struct {
		name        string
		staticPeers []string
		wantErr     bool
	}{
		{
			name: "single static peer",
			staticPeers: []string{
				testRelayPeerMultiaddr,
			},
			wantErr: false,
		},
		{
			name:        "empty static peers",
			staticPeers: []string{},
			wantErr:     false,
		},
		{
			name: "invalid static peer is tolerated",
			staticPeers: []string{
				"not-a-multiaddr",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			cl, err := NewClient(Config{
				Name:        testPeerName,
				PrivateKey:  privKey,
				StaticPeers: tt.staticPeers,
			})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cl)
			require.NoError(t, cl.Close())
		})
	}
}

// TestStaticPeersConnectAtStartup brings up two clients with cl2 configured to
// dial cl1 as a static peer, then asserts cl2 ends up directly connected to
// cl1 without any explicit Connect call from the test.
func TestStaticPeersConnectAtStartup(t *testing.T) {
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
	defer func() { require.NoError(t, cl1.Close()) }()

	c1 := cl1.(*client)
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
	defer func() { require.NoError(t, cl2.Close()) }()

	c2 := cl2.(*client)

	// connectToStaticPeers fires goroutines on startup; allow a moment for the
	// dial to land. The maintenance loop's fast-retry kicks in at 5s if the
	// initial dial happened to miss, so 6s is a safe upper bound.
	require.Eventually(t, func() bool {
		return c2.host.Network().Connectedness(c1.host.ID()) == network.Connected
	}, 6*time.Second, 100*time.Millisecond, "peer2 should auto-connect to peer1 via StaticPeers")
}

// TestStaticPeersReconnectAfterClose verifies the maintenance loop dials again
// after a transient disconnect. Closes the inbound side of the connection on
// peer1's host, then waits for the loop to re-establish it.
func TestStaticPeersReconnectAfterClose(t *testing.T) {
	if testing.Short() {
		t.Skip("reconnect window is several seconds; skip in -short")
	}

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
	defer func() { require.NoError(t, cl1.Close()) }()

	c1 := cl1.(*client)
	addrs := c1.host.Addrs()
	require.NotEmpty(t, addrs)
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
	defer func() { require.NoError(t, cl2.Close()) }()

	c2 := cl2.(*client)

	// Initial connect.
	require.Eventually(t, func() bool {
		return c2.host.Network().Connectedness(c1.host.ID()) == network.Connected
	}, 6*time.Second, 100*time.Millisecond)

	// Yank the connection from peer1's side.
	for _, conn := range c1.host.Network().ConnsToPeer(c2.host.ID()) {
		_ = conn.Close()
	}

	// The fast-retry loop runs at 5s during the first 2 minutes; allow up to
	// 10s for the dial to land.
	assert.Eventually(t, func() bool {
		return c2.host.Network().Connectedness(c1.host.ID()) == network.Connected
	}, 10*time.Second, 200*time.Millisecond, "static peer should reconnect via maintenance loop")
}
