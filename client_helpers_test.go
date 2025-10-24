package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testLocalMultiaddr = "/ip4/127.0.0.1/tcp/4001"

func TestParseMultiaddrs(t *testing.T) {
	tests := []struct {
		name          string
		addrs         []string
		expectedCount int
		description   string
	}{
		{
			name:          "valid multiaddrs",
			addrs:         []string{testLocalMultiaddr, "/ip6/::1/tcp/4001"},
			expectedCount: 2,
			description:   "valid multiaddrs should be parsed successfully",
		},
		{
			name:          "single valid multiaddr",
			addrs:         []string{"/ip4/192.168.1.1/tcp/4001"},
			expectedCount: 1,
			description:   "single multiaddr should be parsed",
		},
		{
			name:          "empty array",
			addrs:         []string{},
			expectedCount: 0,
			description:   "empty array should return empty result",
		},
		{
			name:          "nil array",
			addrs:         nil,
			expectedCount: 0,
			description:   "nil array should return empty result",
		},
		{
			name:          "invalid multiaddr",
			addrs:         []string{"not-a-valid-multiaddr"},
			expectedCount: 0,
			description:   "invalid multiaddrs should be skipped",
		},
		{
			name:          "mixed valid and invalid",
			addrs:         []string{testLocalMultiaddr, "invalid", "/ip6/::1/tcp/4001"},
			expectedCount: 2,
			description:   "only valid multiaddrs should be included",
		},
		{
			name:          "empty string",
			addrs:         []string{""},
			expectedCount: 0,
			description:   "empty string should be skipped",
		},
		{
			name:          "complex multiaddr",
			addrs:         []string{"/ip4/192.168.1.1/tcp/4001/p2p/QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N"},
			expectedCount: 1,
			description:   "complex multiaddr with peer ID should be parsed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMultiaddrs(tt.addrs)

			assert.Len(t, result, tt.expectedCount, tt.description)
		})
	}
}

func TestConnectToCachedPeers(t *testing.T) {
	tests := []struct {
		name        string
		setupPeers  func() []cachedPeer
		description string
	}{
		{
			name: "empty cached peers",
			setupPeers: func() []cachedPeer {
				return []cachedPeer{}
			},
			description: "should handle empty peer list without error",
		},
		{
			name: "nil cached peers",
			setupPeers: func() []cachedPeer {
				return nil
			},
			description: "should handle nil peer list without error",
		},
		{
			name: "invalid peer ID",
			setupPeers: func() []cachedPeer {
				return []cachedPeer{
					{
						ID:    "invalid-peer-id",
						Name:  "test",
						Addrs: []string{testLocalMultiaddr},
					},
				}
			},
			description: "should skip peers with invalid IDs",
		},
		{
			name: "empty addresses",
			setupPeers: func() []cachedPeer {
				return []cachedPeer{
					{
						ID:    "QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N",
						Name:  "test",
						Addrs: []string{},
					},
				}
			},
			description: "should skip peers with no addresses",
		},
		{
			name: "invalid multiaddrs",
			setupPeers: func() []cachedPeer {
				return []cachedPeer{
					{
						ID:    "QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N",
						Name:  "test",
						Addrs: []string{"invalid-multiaddr"},
					},
				}
			},
			description: "should skip peers with invalid multiaddrs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test host
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			h, err := libp2p.New(libp2p.Identity(privKey))
			require.NoError(t, err)
			defer func() {
				closeErr := h.Close()
				require.NoError(t, closeErr)
			}()

			ctx := context.Background()
			logger := &DefaultLogger{}
			peers := tt.setupPeers()

			// Should not panic
			connectToCachedPeers(ctx, h, peers, logger)
		})
	}
}

func TestConnectToDiscoveredPeer(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*testing.T, host.Host) peer.AddrInfo
		description string
	}{
		{
			name: "peer with no addresses",
			setupFunc: func(t *testing.T, _ host.Host) peer.AddrInfo {
				peerID := generateTestPeerID(t)
				return peer.AddrInfo{
					ID:    peerID,
					Addrs: nil,
				}
			},
			description: "should skip peers with no addresses",
		},
		{
			name: "self peer",
			setupFunc: func(_ *testing.T, h host.Host) peer.AddrInfo {
				return peer.AddrInfo{
					ID:    h.ID(),
					Addrs: nil,
				}
			},
			description: "should skip self peer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			config := Config{
				Name:       testPeerName,
				PrivateKey: privKey,
			}

			cl, err := NewClient(config)
			require.NoError(t, err)
			defer func() {
				closeErr := cl.Close()
				require.NoError(t, closeErr)
			}()

			c := cl.(*client)
			ctx := context.Background()

			peerInfo := tt.setupFunc(t, c.host)

			// Should not panic
			c.connectToDiscoveredPeer(ctx, peerInfo)
		})
	}
}

func TestShouldLogConnectionErrorAllCases(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := cl.Close()
		require.NoError(t, closeErr)
	}()

	c := cl.(*client)

	tests := []struct {
		name      string
		error     error
		shouldLog bool
	}{
		{
			name:      "connection refused",
			error:     &mockError{msg: "connection refused"},
			shouldLog: false,
		},
		{
			name:      "rate limit exceeded",
			error:     &mockError{msg: "rate limit exceeded"},
			shouldLog: false,
		},
		{
			name:      "NO_RESERVATION",
			error:     &mockError{msg: "NO_RESERVATION"},
			shouldLog: false,
		},
		{
			name:      "concurrent active dial",
			error:     &mockError{msg: "concurrent active dial"},
			shouldLog: false,
		},
		{
			name:      "all dials failed",
			error:     &mockError{msg: "all dials failed"},
			shouldLog: false,
		},
		{
			name:      "unknown error",
			error:     &mockError{msg: "some other error"},
			shouldLog: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.shouldLogConnectionError(tt.error)
			assert.Equal(t, tt.shouldLog, result)
		})
	}
}

func TestDiscoveryNotifeeHandlePeerFound(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*testing.T, host.Host) peer.AddrInfo
		description string
	}{
		{
			name: "self peer should be ignored",
			setupFunc: func(_ *testing.T, h host.Host) peer.AddrInfo {
				return peer.AddrInfo{
					ID:    h.ID(),
					Addrs: h.Addrs(),
				}
			},
			description: "should not connect to self",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test host
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			h, err := libp2p.New(libp2p.Identity(privKey))
			require.NoError(t, err)
			defer func() {
				closeErr := h.Close()
				require.NoError(t, closeErr)
			}()

			ctx := context.Background()
			logger := &DefaultLogger{}

			notifee := &discoveryNotifee{
				h:      h,
				ctx:    ctx,
				logger: logger,
			}

			peerInfo := tt.setupFunc(t, h)

			// Should not panic
			notifee.HandlePeerFound(peerInfo)

			// Give a moment for any async operations
			time.Sleep(50 * time.Millisecond)
		})
	}
}
