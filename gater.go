package p2p

import (
	"context"
	"errors"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// PeerDisconnector is implemented by the Client that NewClient returns. It is
// a separate interface so that adding it does not break other implementations
// of Client.
type PeerDisconnector interface {
	// DisconnectPeer closes every connection to peerID. Disconnecting a peer
	// that is not connected is not an error; calling it after Close is.
	//
	// Closing a connection does not stop the peer coming back: it can redial,
	// and this client redials bootstrap and static peers and peers it
	// discovers. To keep a peer away, refuse it in Config.ConnectionGater too.
	DisconnectPeer(ctx context.Context, peerID peer.ID) error
}

var _ PeerDisconnector = (*client)(nil)

// errClientClosed is returned by DisconnectPeer after Close.
var errClientClosed = errors.New("client is closed")

// DisconnectPeer closes every connection to peerID. See PeerDisconnector.
func (c *client) DisconnectPeer(ctx context.Context, peerID peer.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()

	if closed {
		return errClientClosed
	}

	return c.host.Network().ClosePeer(peerID)
}

// hostGaters returns the connection gaters to install on the host, in the
// order they are consulted: the private-IP filter unless private IPs are
// allowed, then the caller's gater if one is configured. libp2p accepts only
// one gater per host, so buildHostOptions installs them as one gaterChain.
func hostGaters(config Config, log logger, cancel context.CancelFunc) (gaterChain, error) {
	var gaters gaterChain

	if !config.AllowPrivateIPs {
		ipFilter, err := createPrivateIPConnectionGater(log, cancel)
		if err != nil {
			return nil, err
		}

		gaters = append(gaters, ipFilter)
		log.Infof("Private IP connection gater enabled (blocking RFC1918 and local addresses)")
	}

	if config.ConnectionGater != nil {
		gaters = append(gaters, config.ConnectionGater)
		log.Infof("Caller-supplied connection gater enabled")
	}

	return gaters, nil
}

// gaterChain admits a connection only if every gater in it does, consulting
// them in order and stopping at the first refusal.
type gaterChain []connmgr.ConnectionGater

func (g gaterChain) InterceptPeerDial(p peer.ID) bool {
	for _, gater := range g {
		if !gater.InterceptPeerDial(p) {
			return false
		}
	}

	return true
}

func (g gaterChain) InterceptAddrDial(p peer.ID, addr multiaddr.Multiaddr) bool {
	for _, gater := range g {
		if !gater.InterceptAddrDial(p, addr) {
			return false
		}
	}

	return true
}

func (g gaterChain) InterceptAccept(addrs network.ConnMultiaddrs) bool {
	for _, gater := range g {
		if !gater.InterceptAccept(addrs) {
			return false
		}
	}

	return true
}

func (g gaterChain) InterceptSecured(dir network.Direction, p peer.ID, addrs network.ConnMultiaddrs) bool {
	for _, gater := range g {
		if !gater.InterceptSecured(dir, p, addrs) {
			return false
		}
	}

	return true
}

func (g gaterChain) InterceptUpgraded(conn network.Conn) (bool, control.DisconnectReason) {
	for _, gater := range g {
		if allow, reason := gater.InterceptUpgraded(conn); !allow {
			return false, reason
		}
	}

	return true, 0
}
