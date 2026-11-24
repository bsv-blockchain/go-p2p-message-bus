package p2p

import (
	"errors"
	"fmt"
	"net"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var (
	// ErrInvalidIP is returned by BlockAddr and UnblockAddr for an IP that is
	// nil or neither 4 nor 16 bytes long.
	ErrInvalidIP = errors.New("invalid IP address")
	// ErrInvalidSubnet is returned by BlockSubnet and UnblockSubnet for a nil
	// subnet, a non-canonical mask, or an IP that does not fit the mask.
	ErrInvalidSubnet = errors.New("invalid subnet")
	// ErrInvalidPeerID is returned by BlockPeer and UnblockPeer for a peer ID
	// that does not decode.
	ErrInvalidPeerID = errors.New("invalid peer ID")
)

// privateIPRanges are the ranges refused while Config.AllowPrivateIPs is false.
//
//nolint:gochecknoglobals // fixed table of built-in admission rules
var privateIPRanges = []string{
	"10.0.0.0/8",     // RFC1918 private network
	"172.16.0.0/12",  // RFC1918 private network
	"192.168.0.0/16", // RFC1918 private network
	"127.0.0.0/8",    // Loopback
	"169.254.0.0/16", // Link-local
	"100.64.0.0/10",  // Shared Address Space (RFC6598)
	"fc00::/7",       // IPv6 Unique Local Addresses
	"fe80::/10",      // IPv6 Link-Local Addresses
	"::1/128",        // IPv6 Loopback
}

// connAdmission is the host's single connection gater plus the keys of the
// subnets it blocks on its own behalf. Caller rules and built-in rules share
// the gater, which keys subnets by their string form; builtin lets
// UnblockSubnet leave a built-in range in place even when the caller blocked
// and unblocks the same range. builtin is filled once, before the host
// starts, and only read afterwards.
type connAdmission struct {
	gater   *conngater.BasicConnectionGater
	builtin map[string]struct{}
}

// newConnAdmission builds the gater installed on every host. Unless
// allowPrivateIPs is set it starts with the private, loopback and link-local
// ranges blocked: connections to or from them are refused.
func newConnAdmission(allowPrivateIPs bool) (*connAdmission, error) {
	gater, err := conngater.NewBasicConnectionGater(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection gater: %w", err)
	}
	a := &connAdmission{gater: gater, builtin: make(map[string]struct{})}
	if allowPrivateIPs {
		return a, nil
	}

	for _, cidr := range privateIPRanges {
		_, parsed, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse CIDR %s: %w", cidr, err)
		}
		ipnet, err := normalizeSubnet(parsed)
		if err != nil {
			return nil, fmt.Errorf("built-in subnet %s: %w", cidr, err)
		}
		if err := gater.BlockSubnet(ipnet); err != nil {
			return nil, fmt.Errorf("failed to block subnet %s: %w", cidr, err)
		}
		a.builtin[ipnet.String()] = struct{}{}
	}

	return a, nil
}

// refuses reports whether the gater would refuse a connection to peer p at
// remote address addr: p is blocked, or the leading IP of addr is blocked or
// inside a blocked subnet. As in the gater, a relayed connection's address
// starts with the relay's IP, so an IP rule on a relay covers every
// connection through it, and a peer behind a relay is matched by its peer ID
// alone. An address with no leading IP is judged by its peer alone.
func (a *connAdmission) refuses(p peer.ID, addr multiaddr.Multiaddr) bool {
	if !a.gater.InterceptPeerDial(p) {
		return true
	}
	// Checked here rather than left to InterceptAddrDial, which logs a
	// warning for every address it cannot convert.
	if addr == nil {
		return false
	}
	if _, err := manet.ToIP(addr); err != nil {
		return false
	}
	return !a.gater.InterceptAddrDial(p, addr)
}

// normalizeIP returns a copy of ip, or ErrInvalidIP.
func normalizeIP(ip net.IP) (net.IP, error) {
	if len(ip) != net.IPv4len && len(ip) != net.IPv6len {
		return nil, fmt.Errorf("%w: %q", ErrInvalidIP, ip.String())
	}
	if v4 := ip.To4(); v4 != nil {
		return v4, nil
	}
	return append(net.IP(nil), ip...), nil
}

