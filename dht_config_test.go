package p2p

import (
	"bytes"
	"log"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDHTModeDefault verifies that the default DHT mode is server
func TestDHTModeDefault(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	// Capture log output to verify mode selection
	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOutput)

	config := Config{
		Name:       "test-dht-default",
		PrivateKey: privKey,
		// DHTMode not specified - should default to server
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	// Verify server mode was selected
	output := buf.String()
	assert.Contains(t, output, "DHT mode: server")
	assert.Contains(t, output, "will advertise and store provider records")
}

// TestDHTModeServer verifies explicit server mode configuration
func TestDHTModeServer(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOutput)

	config := Config{
		Name:       "test-dht-server",
		PrivateKey: privKey,
		DHTMode:    "server",
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	// Verify server mode was selected
	output := buf.String()
	assert.Contains(t, output, "DHT mode: server")
}

// TestDHTModeClient verifies client mode configuration
func TestDHTModeClient(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOutput)

	config := Config{
		Name:       "test-dht-client",
		PrivateKey: privKey,
		DHTMode:    "client",
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	// Verify client mode was selected
	output := buf.String()
	assert.Contains(t, output, "DHT mode: client")
	assert.Contains(t, output, "query-only, no provider storage")
}

// TestDHTCleanupIntervalDefault verifies default cleanup interval is not logged when not specified
func TestDHTCleanupIntervalDefault(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOutput)

	config := Config{
		Name:       "test-cleanup-default",
		PrivateKey: privKey,
		DHTMode:    "server",
		// DHTCleanupInterval not specified - should use libp2p default
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	// Verify cleanup interval configuration is NOT logged (uses libp2p default)
	output := buf.String()
	assert.NotContains(t, output, "Configuring DHT cleanup interval")
}

// TestDHTCleanupIntervalCustom verifies custom cleanup interval configuration
func TestDHTCleanupIntervalCustom(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOutput)

	customInterval := 6 * time.Hour
	config := Config{
		Name:               "test-cleanup-custom",
		PrivateKey:         privKey,
		DHTMode:            "server",
		DHTCleanupInterval: customInterval,
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	// Verify cleanup interval configuration was logged
	output := buf.String()
	assert.Contains(t, output, "Configuring DHT cleanup interval: 6h0m0s")
}

// TestDHTCleanupIntervalIgnoredInClientMode verifies cleanup interval is ignored in client mode
func TestDHTCleanupIntervalIgnoredInClientMode(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOutput)

	config := Config{
		Name:               "test-cleanup-client-mode",
		PrivateKey:         privKey,
		DHTMode:            "client",
		DHTCleanupInterval: 24 * time.Hour, // Should be ignored
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	// Verify client mode was selected
	output := buf.String()
	assert.Contains(t, output, "DHT mode: client")

	// Verify cleanup interval was NOT configured (client mode doesn't need it)
	assert.NotContains(t, output, "Configuring DHT cleanup interval")
}

// TestDHTModeServerWithVariousCleanupIntervals tests server mode with different intervals
func TestDHTModeServerWithVariousCleanupIntervals(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		expected string
	}{
		{
			name:     "1 hour cleanup",
			interval: 1 * time.Hour,
			expected: "1h0m0s",
		},
		{
			name:     "24 hour cleanup (default recommended)",
			interval: 24 * time.Hour,
			expected: "24h0m0s",
		},
		{
			name:     "72 hour cleanup",
			interval: 72 * time.Hour,
			expected: "72h0m0s",
		},
		{
			name:     "30 minute cleanup",
			interval: 30 * time.Minute,
			expected: "30m0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			var buf bytes.Buffer
			oldOutput := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(oldOutput)

			config := Config{
				Name:               "test-" + tt.name,
				PrivateKey:         privKey,
				DHTMode:            "server",
				DHTCleanupInterval: tt.interval,
			}

			client, err := NewClient(config)
			require.NoError(t, err)
			require.NotNil(t, client)
			defer client.Close()

			// Verify cleanup interval was configured correctly
			output := buf.String()
			assert.Contains(t, output, "Configuring DHT cleanup interval: "+tt.expected)
		})
	}
}

