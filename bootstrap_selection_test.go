package p2p

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSelectBootstrapPeers covers bootstrap selection outside test mode, which
// getBootstrapAndRelayPeers short-circuits under testing.Testing(). The
// DisableDefaultBootstrapPeers case matters most: without it an empty
// BootstrapPeers list silently joins the public IPFS network.
func TestSelectBootstrapPeers(t *testing.T) {
	log := &DefaultLogger{}

	t.Run("empty list falls back to IPFS defaults", func(t *testing.T) {
		peers := selectBootstrapPeers(Config{}, log, false)
		require.NotEmpty(t, peers)
	})

	t.Run("DisableDefaultBootstrapPeers with empty list yields none", func(t *testing.T) {
		peers := selectBootstrapPeers(Config{DisableDefaultBootstrapPeers: true}, log, false)
		require.Empty(t, peers)
		require.NotNil(t, peers)
	})

	t.Run("custom peers are used regardless of the flag", func(t *testing.T) {
		addr := "/ip4/127.0.0.1/tcp/9905/p2p/12D3KooWSoovh3vMWJpTMqC1Fj4X7Ywb58L2Wmaa968PG3oHrYjq"
		for _, disable := range []bool{false, true} {
			peers := selectBootstrapPeers(Config{BootstrapPeers: []string{addr}, DisableDefaultBootstrapPeers: disable}, log, false)
			require.Len(t, peers, 1)
		}
	})

	t.Run("test mode yields none", func(t *testing.T) {
		require.Empty(t, selectBootstrapPeers(Config{}, log, true))
	})
}
