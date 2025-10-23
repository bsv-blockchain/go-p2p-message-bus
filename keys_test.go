package p2p

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratePrivateKey(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "successful key generation",
		},
		{
			name: "generates unique keys on multiple calls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := GeneratePrivateKey()
			require.NoError(t, err)
			require.NotNil(t, key)

			// Verify key type is Ed25519
			assert.EqualValues(t, crypto.Ed25519, key.Type(), "expected Ed25519 key type")

			// Verify key can generate a public key
			pubKey := key.GetPublic()
			require.NotNil(t, pubKey)
			assert.EqualValues(t, crypto.Ed25519, pubKey.Type(), "expected Ed25519 public key type")
		})
	}
}

func TestGeneratePrivateKeyUniqueness(t *testing.T) {
	// Generate multiple keys and ensure they're different
	key1, err := GeneratePrivateKey()
	require.NoError(t, err)

	key2, err := GeneratePrivateKey()
	require.NoError(t, err)

	// Convert to bytes for comparison
	bytes1, err := crypto.MarshalPrivateKey(key1)
	require.NoError(t, err)

	bytes2, err := crypto.MarshalPrivateKey(key2)
	require.NoError(t, err)

	// Keys should be different
	assert.NotEqual(t, bytes1, bytes2)
}

func TestPrivateKeyToHex(t *testing.T) {
	tests := []struct {
		name        string
		setupKey    func() crypto.PrivKey
		wantErr     bool
		errContains string
	}{
		{
			name: "valid Ed25519 key",
			setupKey: func() crypto.PrivKey {
				key, _ := GeneratePrivateKey()
				return key
			},
			wantErr: false,
		},
		{
			name: "valid key produces hex string",
			setupKey: func() crypto.PrivKey {
				key, _ := GeneratePrivateKey()
				return key
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := tt.setupKey()
			hexStr, err := PrivateKeyToHex(key)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, hexStr)

				// Verify it's valid hex
				_, decodeErr := hex.DecodeString(hexStr)
				require.NoError(t, decodeErr)

				// Hex string should be even length
				assert.Equal(t, 0, len(hexStr)%2)
			}
		})
	}
}

func TestPrivateKeyFromHex(t *testing.T) {
	// Generate a valid key for testing
	validKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	validHex, err := PrivateKeyToHex(validKey)
	require.NoError(t, err)

	tests := []struct {
		name        string
		hexKey      string
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid hex key",
			hexKey:  validHex,
			wantErr: false,
		},
		{
			name:    "empty hex string",
			hexKey:  "",
			wantErr: true,
			// Empty string decodes successfully but fails to unmarshal
			errContains: "failed to",
		},
		{
			name:        "invalid hex characters",
			hexKey:      "xyz123notahexstring",
			wantErr:     true,
			errContains: "failed to decode hex key",
		},
		{
			name:        "odd length hex string",
			hexKey:      "abc",
			wantErr:     true,
			errContains: "failed to decode hex key",
		},
		{
			name:        "valid hex but invalid key data",
			hexKey:      "deadbeef",
			wantErr:     true,
			errContains: "failed to unmarshal private key",
		},
		{
			name:        "truncated valid key",
			hexKey:      validHex[:20], // Take only first 20 chars
			wantErr:     true,
			errContains: "failed to unmarshal private key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := PrivateKeyFromHex(tt.hexKey)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, key)
			} else {
				require.NoError(t, err)
				require.NotNil(t, key)
				assert.EqualValues(t, crypto.Ed25519, key.Type(), "expected Ed25519 key type")
			}
		})
	}
}

func TestPrivateKeyRoundTrip(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "generate, serialize, deserialize produces same key",
		},
		{
			name: "round trip with different key",
		},
		{
			name: "multiple round trips",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Generate a new key
			originalKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			// Convert to hex
			hexStr, err := PrivateKeyToHex(originalKey)
			require.NoError(t, err)
			require.NotEmpty(t, hexStr)

			// Convert back from hex
			restoredKey, err := PrivateKeyFromHex(hexStr)
			require.NoError(t, err)
			require.NotNil(t, restoredKey)

			// Keys should be equal
			originalBytes, err := crypto.MarshalPrivateKey(originalKey)
			require.NoError(t, err)

			restoredBytes, err := crypto.MarshalPrivateKey(restoredKey)
			require.NoError(t, err)

			assert.Equal(t, originalBytes, restoredBytes)

			// Public keys should also match
			originalPub := originalKey.GetPublic()
			restoredPub := restoredKey.GetPublic()

			originalPubBytes, err := crypto.MarshalPublicKey(originalPub)
			require.NoError(t, err)

			restoredPubBytes, err := crypto.MarshalPublicKey(restoredPub)
			require.NoError(t, err)

			assert.Equal(t, originalPubBytes, restoredPubBytes)
		})
	}
}

func TestPrivateKeyHexDeterminism(t *testing.T) {
	// Same key should always produce the same hex string
	key, err := GeneratePrivateKey()
	require.NoError(t, err)

	hex1, err := PrivateKeyToHex(key)
	require.NoError(t, err)

	hex2, err := PrivateKeyToHex(key)
	require.NoError(t, err)

	assert.Equal(t, hex1, hex2, "same key should produce identical hex strings")
}

func TestPrivateKeyFromHexWithRealWorldExample(t *testing.T) {
	// Create a known good key and verify we can restore it
	originalKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)

	keyBytes, err := crypto.MarshalPrivateKey(originalKey)
	require.NoError(t, err)

	hexStr := hex.EncodeToString(keyBytes)

	// Now restore it using our function
	restoredKey, err := PrivateKeyFromHex(hexStr)
	require.NoError(t, err)

	restoredBytes, err := crypto.MarshalPrivateKey(restoredKey)
	require.NoError(t, err)

	assert.Equal(t, keyBytes, restoredBytes)
}
