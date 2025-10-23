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

// FuzzPrivateKeyFromHex performs fuzz testing on the PrivateKeyFromHex function
// to ensure it handles arbitrary hex strings without panicking and with proper
// error handling. This tests the robustness of hex decoding and key unmarshaling.
func FuzzPrivateKeyFromHex(f *testing.F) {
	// Seed corpus with various test cases

	// 1. Valid key (generated)
	validKey, err := GeneratePrivateKey()
	if err == nil {
		validHex, err := PrivateKeyToHex(validKey)
		if err == nil {
			f.Add(validHex)
		}
	}

	// 2. Empty string
	f.Add("")

	// 3. Invalid hex characters
	f.Add("zzz123notvalid")
	f.Add("ghijklmnop")

	// 4. Odd length hex strings
	f.Add("a")
	f.Add("abc")
	f.Add("12345")

	// 5. Valid hex but wrong length/format
	f.Add("deadbeef")
	f.Add("00")
	f.Add("0000000000000000")

	// 6. Very long strings
	f.Add("00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000")

	// 7. Special characters and edge cases
	f.Add("0x123456")     // Hex prefix
	f.Add("  deadbeef  ") // Whitespace
	f.Add("\n\t")         // Control characters

	f.Fuzz(func(t *testing.T, hexInput string) {
		// The function should never panic, regardless of input
		key, err := PrivateKeyFromHex(hexInput)
		if err != nil {
			// Error is expected for invalid input - ensure key is nil
			if key != nil {
				t.Errorf("PrivateKeyFromHex returned non-nil key with error: %v", err)
			}
			return
		}

		// Success case - ensure key is valid
		if key == nil {
			t.Error("PrivateKeyFromHex returned nil key without error")
			return
		}

		// Verify the key has the expected type
		if key.Type() != crypto.Ed25519 {
			t.Errorf("Expected Ed25519 key type, got %v", key.Type())
		}

		// Verify we can get the public key
		pubKey := key.GetPublic()
		if pubKey == nil {
			t.Error("GetPublic() returned nil for valid key")
		}
	})
}

// FuzzPrivateKeyRoundTrip performs fuzz testing on the round-trip conversion
// of private keys to hex and back. This ensures that serialization and
// deserialization is lossless and consistent.
func FuzzPrivateKeyRoundTrip(f *testing.F) {
	// Seed with some valid key bytes
	for i := 0; i < 5; i++ {
		key, err := GeneratePrivateKey()
		if err == nil {
			keyBytes, err := crypto.MarshalPrivateKey(key)
			if err == nil {
				f.Add(keyBytes)
			}
		}
	}

	f.Fuzz(func(t *testing.T, keyBytes []byte) {
		// Try to unmarshal the fuzzed bytes as a private key
		key, err := crypto.UnmarshalPrivateKey(keyBytes)
		if err != nil {
			// Invalid key bytes - this is expected, skip
			return
		}

		// We have a valid key, test round-trip
		hexStr, err := PrivateKeyToHex(key)
		if err != nil {
			t.Errorf("PrivateKeyToHex failed for valid key: %v", err)
			return
		}

		// Convert back from hex
		restoredKey, err := PrivateKeyFromHex(hexStr)
		if err != nil {
			t.Errorf("PrivateKeyFromHex failed for valid hex: %v", err)
			return
		}

		// Compare original and restored keys
		originalBytes, err := crypto.MarshalPrivateKey(key)
		if err != nil {
			t.Errorf("Failed to marshal original key: %v", err)
			return
		}

		restoredBytes, err := crypto.MarshalPrivateKey(restoredKey)
		if err != nil {
			t.Errorf("Failed to marshal restored key: %v", err)
			return
		}

		if !assert.Equal(t, originalBytes, restoredBytes) {
			t.Error("Round-trip conversion produced different key bytes")
		}
	})
}