// TestDHTConfigurationCombinations tests various DHT configuration combinations
func TestDHTConfigurationCombinations(t *testing.T) {
	tests := []struct {
		name                        string
		dhtMode                     string
		cleanupInterval             time.Duration
		expectServerMode            bool
		expectCleanupConfig         bool
		expectedLogServerMessage    string
		expectedLogClientMessage    string
		expectedCleanupIntervalMsg  string
	}{
		{
			name:                     "default configuration (empty mode)",
			dhtMode:                  "",
			cleanupInterval:          0,
			expectServerMode:         true,
			expectCleanupConfig:      false,
			expectedLogServerMessage: "DHT mode: server",
		},
		{
			name:                     "server mode with custom cleanup",
			dhtMode:                  "server",
			cleanupInterval:          48 * time.Hour,
			expectServerMode:         true,
			expectCleanupConfig:      true,
			expectedLogServerMessage: "DHT mode: server",
			expectedCleanupIntervalMsg: "48h0m0s",
		},
		{
			name:                     "client mode with cleanup specified (ignored)",
			dhtMode:                  "client",
			cleanupInterval:          24 * time.Hour,
			expectServerMode:         false,
			expectCleanupConfig:      false,
			expectedLogClientMessage: "DHT mode: client",
		},
		{
			name:                     "server mode without cleanup (uses default)",
			dhtMode:                  "server",
			cleanupInterval:          0,
			expectServerMode:         true,
			expectCleanupConfig:      false,
			expectedLogServerMessage: "DHT mode: server",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			var buf bytes.Buffer
			oldOutput := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(oldOutput)

			config := Config{
				Name:               "test-combo-" + tt.name,
				PrivateKey:         privKey,
				DHTMode:            tt.dhtMode,
				DHTCleanupInterval: tt.cleanupInterval,
			}

			client, err := NewClient(config)
			require.NoError(t, err, "client creation should succeed")
			require.NotNil(t, client, "client should not be nil")
			defer client.Close()

			output := buf.String()

			// Verify mode logging
			if tt.expectServerMode {
				assert.Contains(t, output, tt.expectedLogServerMessage)
			} else {
				assert.Contains(t, output, tt.expectedLogClientMessage)
			}

			// Verify cleanup interval logging
			if tt.expectCleanupConfig {
				assert.Contains(t, output, "Configuring DHT cleanup interval")
				if tt.expectedCleanupIntervalMsg != "" {
					assert.Contains(t, output, tt.expectedCleanupIntervalMsg)
				}
			} else {
				assert.NotContains(t, output, "Configuring DHT cleanup interval")
			}
		})
	}
}

// TestDHTClientCanQuery verifies that client mode client can be created
func TestDHTClientCanQuery(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOutput)

	config := Config{
		Name:       "test-client-query",
		PrivateKey: privKey,
		DHTMode:    "client",
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	// Verify client mode was selected (basic functional check)
	output := buf.String()
	assert.Contains(t, output, "DHT mode: client")

	// Verify client has basic functionality
	peerID := client.GetID()
	assert.NotEmpty(t, peerID, "Client should have a peer ID")
}

// TestP2PClientTypeAlias verifies backward compatibility type alias
func TestP2PClientTypeAlias(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       "test-type-alias",
		PrivateKey: privKey,
	}

	// This should compile due to type alias
	var p2pClient P2PClient
	p2pClient, err = NewClient(config)
	require.NoError(t, err)
	require.NotNil(t, p2pClient)
	defer p2pClient.Close()

	// Verify it's also a Client
	var client Client = p2pClient
	assert.NotNil(t, client)
}

// BenchmarkDHTServerMode benchmarks client creation with server mode
func BenchmarkDHTServerMode(b *testing.B) {
	privKey, _ := GeneratePrivateKey()
	config := Config{
		Name:               "bench-server",
		PrivateKey:         privKey,
		DHTMode:            "server",
		DHTCleanupInterval: 24 * time.Hour,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client, _ := NewClient(config)
		if client != nil {
			client.Close()
		}
	}
}

// BenchmarkDHTClientMode benchmarks client creation with client mode
func BenchmarkDHTClientMode(b *testing.B) {
	privKey, _ := GeneratePrivateKey()
	config := Config{
		Name:       "bench-client",
		PrivateKey: privKey,
		DHTMode:    "client",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client, _ := NewClient(config)
		if client != nil {
			client.Close()
		}
	}
}
