package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

// fixedGater answers every interception with the same verdict and counts the
// calls it saw, so a test can tell which gaters a chain consulted.
type fixedGater struct {
	allow bool
	calls int
}

func (g *fixedGater) InterceptPeerDial(peer.ID) bool {
	g.calls++
	return g.allow
}

func (g *fixedGater) InterceptAddrDial(peer.ID, multiaddr.Multiaddr) bool {
	g.calls++
	return g.allow
}

func (g *fixedGater) InterceptAccept(network.ConnMultiaddrs) bool {
	g.calls++
	return g.allow
}

func (g *fixedGater) InterceptSecured(network.Direction, peer.ID, network.ConnMultiaddrs) bool {
	g.calls++
	return g.allow
}

func (g *fixedGater) InterceptUpgraded(network.Conn) (bool, control.DisconnectReason) {
	g.calls++
	return g.allow, 0
}

var _ connmgr.ConnectionGater = (*fixedGater)(nil)

// newGatedClient starts a client on a random port with the given gater and
// private IPs allowed, so two clients can reach each other over loopback.
func newGatedClient(t *testing.T, name string, gater connmgr.ConnectionGater, staticPeers ...string) *client {
	t.Helper()

	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:            name,
		PrivateKey:      privKey,
		AllowPrivateIPs: true,
		ConnectionGater: gater,
		StaticPeers:     staticPeers,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cl.Close()) })

	return cl.(*client)
}

func TestGaterChainAdmitsOnlyWhenEveryGaterAdmits(t *testing.T) {
	allow1, allow2, deny := &fixedGater{allow: true}, &fixedGater{allow: true}, &fixedGater{allow: false}

	admitted, _ := gaterChain{allow1, allow2}.InterceptUpgraded(nil)
	require.True(t, admitted)
	require.True(t, gaterChain{allow1, allow2}.InterceptPeerDial(""))
	require.True(t, gaterChain{allow1, allow2}.InterceptAddrDial("", nil))
	require.True(t, gaterChain{allow1, allow2}.InterceptAccept(nil))
	require.True(t, gaterChain{allow1, allow2}.InterceptSecured(network.DirInbound, "", nil))

	for _, chain := range []gaterChain{{deny, allow1}, {allow1, deny}} {
		admitted, _ := chain.InterceptUpgraded(nil)
		require.False(t, admitted)
		require.False(t, chain.InterceptPeerDial(""))
		require.False(t, chain.InterceptAddrDial("", nil))
		require.False(t, chain.InterceptAccept(nil))
		require.False(t, chain.InterceptSecured(network.DirInbound, "", nil))
	}
}

func TestGaterChainStopsAtFirstRefusal(t *testing.T) {
	deny, after := &fixedGater{allow: false}, &fixedGater{allow: true}

	require.False(t, gaterChain{deny, after}.InterceptPeerDial(""))
	require.Zero(t, after.calls, "a gater after a refusal must not be consulted")
}

func TestHostGatersSelection(t *testing.T) {
	log := &captureLogger{}
	noop := func() {}
	caller := &fixedGater{allow: true}

	t.Run("private IPs allowed and no caller gater installs none", func(t *testing.T) {
		gaters, err := hostGaters(Config{AllowPrivateIPs: true}, log, noop)
		require.NoError(t, err)
		require.Empty(t, gaters)
	})

	t.Run("private IPs allowed installs the caller gater alone", func(t *testing.T) {
		gaters, err := hostGaters(Config{AllowPrivateIPs: true, ConnectionGater: caller}, log, noop)
		require.NoError(t, err)
		require.Len(t, gaters, 1)
		require.Same(t, caller, gaters[0])
	})

	t.Run("private IP filter alone without a caller gater", func(t *testing.T) {
		gaters, err := hostGaters(Config{}, log, noop)
		require.NoError(t, err)
		require.Len(t, gaters, 1)
		require.IsType(t, &conngater.BasicConnectionGater{}, gaters[0])
	})

	t.Run("private IP filter in front of the caller gater", func(t *testing.T) {
		gaters, err := hostGaters(Config{ConnectionGater: caller}, log, noop)
		require.NoError(t, err)
		require.Len(t, gaters, 2)
		require.IsType(t, &conngater.BasicConnectionGater{}, gaters[0])
		require.Same(t, caller, gaters[1])

		// The private-IP filter still refuses a loopback dial even though the
		// caller's gater would admit it.
		loopback := multiaddr.StringCast("/ip4/127.0.0.1/tcp/9905")
		require.False(t, gaters.InterceptAddrDial("", loopback))
	})
}

// libp2p refuses a second ConnectionGater option, so a caller gater combined
// with the default private-IP filter must still produce a working client.
func TestNewClientWithCallerGaterAndPrivateIPFilter(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:            "gated",
		PrivateKey:      privKey,
		ConnectionGater: &fixedGater{allow: true},
	})
	require.NoError(t, err)
	require.NoError(t, cl.Close())
}

