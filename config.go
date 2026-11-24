package p2p

import (
	"log"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// logger defines the interface for logging in the P2P client.
type logger interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Warnf(format string, v ...any)
	Errorf(format string, v ...any)
}

// DefaultLogger is a simple logger implementation using the standard log package.
type DefaultLogger struct{}

// Debugf logs a debug message with formatting.
func (d *DefaultLogger) Debugf(format string, v ...any) { log.Printf("[DEBUG] "+format, v...) }

// Infof logs an info message with formatting.
func (d *DefaultLogger) Infof(format string, v ...any) { log.Printf("[INFO] "+format, v...) }

// Warnf logs a warning message with formatting.
func (d *DefaultLogger) Warnf(format string, v ...any) { log.Printf("[WARN] "+format, v...) }

// Errorf logs an error message with formatting.
func (d *DefaultLogger) Errorf(format string, v ...any) { log.Printf("[ERROR] "+format, v...) }

// Config contains the configuration options for creating a P2P client.
type Config struct {
	// Name is a required identifier for this peer. It will be included in messages
	// so other peers can identify the sender.
	Name string

	// Logger is an optional logger to use for logging. If not provided, the client will use
	// DefaultLogger. Set to a custom implementation to integrate with your logging framework.
	Logger logger

	// PrivateKey is a required private key for the peer.
	// This ensures the peer ID remains consistent across restarts.
	// Use GeneratePrivateKeyHex() to create a new key for first-time setup.
	PrivateKey crypto.PrivKey

	// This is used to namespace all topics that we publish to and subscribe to.
	ProtocolVersion string

	// PeerCacheFile is an optional path to a file for persisting peer information.
	// If provided, the client will save connected peers to this file and reload them
	// on restart for faster reconnection. If not provided, peer caching is disabled.
	PeerCacheFile string

	// AnnounceAddrs is an optional list of multiaddr strings that this peer should
	// advertise to other peers. This is useful in Kubernetes or other environments
	// where the local address differs from the externally reachable address.
	// Example: []string{"/ip4/203.0.113.1/tcp/4001"}
	// If not provided, libp2p will automatically detect and announce local addresses.
	AnnounceAddrs []string

	// Port is the network port to listen on for incoming connections. If zero, a random port will be chosen.
	Port int

	// PeerCacheTTL is the duration after which unseen peers are removed from the cache.
	// Peers not seen for longer than this duration are dropped when the cache is
	// loaded or saved.
	// If not provided or zero, defaults to 24 hours (same as go-ethereum).
	// Set to a negative value to disable TTL-based eviction.
	PeerCacheTTL time.Duration

	// BootstrapPeers is an optional list of multiaddr strings for bootstrap servers.
	// If provided, these peers will be used for DHT bootstrapping and relay/circuit functionality.
	// If not provided, default IPFS bootstrap peers will be used for bootstrapping and relay,
	// unless DisableDefaultBootstrapPeers is set.
	//
	// IMPORTANT: When DHTMode is "off", include ALL known bootstrap servers here for best results.
	// Topic peer exchange can only establish direct connections to peers whose addresses are
	// in the peerstore, which comes from bootstrap peers and identify protocol exchanges.
	//
	// Example: []string{
	//   "/dns4/server1.example.com/tcp/9905/p2p/12D3KooW...",
	//   "/dns4/server2.example.com/tcp/9905/p2p/12D3KooW...",
	// }
	BootstrapPeers []string

	// DisableDefaultBootstrapPeers stops an empty BootstrapPeers list from falling
	// back to the public IPFS bootstrap peers. Set it for private or local networks
	// (e.g. regtest) that must not dial, relay through or join the DHT of the public
	// IPFS network. Has no effect when BootstrapPeers is non-empty. Default: false.
	DisableDefaultBootstrapPeers bool

	// StaticPeers is an optional list of multiaddr strings for peers the client
	// should always be connected to. Unlike BootstrapPeers, static peers are not
	// used for DHT bootstrapping and not used as relay peers; they are simply
	// dialed at startup and re-dialed by a maintenance loop if the connection
	// drops. Use this for known trusted peers in private/closed networks where
	// you want guaranteed direct connectivity regardless of DHT state.
	//
	// Static peers are dialed in addition to BootstrapPeers, and the same address
	// may appear in both lists. The two are not interchangeable, though: when
	// AllowedPublisherIDs is in use, listing an address here also grants the peer
	// ID it names trust as a publisher, while listing it under BootstrapPeers does
	// not. Put a peer here only if you are willing to act on the messages it
	// publishes.
	//
	// Example: []string{
	//   "/dns4/peer1.internal/tcp/9905/p2p/12D3KooW...",
	//   "/ip4/192.168.1.10/tcp/9905/p2p/12D3KooW...",
	// }
	StaticPeers []string

	// AllowedPublisherIDs optionally restricts which peers this node accepts
	// pubsub messages from, on every topic. It gates message authorship only -
	// not connections, not streams, not the peer-address exchange. When empty
	// (the default) messages from any peer are accepted, matching previous
	// behaviour.
	//
	// Each entry is a peer ID string (e.g. "12D3KooW..."). An unparseable entry
	// is a configuration error and NewClient fails.
	//
	// Matching is on the authenticated libp2p peer ID of the message author,
	// never on the self-reported Name carried in the message envelope, which any
	// peer can forge. GossipSub's default StrictSign policy means the author ID
	// cannot be spoofed without the corresponding private key.
	//
	// Messages from peers outside the set are neither delivered to subscribers
	// nor forwarded to the mesh, and the sender's peer score is not penalized.
	// Connections to those peers are unaffected: they are still dialed, still
	// kept connected, and still served the peer-address exchange protocol.
	//
	// The filter is on authorship, not on freshness or liveness. A message an
	// allowlisted publisher authored once can be replayed by any peer and will be
	// delivered as new, as soon as libp2p's seen-cache window for it has passed.
	// Reject stale or duplicate work at the application layer if that matters.
	//
	// On a network where peers run GossipSub scoring, persistently failing to
	// forward non-allowlisted peers' messages degrades this node's own mesh
	// delivery score with its peers, which can get it pruned or graylisted from
	// the mesh - in turn degrading delivery of the allowlisted peers' messages
	// too. This is a real cost of a narrow allowlist on a scored network, not
	// just a cost to the excluded peers.
	//
	// Because non-allowlisted peers never reach receiveMessages, they are never
	// recorded as topic peers: while filtered they do not appear in GetPeers()
	// output at all, even though they remain connected, and no peer-address
	// discovery is attempted for them either. PeerCacheFile does not follow
	// GetPeers() here: it is written from the GossipSub topic peer lists, so a
	// filtered peer is still cached and still re-dialed at the next startup.
	// The allowlist gates messages, not connections.
	//
	// When non-empty, the set is augmented with this node's own peer ID (this is
	// required: locally published messages pass through the same validators) and
	// with the peer IDs that StaticPeers entries name directly through a /p2p/
	// component. For a relayed address the ID taken is the target's, never the
	// relay's.
	//
	// Publisher IDs are only ever read from configuration text - these entries
	// and the /p2p/ components of StaticPeers. DNS never contributes one. A bare
	// /dnsaddr/<host> entry carries no suffix for the resolver to filter its TXT
	// records against, so trusting what it resolves to would let whoever answers
	// that lookup choose which publishers this node acts on. Such an entry
	// therefore contributes no publisher ID at all, is warned about at startup,
	// and the set is not rebuilt later: use the /dnsaddr/<host>/p2p/<id> form
	// when this allowlist is in use.
	//
	// BootstrapPeers are NOT implicitly allowed; list them here explicitly if
	// they publish.
	//
	// Example: []string{"12D3KooWA...", "12D3KooWB..."}
	AllowedPublisherIDs []string

	// DHTMode specifies how this node participates in the DHT.
	// Valid values: "server", "client", "off"
	//
	// - "server": Full DHT participation (advertises, stores provider records, routes queries).
	//             Connects to the broader IPFS/libp2p DHT network.
	//             High overhead but collects peer addresses from across the network.
	//             Use this for: bootstrap/relay servers, public well-connected nodes.
	//
	// - "client": Queries DHT but doesn't advertise or store records.
	//             Still crawls DHT network for routing (connects to 100+ peers).
	//             Reduced overhead but NOT lightweight - still participates in DHT routing.
	//             Generally not recommended - use "server" or "off" instead.
	//
	// - "off":    Completely disables DHT. Topic-only network mode.
	//             Only connects to BootstrapPeers and discovers topic peers via GossipSub mesh.
	//             Most lightweight - no DHT crawling, no random peer connections.
	//             Uses custom peer address exchange protocol to request direct addresses
	//             from connected peers (typically bootstrap servers running in "server" mode).
	//             DHT-based hole punching is not available in this mode. Peers without
	//             public addresses will communicate via relay circuits through bootstrap servers.
	//             Ideal for: lightweight clients, isolated topic networks, avoiding DHT overhead.
	//             RECOMMENDED for abuse-sensitive cloud hosting (Hetzner, OVH, etc.) to avoid
	//             network scanning alerts from DHT peer discovery connecting to 100+ IPs.
	//
	// ARCHITECTURE: Run bootstrap servers in "server" mode (collects addresses via DHT),
	//               and clients in "off" mode (requests addresses from bootstrap servers).
	//               This creates a hybrid model: servers are well-connected, clients are lightweight.
	//
	// If not provided or empty, defaults to "server" mode.
	DHTMode string

	// peerAddressStreamTimeout overrides the per-stream deadline of the
	// peer-address exchange. Zero means the default. Tests shorten it; it is
	// unexported because production peers must agree on the default.
	peerAddressStreamTimeout time.Duration

	// peerCacheSaveInterval overrides how long peer cache save requests are
	// coalesced before one write. Zero means peerCacheSaveInterval. Tests
	// shorten it.
	peerCacheSaveInterval time.Duration

	// DHTCleanupInterval is the interval at which the DHT's ProviderManager performs
	// garbage collection of expired provider records. The cleanup involves querying all
	// provider records and removing expired entries.
	// Only applies when DHTMode is "server".
	// If not provided or zero, uses the DHT default (1 hour).
	// Recommended: 6-24 hours for production to reduce CPU overhead.
	// The cleanup frequency trades off between memory usage (stale records) and CPU usage.
	DHTCleanupInterval time.Duration

	// EnableNAT enables UPnP/NAT-PMP automatic port mapping features.
	// When true, the node will scan the local gateway (e.g., 10.0.0.1) to configure port forwarding.
	// IMPORTANT: This triggers network scanning alerts on shared hosting (Hetzner, AWS, etc.).
	// Only enable for local development behind a home router/NAT.
	// Default: false (NAT features disabled for production safety)
	EnableNAT bool

	// EnableMDNS enables multicast DNS peer discovery on the local network.
	// When true, the node broadcasts mDNS queries to discover peers on the same LAN.
	// IMPORTANT: Only enable on isolated local networks with proper VLANs. On shared hosting
	// (e.g., Hetzner, AWS) without VLANs, mDNS broadcasts appear as network scanning and may
	// result in abuse reports.
	// Default: false (mDNS disabled for production safety)
	// Set to true only for local development networks with proper isolation
	EnableMDNS bool

	// AllowPrivateIPs allows connections to private/local IP addresses during peer discovery.
	// When true, the node will attempt to connect to RFC1918 private networks (10.0.0.0/8,
	// 172.16.0.0/12, 192.168.0.0/16), link-local addresses (169.254.0.0/16), and localhost.
	// IMPORTANT: Only enable on private networks. On shared hosting, this may trigger network
	// scanning alerts.
	// Default: false (private IPs filtered for production safety)
	// Set to true only for local development or private network deployments
	AllowPrivateIPs bool

	// ConnectionGater optionally lets the caller refuse connections, e.g. to
	// enforce its own peer, address or subnet bans. libp2p consults it on
	// every outbound dial and inbound accept, so its methods must be fast and
	// must not block. Outbound dials are refused before any connection is
	// made. Inbound, an address or subnet is refused before any handshake,
	// but the remote peer ID is only known, and so only refused, after the
	// security handshake.
	//
	// Must be nil or a usable gater: a typed nil (e.g. a nil
	// *conngater.BasicConnectionGater) is installed and panics on first use.
	//
	// When AllowPrivateIPs is false the private-IP filter is still applied,
	// in front of this gater: a connection is admitted only if both admit it.
	//
	// Pair it with DisconnectPeer (see PeerDisconnector) to drop a peer that
	// is already connected; the gater only stops it coming back.
	// Default: nil (no caller gater).
	ConnectionGater connmgr.ConnectionGater

	// MaxConnections is the high water mark for total peer connections.
	// When this limit is reached, the connection manager will prune low-value connections
	// (DHT routing peers) while protecting high-value peers (topic mesh peers).
	// If not provided or zero, defaults to 35.
	// Recommended: 25-50 depending on available bandwidth and number of topics.
	MaxConnections int

	// MinConnections is the low water mark for total peer connections.
	// The connection manager will not prune connections below this threshold.
	// If not provided or zero, defaults to 25.
	// Recommended: Set to MaxConnections - 10 for a reasonable buffer.
	MinConnections int

	// ConnectionGracePeriod is the duration new connections are protected from pruning.
	// This prevents rapid connect/disconnect cycles.
	// If not provided or zero, defaults to 20 seconds.
	ConnectionGracePeriod time.Duration

	// ResourceLimits optionally overrides the libp2p resource manager's limits:
	// system-wide connection, stream and file-descriptor caps, per-peer caps,
	// and per-subnet connection caps. Unset fields keep go-libp2p's defaults,
	// which are auto-scaled to the host's memory and file-descriptor limits and
	// can be low on small hosts. Nodes that take heavy inbound churn, such as
	// bootstrap or DHT server nodes, should set ConnsInbound explicitly.
	// The effective system limits are logged at startup. See ResourceLimits.
	ResourceLimits ResourceLimits

	// EnablePeerScoring turns on GossipSub peer scoring using the library's built-in
	// defaults (DefaultPeerScoreParams / DefaultPeerScoreThresholds). Peer scoring is
	// what protects the mesh from Sybil peers: it applies an IP-colocation penalty and
	// a behaviour penalty for GRAFT/PRUNE flooding and broken IWANT promises, and it
	// gates peer exchange so peer records offered via PRUNE are rejected from any peer
	// that has earned a negative score (a Sybil swarm behind few IPs, or a misbehaving
	// peer). StaticPeers and BootstrapPeers are registered as GossipSub direct peers so
	// scoring can never prune or graylist trusted links.
	//
	// The gossipsub v1.1 spec requires scoring whenever peer exchange is on; leaving
	// this false while peer exchange stays enabled (the default) lets Sybil peers
	// capture the topic mesh at no cost. Prefer enabling this, or set
	// DisablePeerExchange, in any deployment exposed to untrusted peers.
	//
	// The defaults are penalty-only: a well-behaved peer sits at score 0, and only
	// misbehavior or IP colocation drives a score negative. To award positive score
	// to specific peers, or to add per-topic scoring, set PeerScoreParams /
	// AppSpecificScore. Default: false (no scoring, for backwards compatibility).
	EnablePeerScoring bool

	// PeerScoreParams optionally overrides the GossipSub peer score parameters. When
	// set, both this and PeerScoreThresholds must be provided. Setting this enables
	// peer scoring even if EnablePeerScoring is false. Use DefaultPeerScoreParams() as
	// a starting point and add per-topic TopicScoreParams (e.g. positive
	// TimeInMeshWeight / FirstMessageDeliveriesWeight, or a negative
	// InvalidMessageDeliveriesWeight) for your topics.
	PeerScoreParams *pubsub.PeerScoreParams

	// PeerScoreThresholds optionally overrides the GossipSub peer score thresholds.
	// When set, both this and PeerScoreParams must be provided.
	PeerScoreThresholds *pubsub.PeerScoreThresholds

	// AppSpecificScore optionally provides an application-specific score for a peer,
	// added to its GossipSub score (weighted by PeerScoreParams.AppSpecificWeight = 1
	// in the defaults). Use it to award positive score to trusted peers so transient
	// penalties cannot push them below the mesh-eligibility threshold. It is only used
	// when scoring is enabled; if set it overrides the params' AppSpecificScore.
	AppSpecificScore func(peer.ID) float64

	// PeerScoreInspect, if set, is called periodically with a snapshot of every peer's
	// score, for observability (e.g. exporting metrics or logging graylisted peers).
	// Only used when scoring is enabled. The period defaults to 10s.
	PeerScoreInspect func(map[peer.ID]*pubsub.PeerScoreSnapshot)

	// PeerScoreInspectPeriod is how often PeerScoreInspect is called. Defaults to 10s.
	PeerScoreInspectPeriod time.Duration

	// DisablePeerExchange turns off GossipSub peer exchange (PX). PX is enabled by
	// default. Without peer scoring, PX is strictly worse than no PX because a
	// zero AcceptPXThreshold makes the node dial every peer record an attacker hands
	// it in a PRUNE. Set this to true as an interim mitigation when peer scoring is not
	// enabled. Default: false (peer exchange enabled).
	DisablePeerExchange bool

	// TopicValidators optionally validates messages per topic before GossipSub
	// delivers or forwards them. The key is the topic name exactly as passed to
	// Subscribe and Publish. Without a validator a topic accepts any signed
	// message up to the pubsub wire limit and relays it to its mesh peers before
	// the subscriber ever sees it, so caps applied after Subscribe protect only
	// this node, never the network.
	//
	// Each validator bounds the payload size and may run a further check; see
	// TopicValidator. A failing message is rejected (ValidationReject), so it is
	// neither delivered nor forwarded. With peer scoring on, pubsub charges the
	// rejection to the peer that forwarded the message, and only on topics whose
	// TopicScoreParams weight invalid deliveries. Messages the AllowedPublisherIDs
	// filter drops are ignored, never rejected. Local publishes pass the same
	// validator, and a rejected Publish returns a pubsub.ValidationError.
	// Rejections are logged as one aggregate line per topic per interval.
	TopicValidators map[string]TopicValidator

	// MaxMessageSize optionally overrides the pubsub wire limit (default 1MiB,
	// pubsub.DefaultMaxMessageSize). Zero keeps the default.
	//
	// This limit bounds whole RPC frames, not single messages: a peer packs
	// several messages, IWANT replies and control traffic into one frame, sized
	// by its own limit. A frame over this node's limit resets the stream from
	// that peer, dropping everything in flight on it. The mismatch bites in both
	// directions: lowered below what other peers use, this node resets their
	// streams; raised above it, peers still on the default reset the streams
	// this node sends larger frames on. Use TopicValidators to bound message
	// sizes, and change this only on a network where every peer runs the same
	// limit.
	MaxMessageSize int
}
