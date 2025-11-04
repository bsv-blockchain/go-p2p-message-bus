package p2p

import (
	"bytes"
	"log"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test log message constants to avoid duplication
const (
	logMsgDHTModeServer              = "DHT mode: server"
	logMsgDHTModeClient              = "DHT mode: client"
	logMsgConfiguringCleanupInterval = "Configuring DHT cleanup interval"
)

// testClientConfig is a helper to create a test client with log capture
type testClientConfig struct {
	name            string
	dhtMode         string
	cleanupInterval time.Duration
}

// createTestClient is a helper function that reduces duplication in client creation and log setup
func createTestClient(t *testing.T, cfg testClientConfig) (Client, string) {
	t.Helper()

	privKey, err := GeneratePrivateKey()
	require.NoError(t, err, "failed to generate private key")

	// Capture log output
	var buf bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOutput) })

	config := Config{
		Name:               cfg.name,
		PrivateKey:         privKey,
		DHTMode:            cfg.dhtMode,
		DHTCleanupInterval: cfg.cleanupInterval,
	}

	client, err := NewClient(config)
	require.NoError(t, err, "failed to create client")
	require.NotNil(t, client, "client should not be nil")
	t.Cleanup(func() { _ = client.Close() })

	return client, buf.String()
}

// TestDHTModeSelection tests DHT mode configuration (server/client/default)
func TestDHTModeSelection(t *testing.T) {
	tests := []struct {
		name             string
		dhtMode          string
		expectedLogMsg   string
		additionalMsgOpt string // optional additional message to check
	}{
		{
			name:             "default mode (empty string defaults to server)",
			dhtMode:          "",
			expectedLogMsg:   logMsgDHTModeServer,
			additionalMsgOpt: "will advertise and store provider records",
		},
		{
			name:             "explicit server mode",
			dhtMode:          "server",
			expectedLogMsg:   logMsgDHTModeServer,
			additionalMsgOpt: "will advertise and store provider records",
		},
		{
			name:             "client mode",
			dhtMode:          "client",
			expectedLogMsg:   logMsgDHTModeClient,
			additionalMsgOpt: "query-only, no provider storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, output := createTestClient(t, testClientConfig{
				name:    "test-mode-" + tt.name,
				dhtMode: tt.dhtMode,
			})

			assert.Contains(t, output, tt.expectedLogMsg)
			if tt.additionalMsgOpt != "" {
				assert.Contains(t, output, tt.additionalMsgOpt)
			}
		})
	}
}

// TestDHTCleanupIntervalConfiguration tests cleanup interval settings
func TestDHTCleanupIntervalConfiguration(t *testing.T) {
	tests := []struct {
		name                   string
		dhtMode                string
		cleanupInterval        time.Duration
		expectCleanupConfigLog bool
		expectedIntervalString string
	}{
		{
			name:                   "server mode without cleanup interval (uses libp2p default)",
			dhtMode:                "server",
			cleanupInterval:        0,
			expectCleanupConfigLog: false,
		},
		{
			name:                   "server mode with 6 hour cleanup",
			dhtMode:                "server",
			cleanupInterval:        6 * time.Hour,
			expectCleanupConfigLog: true,
			expectedIntervalString: "6h0m0s",
		},
		{
			name:                   "server mode with 24 hour cleanup",
			dhtMode:                "server",
			cleanupInterval:        24 * time.Hour,
			expectCleanupConfigLog: true,
			expectedIntervalString: "24h0m0s",
		},
		{
			name:                   "server mode with 72 hour cleanup",
			dhtMode:                "server",
			cleanupInterval:        72 * time.Hour,
			expectCleanupConfigLog: true,
			expectedIntervalString: "72h0m0s",
		},
		{
			name:                   "server mode with 30 minute cleanup",
			dhtMode:                "server",
			cleanupInterval:        30 * time.Minute,
			expectCleanupConfigLog: true,
			expectedIntervalString: "30m0s",
		},
		{
			name:                   "client mode ignores cleanup interval",
			dhtMode:                "client",
			cleanupInterval:        24 * time.Hour,
			expectCleanupConfigLog: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, output := createTestClient(t, testClientConfig{
				name:            "test-cleanup-" + tt.name,
				dhtMode:         tt.dhtMode,
				cleanupInterval: tt.cleanupInterval,
			})

			if tt.expectCleanupConfigLog {
				assert.Contains(t, output, logMsgConfiguringCleanupInterval)
				if tt.expectedIntervalString != "" {
					assert.Contains(t, output, tt.expectedIntervalString)
				}
			} else {
				assert.NotContains(t, output, logMsgConfiguringCleanupInterval)
			}
		})
	}
}

