package p2p

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

// loopbackAddr returns a dialable multiaddr for c, preferring 127.0.0.1 so the
// test does not depend on whatever other interfaces the host happens to have.
func loopbackAddr(t *testing.T, c *client) string {
	t.Helper()

	addrs := c.host.Addrs()
	require.NotEmpty(t, addrs, "client should have listen addresses")

	base := addrs[0]

	for _, a := range addrs {
		if strings.Contains(a.String(), "127.0.0.1") {
			base = a

			break
		}
	}

	return fmt.Sprintf("%s/p2p/%s", base, c.host.ID())
}

// TestAllowlistFiltersByAuthorNotByName runs three nodes on one topic. Node A
// allowlists only node B. Node C is not allowlisted and publishes under B's
// name, so the only thing distinguishing them is the authenticated peer ID.
// A must receive B's messages and never C's.
func TestAllowlistFiltersByAuthorNotByName(t *testing.T) {
	const topicName = "allowlist-filter-test"

	keyA, err := GeneratePrivateKey()
	require.NoError(t, err)

	keyB, err := GeneratePrivateKey()
	require.NoError(t, err)

	keyC, err := GeneratePrivateKey()
	require.NoError(t, err)

	idB, err := peer.IDFromPrivateKey(keyB)
	require.NoError(t, err)

	idC, err := peer.IDFromPrivateKey(keyC)
	require.NoError(t, err)

	clA, err := NewClient(Config{
		Name:            "peer-a",
		PrivateKey:      keyA,
		Port:            0,
		AllowPrivateIPs: true,
		AllowedPeerIDs:  []string{idB.String()},
	})
	require.NoError(t, err)

	defer func() {
		require.NoError(t, clA.Close())
	}()

	clB, err := NewClient(Config{
		Name:            "peer-b",
		PrivateKey:      keyB,
		Port:            0,
		AllowPrivateIPs: true,
	})
	require.NoError(t, err)

	defer func() {
		require.NoError(t, clB.Close())
	}()

	// Same Name as B on purpose: the envelope name is peer-controlled and must
	// not influence filtering.
	clC, err := NewClient(Config{
		Name:            "peer-b",
		PrivateKey:      keyC,
		Port:            0,
		AllowPrivateIPs: true,
	})
	require.NoError(t, err)

	defer func() {
		require.NoError(t, clC.Close())
	}()

	chA := clA.Subscribe(topicName)
	require.NotNil(t, chA)
	require.NotNil(t, clB.Subscribe(topicName))
	require.NotNil(t, clC.Subscribe(topicName))

	addrA := loopbackAddr(t, clA.(*client))
	require.NoError(t, clB.Connect(clB.(*client).ctx, addrA))
	require.NoError(t, clC.Connect(clC.(*client).ctx, addrA))

	// Publish repeatedly in the background: GossipSub mesh formation is not
	// instantaneous and a single publish issued before the graft is simply lost.
	stop := make(chan struct{})
	defer close(stop)

	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = clB.Publish(context.Background(), topicName, []byte("from-b"))
				_ = clC.Publish(context.Background(), topicName, []byte("from-c"))
			}
		}
	}()

	// Phase 1: wait for one of B's messages. Anything from C fails immediately.
	deadline := time.After(20 * time.Second)

	var gotFromB bool

	for !gotFromB {
		select {
		case msg := <-chA:
			require.NotEqual(t, idC.String(), msg.FromID,
				"A must not receive messages authored by the non-allowlisted peer")
			require.Equal(t, idB.String(), msg.FromID)

			gotFromB = true
		case <-deadline:
			t.Fatal("A never received a message from the allowlisted peer; the mesh did not form")
		}
	}

	// Phase 2: the mesh is proven live, so keep watching for a while and confirm
	// C stays filtered. The deadline is computed once, outside the loop: B keeps
	// publishing every 250ms, and recreating time.After per iteration would keep
	// resetting the window on every message from B, so the observation would
	// never end.
	observationDeadline := time.After(5 * time.Second)

	for {
		select {
		case msg := <-chA:
			require.NotEqual(t, idC.String(), msg.FromID,
				"A must not receive messages authored by the non-allowlisted peer")
		case <-observationDeadline:
			return
		}
	}
}
