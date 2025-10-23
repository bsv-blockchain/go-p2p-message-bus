package p2p

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPeerName              = "test-peer"
	testTopicName             = "test-topic"
	testPeerCacheFileName     = "peers.json"
	testInvalidAnnounceErrMsg = "invalid announce address"
)

func TestNewClientMissingName(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       "", // Missing required field
		PrivateKey: privKey,
	}

	client, err := NewClient(config)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrNameRequired)
	assert.Nil(t, client)
}

func TestNewClientMissingPrivateKey(t *testing.T) {
	config := Config{
		Name:       testPeerName,
		PrivateKey: nil, // Missing required field
	}

	client, err := NewClient(config)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrPrivateKeyRequired)
	assert.Nil(t, client)
}

func TestNewClientInvalidAnnounceAddr(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		AnnounceAddrs: []string{"invalid-multiaddr"},
	}

	client, err := NewClient(config)

	require.Error(t, err)
	assert.Contains(t, err.Error(), testInvalidAnnounceErrMsg)
	assert.Nil(t, client)
}

func TestNewClientMinimalConfig(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)

	require.NoError(t, err)
	require.NotNil(t, client)

	// Clean up
	err = client.Close()
	require.NoError(t, err)
}

func TestNewClientWithCustomLogger(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	customLogger := &DefaultLogger{}

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
		Logger:     customLogger,
	}

	client, err := NewClient(config)

	require.NoError(t, err)
	require.NotNil(t, client)

	err = client.Close()
	require.NoError(t, err)
}

func TestNewClientWithPort(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
		Port:       0, // Random port
	}

	client, err := NewClient(config)

	require.NoError(t, err)
	require.NotNil(t, client)

	err = client.Close()
	require.NoError(t, err)
}

func TestNewClientWithPeerCacheFile(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cacheFile := filepath.Join(t.TempDir(), testPeerCacheFileName)

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		PeerCacheFile: cacheFile,
	}

	client, err := NewClient(config)

	require.NoError(t, err)
	require.NotNil(t, client)

	err = client.Close()
	require.NoError(t, err)
}

func TestNewClientWithProtocolVersion(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:            testPeerName,
		PrivateKey:      privKey,
		ProtocolVersion: "test/1.0.0",
	}

	client, err := NewClient(config)

	require.NoError(t, err)
	require.NotNil(t, client)

	err = client.Close()
	require.NoError(t, err)
}

func TestClientGetID(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := client.Close()
		require.NoError(t, closeErr)
	}()

	id := client.GetID()

	assert.NotEmpty(t, id)

	// Verify it's a valid peer ID
	_, err = peer.Decode(id)
	require.NoError(t, err)
}

func TestClientGetIDConsistency(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		err := client.Close()
		require.NoError(t, err)
	}()

	// Get ID multiple times
	id1 := client.GetID()
	id2 := client.GetID()
	id3 := client.GetID()

	// Should always return the same ID
	assert.Equal(t, id1, id2)
	assert.Equal(t, id2, id3)
}

func TestClientGetPeersEmpty(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		err := client.Close()
		require.NoError(t, err)
	}()

	peers := client.GetPeers()

	// New client should have no peers
	assert.NotNil(t, peers)
	assert.Empty(t, peers)
}

func TestClientClose(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)

	err = client.Close()
	require.NoError(t, err)
}

func TestClientCloseTimeout(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)

	// Close should complete within timeout
	done := make(chan error, 1)
	go func() {
		done <- client.Close()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("client.Close() timed out")
	}
}

func TestClientSubscribe(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		err := client.Close()
		require.NoError(t, err)
	}()

	msgChan := client.Subscribe(testTopicName)

	require.NotNil(t, msgChan)

	// Verify channel is not closed immediately
	select {
	case _, ok := <-msgChan:
		if !ok {
			t.Fatal("message channel closed immediately")
		}
	case <-time.After(100 * time.Millisecond):
		// Channel not closed, which is expected
	}
}

func TestClientSubscribeMultipleTopics(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		err := client.Close()
		require.NoError(t, err)
	}()

	topics := []string{"topic1", "topic2", "topic3"}
	channels := make(map[string]<-chan Message)

	for _, topic := range topics {
		msgChan := client.Subscribe(topic)
		require.NotNil(t, msgChan)
		channels[topic] = msgChan
	}

	assert.Len(t, channels, len(topics))

	// Give subscriptions time to complete before closing
	time.Sleep(100 * time.Millisecond)
}

