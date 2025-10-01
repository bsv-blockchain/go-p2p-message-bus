# P2P Messaging Library

A simple, channel-based peer-to-peer messaging library built on libp2p.

## Features

- **Simple API**: Create a client, subscribe to topics, and publish messages with minimal code
- **Channel-based**: Receive messages through Go channels for idiomatic concurrent programming
- **Auto-discovery**: Automatic peer discovery via DHT, mDNS, and peer caching
- **NAT traversal**: Built-in support for hole punching and relay connections
- **Persistent peers**: Automatically caches and reconnects to known peers

## Installation

```bash
go get github.com/bsv-blockchain/go-p2p-message-bus
```

## Quick Start

```go
package main

import (
    "fmt"
    "log"

    "github.com/bsv-blockchain/go-p2p-message-bus"
)

func main() {
    // Generate a private key (do this once and save it)
    keyHex, err := p2p.GeneratePrivateKeyHex()
    if err != nil {
        log.Fatal(err)
    }
    // In production, save keyHex to config file, env var, or database

    // Create a P2P client
    client, err := p2p.NewPeer(p2p.Config{
        Name:          "my-node",
        PrivateKeyHex: keyHex,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    // Subscribe to a topic
    msgChan := client.Subscribe("my-topic")

    // Receive messages
    go func() {
        for msg := range msgChan {
            fmt.Printf("Received from %s: %s\n", msg.From, string(msg.Data))
        }
    }()

    // Publish a message
    if err := client.Publish("my-topic", []byte("Hello, P2P!")); err != nil {
        log.Printf("Error publishing: %v", err)
    }

    // Get connected peers
    peers := client.GetPeers()
    for _, peer := range peers {
        fmt.Printf("Peer: %s [%s]\n", peer.Name, peer.ID)
    }

    select {} // Wait forever
}
```

## API Reference

### Config

```go
type Config struct {
    Name           string         // Required: identifier for this peer
    BootstrapPeers []string       // Optional: initial peers to connect to
    Logger         Logger         // Optional: custom logger (uses DefaultLogger if not provided)
    PrivateKey     crypto.PrivKey // Required: private key for persistent peer ID
    PeerCacheFile  string         // Optional: file path for peer persistence
    AnnounceAddrs  []string       // Optional: addresses to advertise to peers (for K8s)
}
```

**Logger Interface:**

The library defines a `Logger` interface and provides a `DefaultLogger` implementation:

```go
type Logger interface {
    Debugf(format string, v ...any)
    Infof(format string, v ...any)
    Warnf(format string, v ...any)
    Errorf(format string, v ...any)
}

// DefaultLogger is provided out of the box
logger := &p2p.DefaultLogger{}

// Or use your own custom logger that implements the interface
```

**Persistent Peer Identity:**

The `PrivateKeyHex` field is **required** to ensure consistent peer IDs across restarts:

```go
// Generate a new key for first-time setup
keyHex, err := p2p.GeneratePrivateKeyHex()
if err != nil {
    log.Fatal(err)
}
// Save keyHex somewhere (env var, config file, database, etc.)

// Create client with the saved key
client, err := p2p.NewPeer(p2p.Config{
    Name:          "node1",
    PrivateKeyHex: keyHex,
})

// You can also retrieve the key from an existing client
retrievedKey, _ := client.GetPrivateKeyHex()
```

**Peer Persistence:**

The `PeerCacheFile` field is optional and enables peer persistence for faster reconnection:

```go
client, err := p2p.NewPeer(p2p.Config{
    Name:          "node1",
    PrivateKey:    privKey,
    PeerCacheFile: "peers.json", // Enable peer caching
})
```

When enabled:
- Connected peers are automatically saved to the specified file
- On restart, the client will reconnect to previously known peers
- This significantly speeds up network reconnection
- If not provided, peer caching is disabled

**Kubernetes Support:**

