<div align="center">

# 🛰&nbsp;&nbsp;go-p2p-message-bus

**Idiomatic Go P2P messaging library with auto-discovery, NAT traversal, and channel-based pub/sub.**

<br/>

<a href="https://github.com/bsv-blockchain/go-p2p-message-bus/releases"><img src="https://img.shields.io/github/release-pre/bsv-blockchain/go-p2p-message-bus?include_prereleases&style=flat-square&logo=github&color=black" alt="Release"></a>
<a href="https://golang.org/"><img src="https://img.shields.io/github/go-mod/go-version/bsv-blockchain/go-p2p-message-bus?style=flat-square&logo=go&color=00ADD8" alt="Go Version"></a>
<a href="LICENSE"><img src="https://img.shields.io/badge/license-OpenBSV-blue?style=flat-square&logo=springsecurity&logoColor=white" alt="License"></a>

<br/>

<table align="center" border="0">
  <tr>
    <td align="right">
       <code>CI / CD</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/bsv-blockchain/go-p2p-message-bus/actions"><img src="https://img.shields.io/github/actions/workflow/status/bsv-blockchain/go-p2p-message-bus/fortress.yml?branch=main&label=build&logo=github&style=flat-square" alt="Build"></a>
       <a href="https://github.com/bsv-blockchain/go-p2p-message-bus/actions"><img src="https://img.shields.io/github/last-commit/bsv-blockchain/go-p2p-message-bus?style=flat-square&logo=git&logoColor=white&label=last%20update" alt="Last Commit"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Quality</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://goreportcard.com/report/github.com/bsv-blockchain/go-p2p-message-bus"><img src="https://goreportcard.com/badge/github.com/bsv-blockchain/go-p2p-message-bus?style=flat-square" alt="Go Report"></a>
       <a href="https://codecov.io/gh/bsv-blockchain/go-p2p-message-bus"><img src="https://codecov.io/gh/bsv-blockchain/go-p2p-message-bus/branch/main/graph/badge.svg?style=flat-square" alt="Coverage"></a>
    </td>
  </tr>

  <tr>
    <td align="right">
       <code>Security</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://scorecard.dev/viewer/?uri=github.com/bsv-blockchain/go-p2p-message-bus"><img src="https://api.scorecard.dev/projects/github.com/bsv-blockchain/go-p2p-message-bus/badge?style=flat-square" alt="Scorecard"></a>
       <a href=".github/SECURITY.md"><img src="https://img.shields.io/badge/policy-active-success?style=flat-square&logo=security&logoColor=white" alt="Security"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Community</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/bsv-blockchain/go-p2p-message-bus/graphs/contributors"><img src="https://img.shields.io/github/contributors/bsv-blockchain/go-p2p-message-bus?style=flat-square&color=orange" alt="Contributors"></a>
       <a href="https://github.com/sponsors/bsv-blockchain"><img src="https://img.shields.io/badge/sponsor-BSV-181717.svg?logo=github&style=flat-square" alt="Sponsor"></a>
    </td>
  </tr>
</table>

</div>

<br/>
<br/>

<div align="center">

### <code>Project Navigation</code>

</div>

<table align="center">
  <tr>
    <td align="center" width="33%">
       📦&nbsp;<a href="#-installation"><code>Installation</code></a>
    </td>
    <td align="center" width="33%">
       🧪&nbsp;<a href="#-examples--tests"><code>Examples&nbsp;&&nbsp;Tests</code></a>
    </td>
    <td align="center" width="33%">
       📚&nbsp;<a href="#-documentation"><code>Documentation</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
       🤝&nbsp;<a href="#-contributing"><code>Contributing</code></a>
    </td>
    <td align="center">
       🛠️&nbsp;<a href="#-code-standards"><code>Code&nbsp;Standards</code></a>
    </td>
    <td align="center">
       ⚡&nbsp;<a href="#-benchmarks"><code>Benchmarks</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
       🤖&nbsp;<a href="#-ai-usage--assistant-guidelines"><code>AI&nbsp;Usage</code></a>
    </td>
    <td align="center">
       📝&nbsp;<a href="#-license"><code>License</code></a>
    </td>
    <td align="center">
       👥&nbsp;<a href="#-maintainers"><code>Maintainers</code></a>
    </td>
  </tr>
</table>
<br/>

## 🧩 What's Inside?

## Features

- **Simple API**: Create a client, subscribe to topics, and publish messages with minimal code
- **Channel-based**: Receive messages through Go channels for idiomatic concurrent programming
- **Auto-discovery**: Automatic peer discovery via DHT, mDNS, and peer caching
- **NAT traversal**: Built-in support for hole punching and relay connections
- **Persistent peers**: Automatically caches and reconnects to known peers
- **Connection limiting**: Smart connection manager prioritizes topic peers over routing peers (default: 25-35 connections)

