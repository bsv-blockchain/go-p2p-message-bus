package p2p

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	loopback4 = net.ParseIP("127.0.0.1") //nolint:gochecknoglobals // test fixture
	loopback6 = net.ParseIP("::1")       //nolint:gochecknoglobals // test fixture
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, ipnet, err := net.ParseCIDR(s)
	require.NoError(t, err)
	return ipnet
}

// newAdmissionClient starts one DHT-off client on loopback that has not
// connected to anything.
func newAdmissionClient(t *testing.T, name string, allowPrivateIPs bool) *client {
	t.Helper()
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	cl, err := NewClient(Config{
		Name:            name,
		PrivateKey:      privKey,
		Port:            0,
		AllowPrivateIPs: allowPrivateIPs,
		DHTMode:         "off",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cl.Close()) })
	return cl.(*client)
}

// listenAddr returns c's TCP listen address on ip, or skips the test if c
// does not listen there (no IPv6 loopback on this machine).
func listenAddr(t *testing.T, c *client, ip net.IP) multiaddr.Multiaddr {
	t.Helper()
	for _, addr := range c.host.Addrs() {
		got, err := manet.ToIP(addr)
		if err == nil && got.Equal(ip) {
			return addr
		}
	}
	t.Skipf("client does not listen on %s: %v", ip, c.host.Addrs())
	return nil
}