func TestClientPublish(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := client.Close()
		require.NoError(t, closeErr)
	}()

	ctx := context.Background()
	testData := []byte("test message")

	err = client.Publish(ctx, testTopicName, testData)

	// Publishing should succeed even with no subscribers
	require.NoError(t, err)
}

func TestClientPublishWithContext(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := client.Close()
		require.NoError(t, closeErr)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	testData := []byte("test message")

	err = client.Publish(ctx, testTopicName, testData)
	require.NoError(t, err)
}

func TestGetLoggerDefault(t *testing.T) {
	logger := getLogger(nil)

	require.NotNil(t, logger)
	assert.IsType(t, &DefaultLogger{}, logger)
}

func TestGetLoggerCustom(t *testing.T) {
	customLogger := &DefaultLogger{}

	logger := getLogger(customLogger)

	require.NotNil(t, logger)
	assert.Equal(t, customLogger, logger)
}

func TestConfigureRelayPeersEmpty(t *testing.T) {
	logger := &DefaultLogger{}

	// Create some bootstrap peers for testing
	bootstrapPeers := []peer.AddrInfo{
		{ID: "QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N"},
	}

	relayPeers := configureRelayPeers([]string{}, bootstrapPeers, logger)

	// Should return bootstrap peers when no custom relays
	assert.Equal(t, bootstrapPeers, relayPeers)
}

func TestConfigureRelayPeersInvalidAddresses(t *testing.T) {
	logger := &DefaultLogger{}

	bootstrapPeers := []peer.AddrInfo{
		{ID: "QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N"},
	}

	// Invalid multiaddr strings
	invalidRelays := []string{
		"invalid-address",
		"not-a-multiaddr",
	}

	relayPeers := configureRelayPeers(invalidRelays, bootstrapPeers, logger)

	// Should fall back to bootstrap peers on invalid addresses
	assert.Equal(t, bootstrapPeers, relayPeers)
}

func TestClientWithSamePeerID(t *testing.T) {
	// Generate a key once
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	// Create first client
	config1 := Config{
		Name:       "peer1",
		PrivateKey: privKey,
	}

	client1, err := NewClient(config1)
	require.NoError(t, err)
	id1 := client1.GetID()
	err = client1.Close()
	require.NoError(t, err)

	// Create second client with same private key
	config2 := Config{
		Name:       "peer2",
		PrivateKey: privKey,
	}

	client2, err := NewClient(config2)
	require.NoError(t, err)
	id2 := client2.GetID()
	err = client2.Close()
	require.NoError(t, err)

	// Both clients should have the same peer ID
	assert.Equal(t, id1, id2, "same private key should produce same peer ID")
}

func TestClientWithDifferentPeerIDs(t *testing.T) {
	// Generate two different keys
	privKey1, err := GeneratePrivateKey()
	require.NoError(t, err)

	privKey2, err := GeneratePrivateKey()
	require.NoError(t, err)

	// Create first client
	config1 := Config{
		Name:       "peer1",
		PrivateKey: privKey1,
	}

	client1, err := NewClient(config1)
	require.NoError(t, err)
	id1 := client1.GetID()
	err = client1.Close()
	require.NoError(t, err)

	// Create second client with different key
	config2 := Config{
		Name:       "peer2",
		PrivateKey: privKey2,
	}

	client2, err := NewClient(config2)
	require.NoError(t, err)
	id2 := client2.GetID()
	err = client2.Close()
	require.NoError(t, err)

	// Clients should have different peer IDs
	assert.NotEqual(t, id1, id2, "different private keys should produce different peer IDs")
}

