package p2p

import (
	"log"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
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
	// Peers not seen for longer than this duration will be evicted on next cache load.
	// If not provided or zero, defaults to 24 hours (same as go-ethereum).
	// Set to a negative value to disable TTL-based eviction.
	PeerCacheTTL time.Duration

	// BootstrapPeers is an optional list of multiaddr strings for bootstrap servers.
	// If provided, these peers will be used for DHT bootstrapping and relay/circuit functionality.
	// If not provided, default IPFS bootstrap peers will be used for bootstrapping and relay.
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
}