// dialOnly dials to at addr alone. It first drops every connection between
// the two and every address either knows for the other, plus any dial backoff
// left by an earlier refused attempt, so the result says whether this one
// address is admitted. Without that, a leftover connection over another
// address would satisfy the dial without trying addr at all.
func dialOnly(t *testing.T, from, to *client, addr multiaddr.Multiaddr) error {
	t.Helper()
	disconnect(t, from, to)
	id := to.host.ID()
	if sw, ok := from.host.Network().(*swarm.Swarm); ok {
		sw.Backoff().Clear(id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
	defer cancel()
	return from.host.Connect(ctx, peer.AddrInfo{ID: id, Addrs: []multiaddr.Multiaddr{addr}})
}

// connsFromIP counts c's open connections whose remote address is ip.
func connsFromIP(c *client, ip net.IP) int {
	n := 0
	for _, conn := range c.host.Network().Conns() {
		got, err := manet.ToIP(conn.RemoteMultiaddr())
		if err == nil && got.Equal(ip) {
			n++
		}
	}
	return n
}

// connsTo counts c's open connections to other whose remote address is ip.
func connsTo(c, other *client, ip net.IP) int {
	n := 0
	for _, conn := range c.host.Network().ConnsToPeer(other.host.ID()) {
		got, err := manet.ToIP(conn.RemoteMultiaddr())
		if err == nil && got.Equal(ip) {
			n++
		}
	}
	return n
}

// disconnect closes every connection between a and b and waits until both
// sides see it. It also clears the addresses each knows for the other: a
// stream open still queued for a just-closed connection (pubsub opens one per
// new peer) would otherwise redial over whatever identify learned.
func disconnect(t *testing.T, a, b *client) {
	t.Helper()
	require.Eventually(t, func() bool {
		a.host.Peerstore().ClearAddrs(b.host.ID())
		b.host.Peerstore().ClearAddrs(a.host.ID())
		_ = a.host.Network().ClosePeer(b.host.ID())
		_ = b.host.Network().ClosePeer(a.host.ID())
		return len(a.host.Network().ConnsToPeer(b.host.ID())) == 0 &&
			len(b.host.Network().ConnsToPeer(a.host.ID())) == 0
	}, eventuallyTimeout, eventuallyTick)
}

// gatedTrio is a client under test plus one peer that dials in to it and one
// it dials out to. Two different peers, because libp2p binds outbound TCP
// dials to the listen port: a dial back between the same two hosts reuses
// the previous connection's 4-tuple and can collide with it.
type gatedTrio struct {
	a, in, out *client
}

func newGatedTrio(t *testing.T, allowPrivateIPs bool) gatedTrio {
	t.Helper()
	return gatedTrio{
		a:   newAdmissionClient(t, "gated", allowPrivateIPs),
		in:  newAdmissionClient(t, "dialer", true),
		out: newAdmissionClient(t, "listener", true),
	}
}

// assertAdmitted checks that a dial in to a and a dial out from a, each over
// ip alone, end with a connection over ip, then tears both down.
func (g gatedTrio) assertAdmitted(t *testing.T, ip net.IP) {
	t.Helper()
	require.NoError(t, dialOnly(t, g.in, g.a, listenAddr(t, g.a, ip)), "dial in over %s", ip)
	require.Eventually(t, func() bool { return connsTo(g.a, g.in, ip) > 0 }, eventuallyTimeout, eventuallyTick, "dial in over %s", ip)
	disconnect(t, g.a, g.in)

	require.NoError(t, dialOnly(t, g.a, g.out, listenAddr(t, g.out, ip)), "dial out over %s", ip)
	require.Positive(t, connsTo(g.a, g.out, ip), "dial out over %s", ip)
	disconnect(t, g.a, g.out)
}

// assertRefused checks that a admits no connection over ip, whether dialed
// in to or dialing out, and that the dialer ends up with none either. A
// dial refused on peer ID can complete on the dialer's side before a closes
// it, so the dial-in error is not asserted; the connection table is.
func (g gatedTrio) assertRefused(t *testing.T, ip net.IP) {
	t.Helper()
	_ = dialOnly(t, g.in, g.a, listenAddr(t, g.a, ip))
	assert.Zero(t, connsTo(g.a, g.in, ip), "dial in over %s admitted", ip)
	require.Eventually(t, func() bool { return connsTo(g.in, g.a, ip) == 0 }, eventuallyTimeout, eventuallyTick,
		"dialer kept a connection over %s", ip)

	require.Error(t, dialOnly(t, g.a, g.out, listenAddr(t, g.out, ip)), "dial out over %s", ip)
	assert.Zero(t, connsTo(g.a, g.out, ip), "dial out over %s admitted", ip)
	require.Eventually(t, func() bool { return connsTo(g.out, g.a, ip) == 0 }, eventuallyTimeout, eventuallyTick)
}

func TestNormalizeIP(t *testing.T) {
	tests := []struct {
		name    string
		ip      net.IP
		want    string
		wantErr bool
	}{
		{name: "ipv4", ip: net.IPv4(192, 0, 2, 1).To4(), want: "192.0.2.1"},
		{name: "ipv4 in 16 bytes", ip: net.IPv4(192, 0, 2, 1), want: "192.0.2.1"},
		{name: "ipv4-mapped ipv6", ip: net.ParseIP("::ffff:192.0.2.1"), want: "192.0.2.1"},
		{name: "ipv6", ip: net.ParseIP("2001:db8::1"), want: "2001:db8::1"},
		{name: "nil", ip: nil, wantErr: true},
		{name: "short", ip: net.IP{1, 2, 3}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeIP(tt.ip)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidIP)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}

	// The result is a copy: changing the caller's slice later does not
	// change a stored rule.
	in := net.ParseIP("2001:db8::1")
	got, err := normalizeIP(in)
	require.NoError(t, err)
	in[0] = 0xff
	assert.Equal(t, "2001:db8::1", got.String())
}

func TestNormalizeSubnet(t *testing.T) {
	tests := []struct {
		name    string
		ipnet   *net.IPNet
		want    string
		wantErr bool
	}{
		{name: "canonical ipv4", ipnet: mustCIDR(t, "10.0.0.0/8"), want: "10.0.0.0/8"},
		{name: "host bits cleared", ipnet: &net.IPNet{IP: net.IPv4(10, 1, 2, 3), Mask: net.CIDRMask(8, 32)}, want: "10.0.0.0/8"},
		{name: "ipv4 /32", ipnet: &net.IPNet{IP: net.IPv4(192, 0, 2, 1), Mask: net.CIDRMask(32, 32)}, want: "192.0.2.1/32"},
		{name: "ipv6 host bits cleared", ipnet: &net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: net.CIDRMask(32, 128)}, want: "2001:db8::/32"},
		{name: "nil", ipnet: nil, wantErr: true},
		{name: "non-prefix mask", ipnet: &net.IPNet{IP: net.IPv4(10, 0, 0, 0), Mask: net.IPv4Mask(255, 0, 255, 0)}, wantErr: true},
		{name: "nil mask", ipnet: &net.IPNet{IP: net.IPv4(10, 0, 0, 0)}, wantErr: true},
		{name: "ipv6 ip with ipv4 mask", ipnet: &net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: net.CIDRMask(8, 32)}, wantErr: true},
		{name: "ipv4 ip with ipv6 mask", ipnet: &net.IPNet{IP: net.IPv4(10, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 128)}, wantErr: true},
		{name: "nil ip", ipnet: &net.IPNet{Mask: net.CIDRMask(8, 32)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeSubnet(tt.ipnet)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidSubnet)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}
}

func TestConnAdmissionRefuses(t *testing.T) {
	a, err := newConnAdmission(true)
	require.NoError(t, err)
	blocked, allowed := newTestPeerID(t), newTestPeerID(t)

	require.NoError(t, a.gater.BlockAddr(net.ParseIP("192.0.2.1")))
	require.NoError(t, a.gater.BlockSubnet(mustCIDR(t, "198.51.100.0/24")))
	require.NoError(t, a.gater.BlockSubnet(mustCIDR(t, "2001:db8::/32")))
	require.NoError(t, a.gater.BlockPeer(blocked))

	circuit := fmt.Sprintf("/ip4/203.0.113.9/tcp/4001/p2p/%s/p2p-circuit", newTestPeerID(t))
	tests := []struct {
		name   string
		peer   peer.ID
		addr   string
		refuse bool
	}{
		{name: "blocked ipv4", peer: allowed, addr: "/ip4/192.0.2.1/tcp/1", refuse: true},
		{name: "blocked ipv4 as mapped ipv6", peer: allowed, addr: "/ip6/::ffff:192.0.2.1/tcp/1", refuse: true},
		{name: "neighbour of blocked ipv4", peer: allowed, addr: "/ip4/192.0.2.2/tcp/1"},
		{name: "inside ipv4 subnet", peer: allowed, addr: "/ip4/198.51.100.7/tcp/1", refuse: true},
		{name: "inside ipv4 subnet as mapped ipv6", peer: allowed, addr: "/ip6/::ffff:198.51.100.7/tcp/1", refuse: true},
		{name: "outside ipv4 subnet", peer: allowed, addr: "/ip4/198.51.101.1/tcp/1"},
		{name: "inside ipv6 subnet", peer: allowed, addr: "/ip6/2001:db8::1/tcp/1", refuse: true},
		{name: "outside ipv6 subnet", peer: allowed, addr: "/ip6/2001:db9::1/tcp/1"},
		{name: "blocked peer on clean address", peer: blocked, addr: "/ip4/203.0.113.1/tcp/1", refuse: true},
		{name: "relay circuit, allowed peer", peer: allowed, addr: circuit},
		{name: "relay circuit, blocked peer", peer: blocked, addr: circuit, refuse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := multiaddr.NewMultiaddr(tt.addr)
			require.NoError(t, err)
			assert.Equal(t, tt.refuse, a.refuses(tt.peer, addr))
		})
	}
	assert.False(t, a.refuses(allowed, nil), "nil address, allowed peer")
	assert.True(t, a.refuses(blocked, nil), "nil address, blocked peer")
}