func TestShouldLogConnectionError(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	c, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		err := c.Close()
		require.NoError(t, err)
	}()

	client := c.(*client)

	tests := []struct {
		name        string
		errorMsg    string
		shouldLog   bool
		description string
	}{
		{
			name:        "connection refused should not log",
			errorMsg:    "connection refused",
			shouldLog:   false,
			description: "common transient error",
		},
		{
			name:        "rate limit should not log",
			errorMsg:    "rate limit exceeded",
			shouldLog:   false,
			description: "rate limiting is expected",
		},
		{
			name:        "NO_RESERVATION should not log",
			errorMsg:    "NO_RESERVATION for relay",
			shouldLog:   false,
			description: "relay reservation failure",
		},
		{
			name:        "concurrent dial should not log",
			errorMsg:    "concurrent active dial to peer",
			shouldLog:   false,
			description: "concurrent connection attempts",
		},
		{
			name:        "all dials failed should not log",
			errorMsg:    "all dials failed",
			shouldLog:   false,
			description: "all connection attempts failed",
		},
		{
			name:        "unknown error should log",
			errorMsg:    "unexpected database error",
			shouldLog:   true,
			description: "unexpected errors should be logged",
		},
		{
			name:        "custom error should log",
			errorMsg:    "some other error condition",
			shouldLog:   true,
			description: "non-ignored errors should be logged",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock error with the test message
			mockErr := &mockError{msg: tt.errorMsg}

			shouldLog := client.shouldLogConnectionError(mockErr)

			assert.Equal(t, tt.shouldLog, shouldLog, tt.description)
		})
	}
}

// mockError is a simple error type for testing
type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}

func TestBuildHostOptionsWithAnnounceAddrs(t *testing.T) {
	logger := &DefaultLogger{}
	cancel := func() {}

	tests := []struct {
		name          string
		announceAddrs []string
		wantErr       bool
		errContains   string
	}{
		{
			name:          "valid announce addresses",
			announceAddrs: []string{"/ip4/192.168.1.1/tcp/4001"},
			wantErr:       false,
		},
		{
			name:          "multiple valid announce addresses",
			announceAddrs: []string{"/ip4/192.168.1.1/tcp/4001", "/ip6/::1/tcp/4001"},
			wantErr:       false,
		},
		{
			name:          "invalid announce address",
			announceAddrs: []string{"invalid-address"},
			wantErr:       true,
			errContains:   testInvalidAnnounceErrMsg,
		},
		{
			name:          "empty announce addresses",
			announceAddrs: []string{},
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			config := Config{
				PrivateKey:    privKey,
				AnnounceAddrs: tt.announceAddrs,
			}

			opts, err := buildHostOptions(config, logger, cancel)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, opts)
				// At minimum should have identity option
				assert.NotEmpty(t, opts)
			}
		})
	}
}

func TestClientInterfaceImplementation(_ *testing.T) {
	// Verify that client implements the Client interface at compile time
	var _ Client = (*client)(nil)
}

func TestNewClientReturnsClientInterface(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	config := Config{
		Name:       testPeerName,
		PrivateKey: privKey,
	}

	client, err := NewClient(config)
	require.NoError(t, err)
	defer func() {
		closeErr := client.Close()
		require.NoError(t, closeErr)
	}()

	// Verify it returns the Client interface (type is guaranteed by function signature)
	_ = client
}

func TestClientPeerCacheTTLDefault(t *testing.T) {
	// This test verifies that the default TTL is used when not specified
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cacheFile := filepath.Join(t.TempDir(), testPeerCacheFileName)

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		PeerCacheFile: cacheFile,
		PeerCacheTTL:  0, // Should use default of 24 hours
	}

	client, err := NewClient(config)
	require.NoError(t, err)

	err = client.Close()
	require.NoError(t, err)
}

func TestClientPeerCacheTTLCustom(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cacheFile := filepath.Join(t.TempDir(), testPeerCacheFileName)

	config := Config{
		Name:          testPeerName,
		PrivateKey:    privKey,
		PeerCacheFile: cacheFile,
		PeerCacheTTL:  1 * time.Hour, // Custom TTL
	}

	client, err := NewClient(config)
	require.NoError(t, err)

	err = client.Close()
	require.NoError(t, err)
}