The `AnnounceAddrs` field allows you to specify the external addresses that your peer should advertise. This is essential in Kubernetes where the pod's internal IP differs from the externally accessible address:

```go
// Get external address from environment or K8s service
externalIP := os.Getenv("EXTERNAL_IP")      // e.g., "203.0.113.1"
externalPort := os.Getenv("EXTERNAL_PORT")  // e.g., "30001"

client, err := p2p.NewPeer(p2p.Config{
    Name:       "node1",
    PrivateKey: privKey,
    AnnounceAddrs: []string{
        fmt.Sprintf("/ip4/%s/tcp/%s", externalIP, externalPort),
    },
})
```

Common Kubernetes scenarios:
- **LoadBalancer Service**: Use the external IP of the LoadBalancer
- **NodePort Service**: Use the node's external IP and the NodePort
- **Ingress with TCP**: Use the ingress external IP and configured port

Without `AnnounceAddrs`, libp2p will announce the pod's internal IP, which won't be reachable from outside the cluster.

### Client

#### GeneratePrivateKeyHex

```go
func GeneratePrivateKeyHex() (string, error)
```

Generates a new Ed25519 private key and returns it as a hex string. Use this function to create a new key for `Config.PrivateKeyHex` when setting up a new peer for the first time.

#### NewPeer

```go
func NewPeer(config Config) (*Client, error)
```

Creates and starts a new P2P client. The client automatically:
- Creates a libp2p host with NAT traversal support
- Bootstraps to the DHT network
- Starts peer discovery (DHT + mDNS)
- Connects to cached peers from previous sessions

**Note:** Requires `Config.PrivateKeyHex` to be set. Use `GeneratePrivateKeyHex()` to create a new key.

#### Subscribe

```go
func (c *Client) Subscribe(topic string) <-chan Message
```

Subscribes to a topic and returns a channel that receives messages. The channel is closed when the client is closed.

#### Publish

```go
func (c *Client) Publish(topic string, data []byte) error
```

Publishes a message to a topic. The message is broadcast to all peers subscribed to the topic.

#### GetPeers

```go
func (c *Client) GetPeers() []PeerInfo
```

Returns information about all known peers on subscribed topics.

#### GetID

```go
func (c *Client) GetID() string
```

Returns this peer's ID as a string.

#### GetPrivateKeyHex

```go
func (c *Client) GetPrivateKeyHex() (string, error)
```

Returns the hex-encoded private key for this peer. This can be saved and used in `Config.PrivateKey` to maintain the same peer ID across restarts.

#### Close

```go
func (c *Client) Close() error
```

Shuts down the client and releases all resources.

### Message

```go
type Message struct {
    Topic     string    // Topic this message was received on
    From      string    // Sender's name
    FromID    string    // Sender's peer ID
    Data      []byte    // Message payload
    Timestamp time.Time // When the message was received
}
```

### PeerInfo

```go
type PeerInfo struct {
    ID    string   // Peer ID
    Name  string   // Peer name (if known)
    Addrs []string // Peer addresses
}
```

## Example

See [example/main.go](example/main.go) for a complete working example.

To run the example:

```bash
cd example
go run main.go -name "node1"
```

In another terminal:

```bash
cd example
go run main.go -name "node2"
```

The two nodes will discover each other and exchange messages.

## How It Works

1. **Peer Discovery**: The library uses multiple discovery mechanisms:
   - **DHT**: Connects to IPFS bootstrap peers and advertises topics on the distributed hash table
   - **mDNS**: Discovers peers on the local network
   - **Peer Cache**: Persists peer information to `peer_cache.json` for faster reconnection

2. **NAT Traversal**: Automatically handles NAT traversal through:
   - **Hole Punching**: Attempts direct connections between NAT'd peers
   - **Relay**: Falls back to relay connections when direct connections fail
   - **UPnP/NAT-PMP**: Automatically configures port forwarding when possible

3. **Message Routing**: Uses GossipSub for efficient topic-based message propagation

## License

MIT