func TestNewConnAdmissionBuiltinRanges(t *testing.T) {
	t.Run("AllowPrivateIPs false blocks the private ranges", func(t *testing.T) {
		a, err := newConnAdmission(false)
		require.NoError(t, err)
		assert.Len(t, a.gater.ListBlockedSubnets(), len(privateIPRanges))
		assert.Len(t, a.builtin, len(privateIPRanges))
		for _, cidr := range privateIPRanges {
			assert.Contains(t, a.builtin, cidr)
		}
		id := newTestPeerID(t)
		for _, addr := range []string{"/ip4/10.0.0.1/tcp/1", "/ip4/127.0.0.1/tcp/1", "/ip6/::1/tcp/1", "/ip6/fe80::1/tcp/1"} {
			assert.True(t, a.refuses(id, multiaddr.StringCast(addr)), addr)
		}
		assert.False(t, a.refuses(id, multiaddr.StringCast("/ip4/203.0.113.1/tcp/1")))
	})

	t.Run("AllowPrivateIPs true starts with no rules", func(t *testing.T) {
		a, err := newConnAdmission(true)
		require.NoError(t, err)
		assert.Empty(t, a.gater.ListBlockedSubnets())
		assert.Empty(t, a.gater.ListBlockedAddrs())
		assert.Empty(t, a.gater.ListBlockedPeers())
		assert.Empty(t, a.builtin)
		id := newTestPeerID(t)
		for _, addr := range []string{"/ip4/10.0.0.1/tcp/1", "/ip4/127.0.0.1/tcp/1", "/ip6/::1/tcp/1"} {
			assert.False(t, a.refuses(id, multiaddr.StringCast(addr)), addr)
		}
	})
}