<br/>

## 🚀 Quick Start

<details>
<summary><strong>Get started in 60 seconds</strong></summary>
<br/>

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

</details>

<br/>

## 📦 Installation

**go-p2p-message-bus** requires a [supported release of Go](https://golang.org/doc/devel/release.html#policy).
```shell script
go get -u github.com/bsv-blockchain/go-p2p-message-bus
```

<br/>

## 📚 Documentation

- **API Reference** – Dive into the godocs at [pkg.go.dev/github.com/bsv-blockchain/go-p2p-message-bus](https://pkg.go.dev/github.com/bsv-blockchain/go-p2p-message-bus)
- **Usage Examples** – Browse practical patterns either the [examples directory](cmd/example) or the example tests
- **Benchmarks** – Check the latest numbers in the benchmark results
- **Test Suite** – Review both the unit tests and fuzz tests (powered by [`testify`](https://github.com/stretchr/testify))

<details>
<summary><strong><code>API Reference</code></strong></summary>
<br/>

<details>
<summary><strong>Config</strong></summary>
<br/>

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

</details>

<details>
<summary><strong>Client Methods</strong></summary>
<br/>

**GeneratePrivateKeyHex**

```go
func GeneratePrivateKeyHex() (string, error)
```

Generates a new Ed25519 private key and returns it as a hex string. Use this function to create a new key for `Config.PrivateKeyHex` when setting up a new peer for the first time.

**NewPeer**

```go
func NewPeer(config Config) (*Client, error)
```

Creates and starts a new P2P client. The client automatically:
- Creates a libp2p host with NAT traversal support
- Bootstraps to the DHT network
- Starts peer discovery (DHT + mDNS)
- Connects to cached peers from previous sessions

**Note:** Requires `Config.PrivateKeyHex` to be set. Use `GeneratePrivateKeyHex()` to create a new key.

**Subscribe**

```go
func (c *Client) Subscribe(topic string) <-chan Message
```

Subscribes to a topic and returns a channel that receives messages. The channel is closed when the client is closed.

**Publish**

```go
func (c *Client) Publish(topic string, data []byte) error
```

Publishes a message to a topic. The message is broadcast to all peers subscribed to the topic.

**GetPeers**

```go
func (c *Client) GetPeers() []PeerInfo
```

Returns information about all known peers on subscribed topics.

**GetID**

```go
func (c *Client) GetID() string
```

Returns this peer's ID as a string.

**GetPrivateKeyHex**

```go
func (c *Client) GetPrivateKeyHex() (string, error)
```

Returns the hex-encoded private key for this peer. This can be saved and used in `Config.PrivateKey` to maintain the same peer ID across restarts.

**Close**

```go
func (c *Client) Close() error
```

Shuts down the client and releases all resources.

</details>

<details>
<summary><strong>Data Types</strong></summary>
<br/>

**Message**

```go
type Message struct {
    Topic     string    // Topic this message was received on
    From      string    // Sender's name
    FromID    string    // Sender's peer ID
    Data      []byte    // Message payload
    Timestamp time.Time // When the message was received
}
```

**PeerInfo**

```go
type PeerInfo struct {
    ID    string   // Peer ID
    Name  string   // Peer name (if known)
    Addrs []string // Peer addresses
}
```

</details>

</details>

<br/>

<details>
<summary><strong><code>Development Build Commands</code></strong></summary>
<br/>

Get the [MAGE-X](https://github.com/mrz1836/mage-x) build tool for development:
```shell script
go install github.com/mrz1836/mage-x/cmd/magex@latest
```

View all build commands

```bash script
magex help
```

</details>

<details>
<summary><strong>Repository Features</strong></summary>
<br/>

This repository includes 25+ built-in features covering CI/CD, security, code quality, developer experience, and community tooling.

**[View the full Repository Features list →](.github/docs/repository-features.md)**

</details>

<details>
<summary><strong><code>Library Deployment</code></strong></summary>
<br/>

This project uses [goreleaser](https://github.com/goreleaser/goreleaser) for streamlined binary and library deployment to GitHub. To get started, install it via:

```bash
brew install goreleaser
```

The release process is defined in the [.goreleaser.yml](.goreleaser.yml) configuration file.


Then create and push a new Git tag using:

```bash
magex version:bump push=true bump=patch branch=main
```

This process ensures consistent, repeatable releases with properly versioned artifacts and citation metadata.

</details>

<details>
<summary><strong><code>Pre-commit Hooks</code></strong></summary>
<br/>

Set up the Go-Pre-commit System to run the same formatting, linting, and tests defined in [AGENTS.md](.github/AGENTS.md) before every commit:

```bash
go install github.com/mrz1836/go-pre-commit/cmd/go-pre-commit@latest
go-pre-commit install
```

The system is configured via modular [environment files](.github/env/README.md) and provides 17x faster execution than traditional Python-based pre-commit hooks. See the [complete documentation](http://github.com/mrz1836/go-pre-commit) for details.

</details>

<details>
<summary><strong>GitHub Workflows</strong></summary>
<br/>

All workflows are driven by modular configuration in [`.github/env/`](.github/env/README.md) — no YAML editing required.

**[View all workflows and the control center →](.github/docs/workflows.md)**

</details>

<details>
<summary><strong><code>Updating Dependencies</code></strong></summary>
<br/>

To update all dependencies (Go modules, linters, and related tools), run:

```bash
magex deps:update
```

This command ensures all dependencies are brought up to date in a single step, including Go modules and any tools managed by [MAGE-X](https://github.com/mrz1836/mage-x). It is the recommended way to keep your development environment and CI in sync with the latest versions.

</details>

<br/>

## 🔧 How It Works

<details>
<summary><strong>Peer Discovery, NAT Traversal, and Message Routing</strong></summary>
<br/>

**Peer Discovery**

The library uses multiple discovery mechanisms:
- **DHT**: Connects to IPFS bootstrap peers and advertises topics on the distributed hash table
- **mDNS**: Discovers peers on the local network
- **Peer Cache**: Persists peer information to disk for faster reconnection across restarts

**NAT Traversal**

Automatically handles NAT traversal through:
- **Hole Punching**: Attempts direct connections between NAT'd peers
- **Relay**: Falls back to relay connections when direct connections fail
- **UPnP/NAT-PMP**: Automatically configures port forwarding when possible

**Message Routing**

Uses GossipSub for efficient topic-based message propagation:
- Messages are distributed using an optimized gossip protocol
- Reduces bandwidth while maintaining reliability
- Automatically handles peer mesh management and scoring

</details>

<br/>

## 🧪 Examples & Tests

All unit tests and [examples](cmd/example) run via [GitHub Actions](https://github.com/bsv-blockchain/go-p2p-message-bus/actions) and use [Go version 1.24.x](https://go.dev/doc/go1.24). View the [configuration file](.github/workflows/fortress.yml).

Run all tests (fast):

```bash script
magex test
```

Run all tests with race detector (slower):
```bash script
magex test:race
```

<details>
<summary><strong><code>Running the Example</code></strong></summary>
<br/>

See [cmd/example/main.go](cmd/example/main.go) for a complete working example.

To run the example:

```bash
go run ./cmd/example -name "node1"
```

In another terminal:

```bash
go run ./cmd/example -name "node2"
```

The two nodes will discover each other and exchange messages.

</details>

<br/>

## ⚡ Benchmarks

Run the Go benchmarks:

```bash script
magex bench
```

<br/>

## 🛠️ Code Standards
Read more about this Go project's [code standards](.github/CODE_STANDARDS.md).

<br/>

## 🤖 AI Usage & Assistant Guidelines
Read the [AI Usage & Assistant Guidelines](.github/tech-conventions/ai-compliance.md) for details on how AI is used in this project and how to interact with AI assistants.

<br/>

## 👥 Maintainers
| [<img src="https://github.com/icellan.png" height="50" alt="Siggi" />](https://github.com/icellan) | [<img src="https://github.com/galt-tr.png" height="50" alt="Galt" />](https://github.com/galt-tr) | [<img src="https://github.com/mrz1836.png" height="50" alt="MrZ" />](https://github.com/mrz1836) |
|:--------------------------------------------------------------------------------------------------:|:-------------------------------------------------------------------------------------------------:|:------------------------------------------------------------------------------------------------:|
|                                [Siggi](https://github.com/icellan)                                 |                                [Dylan](https://github.com/galt-tr)                                 |                                [MrZ](https://github.com/mrz1836)                                 |

<br/>

## 🤝 Contributing
View the [contributing guidelines](.github/CONTRIBUTING.md) and please follow the [code of conduct](.github/CODE_OF_CONDUCT.md).

### How can I help?
All kinds of contributions are welcome :raised_hands:!
The most basic way to show your support is to star :star2: the project, or to raise issues :speech_balloon:.

[![Stars](https://img.shields.io/github/stars/bsv-blockchain/go-p2p-message-bus?label=Please%20like%20us&style=social&v=1)](https://github.com/bsv-blockchain/go-p2p-message-bus/stargazers)

<br/>

## 📝 License

[![License](https://img.shields.io/badge/license-OpenBSV-blue?style=flat&logo=springsecurity&logoColor=white)](LICENSE)
