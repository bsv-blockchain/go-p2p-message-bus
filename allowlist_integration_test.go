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
		Name:                "peer-a",
		PrivateKey:          keyA,
		Port:                0,
		AllowPrivateIPs:     true,
		AllowedPublisherIDs: []string{idB.String()},
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

	// B's channel is drained in phase 2 as the positive control for C: B does
	// not filter, so if C's messages reach B then C is genuinely publishing
	// into a live mesh and A's non-receipt is a real result rather than an
	// artifact of C never having grafted.
	chB := clB.Subscribe(topicName)
	require.NotNil(t, chB)
	require.NotNil(t, clC.Subscribe(topicName))

	addrA := loopbackAddr(t, clA.(*client))
	addrB := loopbackAddr(t, clB.(*client))
	require.NoError(t, clB.Connect(clB.(*client).ctx, addrA))
	require.NoError(t, clC.Connect(clC.(*client).ctx, addrA))

	// C also peers with B directly. A ignores C's messages and therefore never
	// forwards them, so without this link there is no path by which B could
	// observe C and the control above would be unobservable.
	require.NoError(t, clC.Connect(clC.(*client).ctx, addrB))

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

	requireCStaysFilteredAtA(t, chA, chB, idC)
}

// requireCStaysFilteredAtA is phase 2 of TestAllowlistFiltersByAuthorNotByName:
// with the mesh proven live it keeps watching A and asserts C's messages never
// arrive there.
//
// B's channel is drained alongside A's as a positive control on C. Without it
// the phase would pass vacuously if C had failed to connect for some unrelated
// reason - and would keep passing with the validator deleted. B does not
// filter, so C's messages reaching B establishes that C is publishing into a
// live mesh and makes A's non-receipt a controlled negative.
//
// The observation ends once both hold: at least 5s of A seeing nothing from C,
// and B having seen C at least once. The second can take longer than the first
// if the C-B graft is slow, so it gets its own, longer deadline. Both deadlines
// are computed once, outside the loop: B publishes every 250ms, and recreating
// them per iteration would reset the window on every message, so the
// observation would never end.
func requireCStaysFilteredAtA(t *testing.T, chA, chB <-chan Message, idC peer.ID) {
	t.Helper()

	observationDeadline := time.After(5 * time.Second)
	controlDeadline := time.After(30 * time.Second)

	var observed, gotFromCAtB bool

	for !observed || !gotFromCAtB {
		select {
		case msg := <-chA:
			require.NotEqual(t, idC.String(), msg.FromID,
				"A must not receive messages authored by the non-allowlisted peer")
		case msg := <-chB:
			if msg.FromID == idC.String() {
				gotFromCAtB = true
			}
		case <-observationDeadline:
			observed = true
			observationDeadline = nil // a nil channel blocks forever: do not re-fire
		case <-controlDeadline:
			require.True(t, gotFromCAtB,
				"B, which does not filter, never received C's messages: C was not publishing into a live mesh, so A's non-receipt proves nothing")

			return
		}
	}
}