// With AllowPrivateIPs true and no caller rules, a client admits loopback
// connections in both directions, as before the gater was always installed.
func TestAllowPrivateIPsWithoutRulesAdmitsAll(t *testing.T) {
	g := newGatedTrio(t, true)

	assert.Empty(t, g.a.admission.gater.ListBlockedSubnets())
	assert.Empty(t, g.a.admission.gater.ListBlockedAddrs())
	assert.Empty(t, g.a.admission.gater.ListBlockedPeers())
	g.assertAdmitted(t, loopback4)
}

func TestBlockAddrClosesExistingConnection(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	require.Positive(t, connsFromIP(a, loopback4))

	require.NoError(t, a.BlockAddr(loopback4))
	require.Eventually(t, func() bool {
		return connsFromIP(a, loopback4) == 0 && connsFromIP(b, loopback4) == 0
	}, eventuallyTimeout, eventuallyTick)
}

func TestBlockAddrRefusesDialInAndOut(t *testing.T) {
	g := newGatedTrio(t, true)

	require.NoError(t, g.a.BlockAddr(loopback4))
	g.assertRefused(t, loopback4)

	t.Run("other address unaffected", func(t *testing.T) {
		g.assertAdmitted(t, loopback6)
	})

	t.Run("unblock restores", func(t *testing.T) {
		require.NoError(t, g.a.UnblockAddr(loopback4))
		g.assertAdmitted(t, loopback4)
	})

	t.Run("IPv4-mapped form names the same rule", func(t *testing.T) {
		mapped := net.ParseIP("::ffff:127.0.0.1")
		require.NoError(t, g.a.BlockAddr(mapped))
		g.assertRefused(t, loopback4)
		require.NoError(t, g.a.UnblockAddr(loopback4))
		g.assertAdmitted(t, loopback4)
	})
}

func TestBlockSubnetClosesExistingConnection(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	require.Positive(t, connsFromIP(a, loopback4))

	require.NoError(t, a.BlockSubnet(mustCIDR(t, "127.0.0.0/8")))
	require.Eventually(t, func() bool {
		return connsFromIP(a, loopback4) == 0 && connsFromIP(b, loopback4) == 0
	}, eventuallyTimeout, eventuallyTick)
}