// normalizeSubnet returns a copy of ipnet with the host bits cleared, so that
// "10.1.2.3/8" and "10.0.0.0/8" name the same rule. The gater keeps the
// pointer it is given and keys it by String, so callers must not be able to
// mutate it afterwards or unblock a range under a different spelling.
func normalizeSubnet(ipnet *net.IPNet) (*net.IPNet, error) {
	if ipnet == nil {
		return nil, fmt.Errorf("%w: nil", ErrInvalidSubnet)
	}
	ones, bits := ipnet.Mask.Size()
	var ip net.IP
	switch bits {
	case 8 * net.IPv4len:
		ip = ipnet.IP.To4()
	case 8 * net.IPv6len:
		if len(ipnet.IP) == net.IPv6len {
			ip = ipnet.IP
		}
	}
	if ip == nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSubnet, ipnet.String())
	}
	mask := net.CIDRMask(ones, bits)
	return &net.IPNet{IP: ip.Mask(mask), Mask: mask}, nil
}

// BlockAddr refuses connections to and from ip and closes open ones; see
// Client.BlockAddr.
func (c *client) BlockAddr(ip net.IP) error {
	ip, err := normalizeIP(ip)
	if err != nil {
		return err
	}
	if err := c.admission.gater.BlockAddr(ip); err != nil {
		return fmt.Errorf("failed to block address %s: %w", ip, err)
	}
	c.closeRefusedConns()
	return nil
}

// UnblockAddr removes a BlockAddr rule; see Client.UnblockAddr.
func (c *client) UnblockAddr(ip net.IP) error {
	ip, err := normalizeIP(ip)
	if err != nil {
		return err
	}
	if err := c.admission.gater.UnblockAddr(ip); err != nil {
		return fmt.Errorf("failed to unblock address %s: %w", ip, err)
	}
	return nil
}

// BlockSubnet refuses connections to and from ipnet and closes open ones; see
// Client.BlockSubnet.
func (c *client) BlockSubnet(ipnet *net.IPNet) error {
	ipnet, err := normalizeSubnet(ipnet)
	if err != nil {
		return err
	}
	if err := c.admission.gater.BlockSubnet(ipnet); err != nil {
		return fmt.Errorf("failed to block subnet %s: %w", ipnet, err)
	}
	c.closeRefusedConns()
	return nil
}

// UnblockSubnet removes a BlockSubnet rule, never a built-in one; see
// Client.UnblockSubnet.
func (c *client) UnblockSubnet(ipnet *net.IPNet) error {
	ipnet, err := normalizeSubnet(ipnet)
	if err != nil {
		return err
	}
	if _, builtin := c.admission.builtin[ipnet.String()]; builtin {
		return nil
	}
	if err := c.admission.gater.UnblockSubnet(ipnet); err != nil {
		return fmt.Errorf("failed to unblock subnet %s: %w", ipnet, err)
	}
	return nil
}

// BlockPeer refuses connections to and from peerID and closes open ones; see
// Client.BlockPeer.
func (c *client) BlockPeer(peerID string) error {
	id, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPeerID, err)
	}
	if err := c.admission.gater.BlockPeer(id); err != nil {
		return fmt.Errorf("failed to block peer %s: %w", id, err)
	}
	_ = c.host.Network().ClosePeer(id)
	return nil
}

// UnblockPeer removes a BlockPeer rule; see Client.UnblockPeer.
func (c *client) UnblockPeer(peerID string) error {
	id, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPeerID, err)
	}
	if err := c.admission.gater.UnblockPeer(id); err != nil {
		return fmt.Errorf("failed to unblock peer %s: %w", id, err)
	}
	return nil
}

// closeRefusedConns closes every open connection the gater now refuses. A
// block call adds its rule first and sweeps after, so a connection is either
// already in the table when the sweep reads it or lands later and is caught
// by closeIfRefused on its Connected event.
func (c *client) closeRefusedConns() {
	for _, conn := range c.host.Network().Conns() {
		if c.admission.refuses(conn.RemotePeer(), conn.RemoteMultiaddr()) {
			_ = conn.Close()
		}
	}
}

// closeIfRefused closes conn if the gater refuses it. The gater only sees new
// dials and accepts, so a handshake that passed it just before a block call
// can complete after that call's sweep; this catches it on arrival. It runs
// on libp2p's notification path, so the close is handed to a goroutine.
func (c *client) closeIfRefused(conn network.Conn) bool {
	if !c.admission.refuses(conn.RemotePeer(), conn.RemoteMultiaddr()) {
		return false
	}
	go func() { _ = conn.Close() }()
	return true
}