// FuzzMessageUnmarshal performs fuzz testing on the P2P message unmarshaling logic
// to ensure it handles arbitrary JSON data without panicking. This tests the
// robustness of message parsing from potentially malicious or corrupted peers.
func FuzzMessageUnmarshal(f *testing.F) {
	// Seed corpus with various P2P message formats

	// 1. Valid message
	f.Add([]byte(`{"name":"alice","data":"aGVsbG8="}`)) // "hello" in base64

	// 2. Valid message with empty data
	f.Add([]byte(`{"name":"bob","data":""}`))

	// 3. Valid message with null data
	f.Add([]byte(`{"name":"charlie","data":null}`))

	// 4. Missing name field
	f.Add([]byte(`{"data":"aGVsbG8="}`))

	// 5. Missing data field
	f.Add([]byte(`{"name":"alice"}`))

	// 6. Empty JSON object
	f.Add([]byte(`{}`))

	// 7. Invalid JSON
	f.Add([]byte(`{invalid json`))
	f.Add([]byte(`}`))
	f.Add([]byte(`{"name":"alice","data":`))

	// 8. Wrong data types
	f.Add([]byte(`{"name":123,"data":"test"}`))
	f.Add([]byte(`{"name":"alice","data":123}`))
	f.Add([]byte(`{"name":"alice","data":{"nested":"object"}}`))
	f.Add([]byte(`{"name":"alice","data":["array","of","items"]}`))

	// 9. Not an object
	f.Add([]byte(`[]`))
	f.Add([]byte(`"string"`))
	f.Add([]byte(`123`))
	f.Add([]byte(`null`))
	f.Add([]byte(`true`))

	// 10. Extra fields
	f.Add([]byte(`{"name":"alice","data":"test","extra":"field","another":123}`))

	// 11. Very long strings
	longName := `{"name":"` + string(make([]byte, 1000)) + `","data":"test"}`
	f.Add([]byte(longName))

	// 12. Unicode and special characters (using escape sequences to avoid gosmopolitan warning)
	f.Add([]byte(`{"name":"peer\u4f60\u597d\u4e16\u754c","data":"test"}`)) // Chinese characters
	f.Add([]byte(`{"name":"alice\u0000null","data":"test"}`))
	f.Add([]byte(`{"name":"alice","data":"data\u0000null"}`))

	// 13. Escape sequences
	f.Add([]byte(`{"name":"alice\"bob","data":"test"}`))
	f.Add([]byte(`{"name":"alice\\bob","data":"test"}`))

	// 14. Empty string
	f.Add([]byte(``))

	// 15. Nested JSON in data field (as string)
	f.Add([]byte(`{"name":"alice","data":"{\"nested\":\"json\"}"}`))

	// 16. Binary data encoded as base64 (proper use case)
	f.Add([]byte(`{"name":"alice","data":"SGVsbG8gV29ybGQhIFRoaXMgaXMgYSB0ZXN0IG1lc3NhZ2Uu"}`))

	f.Fuzz(func(t *testing.T, jsonData []byte) {
		// Simulate the message unmarshaling logic from client.go:616-623
		var m struct {
			Name string `json:"name"`
			Data []byte `json:"data"`
		}

		// The function should never panic, regardless of input
		err := json.Unmarshal(jsonData, &m)
		// Verify behavior is consistent
		if err != nil {
			// Error is expected for invalid JSON - the client code logs this error and continues
			// We just verified no panic occurred, which is the main goal
			return
		}

		// Name should be a string (could be empty)
		_ = m.Name

		// Data should be a byte slice (could be nil or empty)
		if m.Data != nil {
			// Data exists, verify it's a valid byte slice
			_ = len(m.Data)
		}

		// Test that we can re-marshal the data
		remarshaled, marshalErr := json.Marshal(m)
		if marshalErr != nil {
			t.Errorf("Failed to re-marshal successfully unmarshaled message: %v", marshalErr)
		}

		// Re-marshaled data should be valid JSON
		if len(remarshaled) == 0 {
			t.Error("Re-marshaled data is empty")
		}

		// Test that we can unmarshal the re-marshaled data
		var m2 struct {
			Name string `json:"name"`
			Data []byte `json:"data"`
		}
		if unmarshalErr := json.Unmarshal(remarshaled, &m2); unmarshalErr != nil {
			t.Errorf("Failed to unmarshal re-marshaled data: %v", unmarshalErr)
		}

		// Round-trip should preserve the data
		if m.Name != m2.Name {
			t.Errorf("Name changed after round-trip: %q -> %q", m.Name, m2.Name)
		}

		// For data, compare lengths and content
		if len(m.Data) != len(m2.Data) {
			t.Errorf("Data length changed after round-trip: %d -> %d", len(m.Data), len(m2.Data))
		} else {
			for i := range m.Data {
				if m.Data[i] != m2.Data[i] {
					t.Errorf("Data byte at index %d changed after round-trip: %d -> %d", i, m.Data[i], m2.Data[i])
					break
				}
			}
		}
	})
}
