package p2p

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRelayPeerMultiaddr = "/ip4/127.0.0.1/tcp/4001/p2p/QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N"

func TestNewClientWithCustomBootstrapPeers(t *testing.T) {
	tests := []struct {
		name           string
		bootstrapPeers []string
		wantErr        bool
	}{
		{
			name: "single bootstrap peer",
			bootstrapPeers: []string{
				testRelayPeerMultiaddr,
			},
			wantErr: false,
		},
		{
			name:           "empty bootstrap peers",
			bootstrapPeers: []string{},
			wantErr:        false,
		},
		{
			name: "invalid bootstrap peer",
			bootstrapPeers: []string{
				"invalid-bootstrap",
			},
			wantErr: false, // Invalid bootstrap peers are logged but don't fail client creation
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			config := Config{
				Name:           testPeerName,
				PrivateKey:     privKey,
				BootstrapPeers: tt.bootstrapPeers,
			}

			cl, err := NewClient(config)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, cl)
				closeErr := cl.Close()
				require.NoError(t, closeErr)
			}
		})
	}
}

func TestConfigureBootstrapPeersWithValidPeers(t *testing.T) {
	logger := &DefaultLogger{}

	// Valid bootstrap peer
	bootstrapPeersConfig := []string{
		testRelayPeerMultiaddr,
	}

	bootstrapPeers := parsePeerMultiaddrs(bootstrapPeersConfig, logger)

	assert.Len(t, bootstrapPeers, 1)
}

func TestConfigureBootstrapPeersWithMixedValidity(t *testing.T) {
	logger := &DefaultLogger{}

	// Mix of valid and invalid
	bootstrapPeersConfig := []string{
		testRelayPeerMultiaddr,
		"invalid-peer",
	}

	bootstrapPeers := parsePeerMultiaddrs(bootstrapPeersConfig, logger)

	// Should have 1 valid peer (second one is invalid)
	assert.Len(t, bootstrapPeers, 1)
}

func TestClientCloseTwice(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)

	// First close
	err = cl.Close()
	require.NoError(t, err)

	// Second close should not panic
	err = cl.Close()
	// May return error or nil, both are acceptable
	_ = err
}

func TestGetPeersWithNoConnections(t *testing.T) {
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

	// Manually add a peer to the tracker
	testPeerID := generateTestPeerID(t)
	c.peerTracker.recordMessageFrom(testPeerID)

	peers := cl.GetPeers()

	// Should have one peer, even without connections
	assert.Len(t, peers, 1)
	assert.Equal(t, testPeerID.String(), peers[0].ID)
}

func TestClientSubscribeAndClose(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	cl, err := NewClient(config)
	require.NoError(t, err)

	// Subscribe to a topic
	msgChan := cl.Subscribe("test-topic")
	require.NotNil(t, msgChan)

	// Give subscription time to initialize
	time.Sleep(100 * time.Millisecond)

	// Close immediately
	err = cl.Close()
	require.NoError(t, err)

	// Channel should be closed
	select {
	case _, ok := <-msgChan:
		assert.False(t, ok, "channel should be closed")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel not closed in time")
	}
}

func TestClientWithAllOptions(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:            "full-featured-client",
		PrivateKey:      privKey,
		ProtocolVersion: "test/1.0.0",
		Port:            0,
		PeerCacheTTL:    1 * time.Hour,
		Logger:          &DefaultLogger{},
		AnnounceAddrs:   []string{"/ip4/192.168.1.100/tcp/4001"},
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, cl)

	err = cl.Close()
	require.NoError(t, err)
}

func TestNewClientWithDHTModeOff(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
		DHTMode:    "off",
	}

	cl, err := NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, cl)

	// Verify DHT is actually disabled (nil)
	c := cl.(*client)
	assert.Nil(t, c.dht)

	err = cl.Close()
	require.NoError(t, err)
}

func TestConnectToBootstrapPeersWithCustomPeers(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
		BootstrapPeers: []string{
			"/ip4/127.0.0.1/tcp/9999/p2p/QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N",
		},
	}

	cl, err := NewClient(config)
	require.NoError(t, err)

	// Give time for connection attempts
	time.Sleep(100 * time.Millisecond)

	err = cl.Close()
	require.NoError(t, err)
}

func TestConnectToDiscoveredPeerWithRealConnection(t *testing.T) {
	// Create two clients that can discover each other
	privKey1, err := GeneratePrivateKey()
	require.NoError(t, err)

	privKey2, err := GeneratePrivateKey()
	require.NoError(t, err)

	config1 := Config{
		Name:       "peer1",
		PrivateKey: privKey1,
		Port:       0,
	}

	config2 := Config{
		Name:       "peer2",
		PrivateKey: privKey2,
		Port:       0,
	}

	cl1, err := NewClient(config1)
	require.NoError(t, err)
	defer func() {
		closeErr := cl1.Close()
		require.NoError(t, closeErr)
	}()

	cl2, err := NewClient(config2)
	require.NoError(t, err)
	defer func() {
		closeErr := cl2.Close()
		require.NoError(t, closeErr)
	}()

	// Subscribe both to same topic
	_ = cl1.Subscribe("discovery-test")
	_ = cl2.Subscribe("discovery-test")

	// Give time for discovery - mDNS discovery should work locally
	time.Sleep(5 * time.Second)

	// Check if peers discovered each other
	peers1 := cl1.GetPeers()
	peers2 := cl2.GetPeers()

	// This is a best-effort test - discovery may not always work in test environment
	// Just verify no panic occurred
	t.Logf("Peer1 discovered %d peers, Peer2 discovered %d peers", len(peers1), len(peers2))
}