// TestDHTConfigurationCombinations tests various configuration combinations
func TestDHTConfigurationCombinations(t *testing.T) {
	tests := []struct {
		name                   string
		dhtMode                string
		cleanupInterval        time.Duration
		expectedModeLog        string
		expectCleanupConfigLog bool
		expectedIntervalString string
	}{
		{
			name:                   "default configuration (empty mode, no interval)",
			dhtMode:                "",
			cleanupInterval:        0,
			expectedModeLog:        logMsgDHTModeServer,
			expectCleanupConfigLog: false,
		},
		{
			name:                   "server mode with 48h cleanup",
			dhtMode:                "server",
			cleanupInterval:        48 * time.Hour,
			expectedModeLog:        logMsgDHTModeServer,
			expectCleanupConfigLog: true,
			expectedIntervalString: "48h0m0s",
		},
		{
			name:                   "client mode with cleanup specified (ignored)",
			dhtMode:                "client",
			cleanupInterval:        24 * time.Hour,
			expectedModeLog:        logMsgDHTModeClient,
			expectCleanupConfigLog: false,
		},
		{
			name:                   "server mode without cleanup",
			dhtMode:                "server",
			cleanupInterval:        0,
			expectedModeLog:        logMsgDHTModeServer,
			expectCleanupConfigLog: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, output := createTestClient(t, testClientConfig{
				name:            "test-combo-" + tt.name,
				dhtMode:         tt.dhtMode,
				cleanupInterval: tt.cleanupInterval,
			})

			assert.Contains(t, output, tt.expectedModeLog, "mode log should be present")

			if tt.expectCleanupConfigLog {
				assert.Contains(t, output, logMsgConfiguringCleanupInterval, "cleanup config should be logged")
				if tt.expectedIntervalString != "" {
					assert.Contains(t, output, tt.expectedIntervalString, "interval string should match")
				}
			} else {
				assert.NotContains(t, output, logMsgConfiguringCleanupInterval, "cleanup config should not be logged")
			}
		})
	}
}

// TestDHTClientBasicFunctionality verifies client mode basic operations
func TestDHTClientBasicFunctionality(t *testing.T) {
	client, output := createTestClient(t, testClientConfig{
		name:    "test-client-functionality",
		dhtMode: "client",
	})

	// Verify client mode was selected
	assert.Contains(t, output, logMsgDHTModeClient)

	// Verify client has basic functionality
	peerID := client.GetID()
	assert.NotEmpty(t, peerID, "client should have a peer ID")
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
	defer func() { _ = p2pClient.Close() }()

	// Verify it's also a Client
	client := p2pClient
	assert.NotNil(t, client)
}

// BenchmarkDHTServerMode benchmarks client creation with server mode
func BenchmarkDHTServerMode(b *testing.B) {
	privKey, err := GeneratePrivateKey()
	if err != nil {
		b.Fatal(err)
	}

	config := Config{
		Name:               "bench-server",
		PrivateKey:         privKey,
		DHTMode:            "server",
		DHTCleanupInterval: 24 * time.Hour,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client, err := NewClient(config)
		if err != nil {
			b.Fatal(err)
		}
		if client != nil {
			_ = client.Close()
		}
	}
}

// BenchmarkDHTClientMode benchmarks client creation with client mode
func BenchmarkDHTClientMode(b *testing.B) {
	privKey, err := GeneratePrivateKey()
	if err != nil {
		b.Fatal(err)
	}

	config := Config{
		Name:       "bench-client",
		PrivateKey: privKey,
		DHTMode:    "client",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client, err := NewClient(config)
		if err != nil {
			b.Fatal(err)
		}
		if client != nil {
			_ = client.Close()
		}
	}
}