func TestBlockSubnetRefusesDialInAndOut(t *testing.T) {
	g := newGatedTrio(t, true)

	// Host bits set on purpose: the rule is 127.0.0.0/8.
	require.NoError(t, g.a.BlockSubnet(&net.IPNet{IP: net.IPv4(127, 9, 9, 9), Mask: net.CIDRMask(8, 32)}))
	g.assertRefused(t, loopback4)

	t.Run("address outside the subnet unaffected", func(t *testing.T) {
		g.assertAdmitted(t, loopback6)
	})

	t.Run("unblock restores", func(t *testing.T) {
		require.NoError(t, g.a.UnblockSubnet(mustCIDR(t, "127.0.0.0/8")))
		g.assertAdmitted(t, loopback4)
	})

	t.Run("ipv6 subnet", func(t *testing.T) {
		listenAddr(t, g.a, loopback6)
		require.NoError(t, g.a.BlockSubnet(mustCIDR(t, "::1/128")))
		g.assertRefused(t, loopback6)
		g.assertAdmitted(t, loopback4)
		require.NoError(t, g.a.UnblockSubnet(mustCIDR(t, "::1/128")))
		g.assertAdmitted(t, loopback6)
	})
}

func TestBlockPeerRefusesOnAnyAddress(t *testing.T) {
	g := newGatedTrio(t, true)
	for _, other := range []*client{g.in, g.out} {
		require.NoError(t, dialOnly(t, g.a, other, listenAddr(t, other, loopback4)))
	}

	for _, other := range []*client{g.in, g.out} {
		require.NoError(t, g.a.BlockPeer(other.host.ID().String()))
	}
	require.Eventually(t, func() bool {
		return len(g.a.host.Network().Conns()) == 0 &&
			len(g.in.host.Network().ConnsToPeer(g.a.host.ID())) == 0 &&
			len(g.out.host.Network().ConnsToPeer(g.a.host.ID())) == 0
	}, eventuallyTimeout, eventuallyTick)

	g.assertRefused(t, loopback4)
	g.assertRefused(t, loopback6)

	// Another peer on the same address is unaffected.
	other := newGatedTrio(t, true)
	g.in, g.out = other.in, other.out
	g.assertAdmitted(t, loopback4)

	g.in, g.out = other.a, other.a
	require.NoError(t, g.a.BlockPeer(other.a.host.ID().String()))
	require.NoError(t, g.a.UnblockPeer(other.a.host.ID().String()))
	require.NoError(t, dialOnly(t, g.a, other.a, listenAddr(t, other.a, loopback4)))
}

// While AllowPrivateIPs is false, no caller unblock lifts a built-in range:
// not an exact match, not a differently spelled one, and not an UnblockAddr.
func TestUnblockCannotLiftPrivateRange(t *testing.T) {
	g := newGatedTrio(t, false)
	c := g.a

	for _, cidr := range privateIPRanges {
		ipnet := mustCIDR(t, cidr)
		require.NoError(t, c.BlockSubnet(ipnet))
		require.NoError(t, c.UnblockSubnet(ipnet))
		require.NoError(t, c.UnblockSubnet(ipnet), "second unblock")
	}
	require.NoError(t, c.UnblockSubnet(&net.IPNet{IP: net.IPv4(127, 1, 2, 3), Mask: net.CIDRMask(8, 32)}))
	require.NoError(t, c.BlockAddr(loopback4))
	require.NoError(t, c.UnblockAddr(loopback4))

	assert.Len(t, c.admission.gater.ListBlockedSubnets(), len(privateIPRanges))
	g.assertRefused(t, loopback4)

	// A caller rule beside the built-ins is still removable.
	require.NoError(t, c.BlockSubnet(mustCIDR(t, "203.0.113.0/24")))
	require.NoError(t, c.UnblockSubnet(mustCIDR(t, "203.0.113.0/24")))
	assert.Len(t, c.admission.gater.ListBlockedSubnets(), len(privateIPRanges))
}

// With AllowPrivateIPs true there are no built-in ranges, so a caller can
// block and then lift a private range.
func TestUnblockLiftsCallerPrivateRangeWhenAllowed(t *testing.T) {
	g := newGatedTrio(t, true)

	require.NoError(t, g.a.BlockSubnet(mustCIDR(t, "127.0.0.0/8")))
	g.assertRefused(t, loopback4)
	require.NoError(t, g.a.UnblockSubnet(mustCIDR(t, "127.0.0.0/8")))
	g.assertAdmitted(t, loopback4)
}

