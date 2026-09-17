package p2p

import (
	"errors"
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ErrInvalidAllowedPeerID is returned by NewClient when Config.AllowedPeerIDs
// contains an entry that is not a valid libp2p peer ID.
var ErrInvalidAllowedPeerID = errors.New("invalid AllowedPeerIDs entry")

// peerAllowlist is the set of peers whose pubsub messages this node accepts. A
// nil allowlist, or one built from an empty Config.AllowedPeerIDs, is disabled
// and allows every peer.
type peerAllowlist struct {
	allowed map[peer.ID]struct{}
}

// newPeerAllowlist builds the allowlist from config. When AllowedPeerIDs is
// empty the result is disabled. Otherwise the configured IDs are joined by
// selfID - locally published messages pass through the same validators, so a
// node that does not allow itself cannot publish at all - and by the peer IDs
// of config.StaticPeers, which the config already describes as known trusted
// peers. BootstrapPeers are deliberately excluded: bootstrap is a routing role,
// not a trust statement, and the default list is public infrastructure.
func newPeerAllowlist(config Config, selfID peer.ID, log logger) (*peerAllowlist, error) {
	if len(config.AllowedPeerIDs) == 0 {
		return &peerAllowlist{}, nil
	}

	allowed := make(map[peer.ID]struct{}, len(config.AllowedPeerIDs)+1)

	for _, entry := range config.AllowedPeerIDs {
		id, err := peer.Decode(entry)
		if err != nil {
			return nil, fmt.Errorf("%w %q: %w", ErrInvalidAllowedPeerID, entry, err)
		}

		allowed[id] = struct{}{}
	}

	allowed[selfID] = struct{}{}

	for _, addrInfo := range parsePeerMultiaddrs(config.StaticPeers, log) {
		allowed[addrInfo.ID] = struct{}{}
	}

	log.Infof("Peer allowlist enabled: accepting pubsub messages from %d peer(s)", len(allowed))

	return &peerAllowlist{allowed: allowed}, nil
}

// enabled reports whether the allowlist restricts anything. A nil allowlist is
// disabled.
func (a *peerAllowlist) enabled() bool {
	return a != nil && len(a.allowed) > 0
}

// allows reports whether pubsub messages authored by id should be accepted. A
// disabled allowlist allows every peer.
func (a *peerAllowlist) allows(id peer.ID) bool {
	if !a.enabled() {
		return true
	}

	_, ok := a.allowed[id]

	return ok
}