func TestConnectionGaterRefusesInboundFromBlockedPeer(t *testing.T) {
	gater, err := conngater.NewBasicConnectionGater(nil)
	require.NoError(t, err)

	listener := newGatedClient(t, "listener", gater)
	dialer := newGatedClient(t, "dialer", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Control: unblocked, the same dial connects, so a refusal below comes
	// from the gater and not from a bad address.
	require.NoError(t, dialer.Connect(ctx, loopbackAddr(t, listener)))
	require.Eventually(t, connectedness(listener, dialer), 5*time.Second, 50*time.Millisecond)

	// Block before disconnecting, as a ban does: the gater does not touch an
	// existing connection, so a background redial landing between the two
	// would otherwise survive.
	require.NoError(t, gater.BlockPeer(dialer.host.ID()))
	require.NoError(t, listener.DisconnectPeer(ctx, dialer.host.ID()))
	require.Eventually(t, func() bool { return !connectedness(dialer, listener)() },
		5*time.Second, 50*time.Millisecond)

	_ = dialer.Connect(ctx, loopbackAddr(t, listener))

	// The dialer may briefly see a connection the listener is tearing down;
	// the listener must never keep one.
	require.Never(t, connectedness(listener, dialer), time.Second, 50*time.Millisecond,
		"listener must refuse a connection from a peer its gater blocks")
	require.Eventually(t, func() bool { return !connectedness(dialer, listener)() },
		5*time.Second, 50*time.Millisecond)
}

func TestConnectionGaterRefusesOutboundToBlockedPeer(t *testing.T) {
	gater, err := conngater.NewBasicConnectionGater(nil)
	require.NoError(t, err)

	target := newGatedClient(t, "target", nil)
	dialer := newGatedClient(t, "dialer", gater)

	require.NoError(t, gater.BlockPeer(target.host.ID()))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.ErrorIs(t, dialer.Connect(ctx, loopbackAddr(t, target)), swarm.ErrGaterDisallowedConnection)
	require.False(t, connectedness(dialer, target)())
}

func TestDisconnectPeerClosesConnection(t *testing.T) {
	a := newGatedClient(t, "a", nil)
	b := newGatedClient(t, "b", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, b.Connect(ctx, loopbackAddr(t, a)))
	require.Eventually(t, connectedness(a, b), 5*time.Second, 50*time.Millisecond)

	require.NoError(t, a.DisconnectPeer(ctx, b.host.ID()))

	require.False(t, connectedness(a, b)(), "DisconnectPeer must close every connection to the peer")
	require.Eventually(t, func() bool { return !connectedness(b, a)() }, 5*time.Second, 50*time.Millisecond)
}

func TestDisconnectPeerNotConnectedIsNoOp(t *testing.T) {
	a := newGatedClient(t, "a", nil)
	b := newGatedClient(t, "b", nil)

	require.NoError(t, a.DisconnectPeer(context.Background(), b.host.ID()))
}

func TestDisconnectPeerHonoursCancelledContext(t *testing.T) {
	a := newGatedClient(t, "a", nil)
	b := newGatedClient(t, "b", nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, a.DisconnectPeer(ctx, b.host.ID()), context.Canceled)
}

func TestDisconnectPeerAfterCloseErrors(t *testing.T) {
	a := newGatedClient(t, "a", nil)
	b := newGatedClient(t, "b", nil)

	require.NoError(t, a.Close())

	require.ErrorIs(t, a.DisconnectPeer(context.Background(), b.host.ID()), errClientClosed)
}

func TestClientImplementsPeerDisconnector(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{Name: "x", PrivateKey: privKey})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() })

	_, ok := cl.(PeerDisconnector)
	require.True(t, ok, "the Client NewClient returns must implement PeerDisconnector")
}

// Severing a static peer alone does not keep it away: the maintenance loop
// redials it within seconds. Blocking it in the gater as well does.
func TestDisconnectPeerWithGaterBlockStaysDisconnected(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past the static-peer fast-retry interval")
	}

	gater, err := conngater.NewBasicConnectionGater(nil)
	require.NoError(t, err)

	listener := newGatedClient(t, "listener", gater)
	dialer := newGatedClient(t, "dialer", nil, loopbackAddr(t, listener))

	require.Eventually(t, connectedness(listener, dialer), 6*time.Second, 100*time.Millisecond)

	require.NoError(t, gater.BlockPeer(dialer.host.ID()))
	require.NoError(t, listener.DisconnectPeer(context.Background(), dialer.host.ID()))

	require.Never(t, connectedness(listener, dialer), 7*time.Second, 200*time.Millisecond,
		"a gater-blocked static peer must not reconnect after DisconnectPeer")
}