func TestBlockRejectsInvalidInput(t *testing.T) {
	a := newAdmissionClient(t, "peer-a", true)

	require.ErrorIs(t, a.BlockAddr(nil), ErrInvalidIP)
	require.ErrorIs(t, a.UnblockAddr(net.IP{1, 2}), ErrInvalidIP)
	require.ErrorIs(t, a.BlockSubnet(nil), ErrInvalidSubnet)
	require.ErrorIs(t, a.UnblockSubnet(&net.IPNet{IP: net.IPv4(10, 0, 0, 0)}), ErrInvalidSubnet)
	for _, id := range []string{"", "not-a-peer-id"} {
		require.ErrorIs(t, a.BlockPeer(id), ErrInvalidPeerID)
		require.ErrorIs(t, a.UnblockPeer(id), ErrInvalidPeerID)
	}
	assert.Empty(t, a.admission.gater.ListBlockedSubnets())
	assert.Empty(t, a.admission.gater.ListBlockedAddrs())
	assert.Empty(t, a.admission.gater.ListBlockedPeers())
}

func TestBlockAfterClose(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	require.NoError(t, a.Close())

	id := b.host.ID().String()
	ipnet := mustCIDR(t, "127.0.0.0/8")
	require.NoError(t, a.BlockAddr(loopback4))
	require.NoError(t, a.UnblockAddr(loopback4))
	require.NoError(t, a.BlockSubnet(ipnet))
	require.NoError(t, a.UnblockSubnet(ipnet))
	require.NoError(t, a.BlockPeer(id))
	require.NoError(t, a.UnblockPeer(id))
}

// Block and Unblock calls race each other, the sweep and new connections;
// run under -race.
func TestBlockConcurrent(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	bID := b.host.ID().String()
	ipnet := mustCIDR(t, "127.0.0.0/8")
	bAddr := fmt.Sprintf("%s/p2p/%s", listenAddr(t, b, loopback4), b.host.ID())

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				_ = a.BlockAddr(loopback4)
				_ = a.BlockSubnet(ipnet)
				_ = a.BlockPeer(bID)
				_ = a.PeerAddrs(bID)
				_ = a.UnblockAddr(loopback4)
				_ = a.UnblockSubnet(ipnet)
				_ = a.UnblockPeer(bID)
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
			_ = a.Connect(ctx, bAddr)
			cancel()
		}
	})
	wg.Wait()

	assert.Empty(t, a.admission.gater.ListBlockedSubnets())
	assert.Empty(t, a.admission.gater.ListBlockedAddrs())
	assert.Empty(t, a.admission.gater.ListBlockedPeers())
}

// A connection whose handshake passed the gater before a block call can land
// after that call's sweep. The Connected handler closes it on arrival; this
// adds the rule without a sweep and delivers the event by hand.
func TestConnectedEventClosesRefusedConn(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	conns := a.host.Network().ConnsToPeer(b.host.ID())
	require.NotEmpty(t, conns)

	require.NoError(t, a.admission.gater.BlockPeer(b.host.ID()))
	require.NotEmpty(t, a.host.Network().ConnsToPeer(b.host.ID()), "rule alone must not close connections")

	for _, conn := range conns {
		a.connNotifee.Connected(a.host.Network(), conn)
	}
	require.Eventually(t, func() bool {
		return len(a.host.Network().ConnsToPeer(b.host.ID())) == 0
	}, eventuallyTimeout, eventuallyTick)
}

// An admitted connection's Connected event goes on to the topic-peer handler
// untouched.
func TestConnectedEventKeepsAdmittedConn(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	conns := a.host.Network().ConnsToPeer(b.host.ID())
	require.NotEmpty(t, conns)

	for _, conn := range conns {
		assert.False(t, a.closeIfRefused(conn))
		a.connNotifee.Connected(a.host.Network(), conn)
	}
	assert.Equal(t, network.Connected, a.host.Network().Connectedness(b.host.ID()))
}
