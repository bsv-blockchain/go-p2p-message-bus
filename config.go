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

func (d *DefaultLogger) Debugf(format string, v ...any) { log.Printf("[DEBUG] "+format, v...) }
func (d *DefaultLogger) Infof(format string, v ...any)  { log.Printf("[INFO] "+format, v...) }
func (d *DefaultLogger) Warnf(format string, v ...any)  { log.Printf("[WARN] "+format, v...) }
func (d *DefaultLogger) Errorf(format string, v ...any) { log.Printf("[ERROR] "+format, v...) }

// Config contains the configuration options for creating a P2P client.
type Config struct {
	// Name is a required identifier for this peer. It will be included in messages
	// so other peers can identify the sender.
	Name string

	// BootstrapPeers is an optional list of multiaddr strings for initial peers to connect to.
	// If not provided, the client will use libp2p's default bootstrap peers.
	// Example: []string{"/ip4/192.168.1.100/tcp/4001/p2p/QmPeerID"}
	BootstrapPeers []string

	// Logger is an optional logger to use for logging. If not provided, the client will use
	// DefaultLogger. Set to a custom implementation to integrate with your logging framework.
	Logger logger

	// PrivateKey is a required private key for the peer.
	// This ensures the peer ID remains consistent across restarts.
	// Use GeneratePrivateKeyHex() to create a new key for first-time setup.
	PrivateKey crypto.PrivKey

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

	// PeerCacheTTL is the duration after which unseen peers are removed from the cache.
	// Peers not seen for longer than this duration will be evicted on next cache load.
	// If not provided or zero, defaults to 24 hours (same as go-ethereum).
	// Set to a negative value to disable TTL-based eviction.
	PeerCacheTTL time.Duration
}
