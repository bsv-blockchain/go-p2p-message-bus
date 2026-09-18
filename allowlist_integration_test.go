package p2p

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
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

// requireCStaysFilteredAtA is phase 2 of TestAllowlistFiltersByAuthorNotByName
// and of TestAllowlistFiltersForwardedAuthor: with the mesh proven live it
// keeps watching A and asserts C's messages never arrive there.
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

// newLineTopologyClient builds one node of TestAllowlistFiltersForwardedAuthor
// and registers its shutdown.
//
// DHT is off for every node in that test, and deliberately so: with the DHT in
// its default server mode the nodes advertise the topic to each other and
// A would discover C through B's provider records and dial it directly,
// collapsing the line into a full mesh and destroying the forwarding hop the
// test exists to exercise. With the DHT off, the only address-discovery path
// left is attemptDirectConnectionsToTopicPeers, which walks peerTracker's topic
// peers - and A never records C there, because peerTracker is only written from
// receiveMessages, which C's filtered messages never reach.
func newLineTopologyClient(t *testing.T, name string, key crypto.PrivKey, allowedPublishers []string, log logger) Client {
	t.Helper()

	cl, err := NewClient(Config{
		Name:                name,
		PrivateKey:          key,
		Port:                0,
		AllowPrivateIPs:     true,
		DHTMode:             "off",
		AllowedPublisherIDs: allowedPublishers,
		Logger:              log,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, cl.Close())
	})

	return cl
}

// TestAllowlistFiltersForwardedAuthor pins the author-not-sender property end
// to end, over a line topology where the two genuinely differ:
//
//	A <-> B <-> C
//
// A allowlists B only and is connected only to B. C, which A does not allow,
// publishes; B allowlists nobody, so B accepts C's messages and forwards them
// into the mesh, which is how they reach A - carried by an allowed peer. A must
// still drop them.
//
// This is the case TestAllowlistFiltersByAuthorNotByName cannot reach: there A
// is linked to both B and C, so the propagation source of C's messages at A is
// C itself, and a validator filtering on the source instead of the author would
// behave identically. Here it would not: filtering on the source would accept
// everything B forwards, C's traffic included.
func TestAllowlistFiltersForwardedAuthor(t *testing.T) {
	const topicName = "allowlist-forwarded-author-test"

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

	// A's log is read at the end as the second positive control: it is the only
	// place from which the test can observe that A's validator actually saw, and
	// dropped, C's messages.
	logA := &captureLogger{}

	clA := newLineTopologyClient(t, "peer-a", keyA, []string{idB.String()}, logA)
	clB := newLineTopologyClient(t, "peer-b", keyB, nil, &captureLogger{})
	clC := newLineTopologyClient(t, "peer-c", keyC, nil, &captureLogger{})

	chA := clA.Subscribe(topicName)
	require.NotNil(t, chA)

	// B's channel is the positive control, as in TestAllowlistFiltersByAuthorNotByName:
	// B does not filter, so C's messages arriving at B prove C is publishing into
	// a live mesh and make A's non-receipt a controlled negative.
	chB := clB.Subscribe(topicName)
	require.NotNil(t, chB)
	require.NotNil(t, clC.Subscribe(topicName))

	// The line: B dials A, C dials B. Nothing dials A to C.
	addrA := loopbackAddr(t, clA.(*client))
	addrB := loopbackAddr(t, clB.(*client))
	require.NoError(t, clB.Connect(clB.(*client).ctx, addrA))
	require.NoError(t, clC.Connect(clC.(*client).ctx, addrB))

	requireNotConnected(t, clA.(*client), idC, "before publishing")

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

	// Phase 1: wait for one of B's messages, so the mesh is proven live before
	// anything is concluded from A's silence. Anything from C fails immediately.
	deadline := time.After(20 * time.Second)

	var gotFromB bool

	for !gotFromB {
		select {
		case msg := <-chA:
			require.NotEqual(t, idC.String(), msg.FromID,
				"A must not receive messages authored by C, whoever forwarded them")
			require.Equal(t, idB.String(), msg.FromID)

			gotFromB = true
		case <-deadline:
			t.Fatal("A never received a message from the allowlisted peer; the mesh did not form")
		}
	}

	requireCStaysFilteredAtA(t, chA, chB, idC)

	// A's silence about C must be the validator's doing, not the absence of any
	// C traffic at A: without this the test would still pass with the validator
	// deleted if B simply never forwarded to A. Only C is unlisted here, so the
	// aggregate drop line can only have been caused by one of C's messages
	// arriving from B and being filtered on its author.
	//
	// Polled rather than asserted outright: the observation above ends as soon as
	// B has seen C once, which can be the same instant B forwards that message on
	// to A. The wait only ever makes a genuine failure slower, never a failure
	// pass.
	require.Eventually(t, func() bool {
		return strings.Contains(logA.String(), "from non-allowlisted peers")
	}, 15*time.Second, 50*time.Millisecond,
		"A never reported dropping a message: C's messages never reached A's validator, so A's non-receipt of them proves nothing")

	// The line must have held for the whole observation: had A picked up a direct
	// link to C, the propagation source of C's messages at A would have been C
	// itself and the forwarding hop would no longer have been under test.
	requireNotConnected(t, clA.(*client), idC, "after the observation window")
}

// requireNotConnected asserts that c has no connection to other, which is what
// makes the line topology a line.
func requireNotConnected(t *testing.T, c *client, other peer.ID, when string) {
	t.Helper()

	require.NotEqual(t, network.Connected, c.host.Network().Connectedness(other),
		"A must reach C only through B, but they were directly connected %s: the forwarding hop was not exercised", when)
}
