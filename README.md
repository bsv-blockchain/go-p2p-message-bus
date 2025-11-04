# 🛰 go-p2p-message-bus
> Idiomatic Go P2P messaging library with auto-discovery, NAT traversal, and channel-based pub/sub

<table>
  <thead>
    <tr>
      <th>CI&nbsp;/&nbsp;CD</th>
      <th>Quality&nbsp;&amp;&nbsp;Security</th>
      <th>Docs&nbsp;&amp;&nbsp;Meta</th>
      <th>Community</th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td valign="top" align="left">
        <a href="https://github.com/bsv-blockchain/go-p2p-message-bus/releases">
          <img src="https://img.shields.io/github/release-pre/bsv-blockchain/go-p2p-message-bus?logo=github&style=flat" alt="Latest Release">
        </a><br/>
        <a href="https://github.com/bsv-blockchain/go-p2p-message-bus/actions">
          <img src="https://img.shields.io/github/actions/workflow/status/bsv-blockchain/go-p2p-message-bus/fortress.yml?branch=main&logo=github&style=flat" alt="Build Status">
        </a><br/>
		    <a href="https://github.com/bsv-blockchain/go-p2p-message-bus/actions">
          <img src="https://github.com/bsv-blockchain/go-p2p-message-bus/actions/workflows/codeql-analysis.yml/badge.svg?style=flat" alt="CodeQL">
        </a><br/>
		    <a href="https://sonarcloud.io/project/overview?id=bsv-blockchain_go-p2p-message-bus">
          <img src="https://sonarcloud.io/api/project_badges/measure?project=bsv-blockchain_go-p2p-message-bus&metric=alert_status&style-flat" alt="SonarCloud">
        </a>
      </td>
      <td valign="top" align="left">
        <a href="https://goreportcard.com/report/github.com/bsv-blockchain/go-p2p-message-bus">
          <img src="https://goreportcard.com/badge/github.com/bsv-blockchain/go-p2p-message-bus?style=flat" alt="Go Report Card">
        </a><br/>
		    <a href="https://codecov.io/gh/bsv-blockchain/go-p2p-message-bus/tree/main">
          <img src="https://codecov.io/gh/bsv-blockchain/go-p2p-message-bus/branch/main/graph/badge.svg?style=flat" alt="Code Coverage">
        </a><br/>
		    <a href="https://scorecard.dev/viewer/?uri=github.com/bsv-blockchain/go-p2p-message-bus">
          <img src="https://api.scorecard.dev/projects/github.com/bsv-blockchain/go-p2p-message-bus/badge?logo=springsecurity&logoColor=white" alt="OpenSSF Scorecard">
        </a><br/>
		    <a href=".github/SECURITY.md">
          <img src="https://img.shields.io/badge/security-policy-blue?style=flat&logo=springsecurity&logoColor=white" alt="Security policy">
        </a>
      </td>
      <td valign="top" align="left">
        <a href="https://golang.org/">
          <img src="https://img.shields.io/github/go-mod/go-version/bsv-blockchain/go-p2p-message-bus?style=flat" alt="Go version">
        </a><br/>
        <a href="https://pkg.go.dev/github.com/bsv-blockchain/go-p2p-message-bus?tab=doc">
          <img src="https://pkg.go.dev/badge/github.com/bsv-blockchain/go-p2p-message-bus.svg?style=flat" alt="Go docs">
        </a><br/>
        <a href=".github/AGENTS.md">
          <img src="https://img.shields.io/badge/AGENTS.md-found-40b814?style=flat&logo=openai" alt="AGENTS.md rules">
        </a><br/>
        <a href="https://magefile.org/">
          <img src="https://img.shields.io/badge/mage-powered-brightgreen?style=flat&logo=probot&logoColor=white" alt="Mage Powered">
        </a><br/>
		    <a href=".github/dependabot.yml">
          <img src="https://img.shields.io/badge/dependencies-automatic-blue?logo=dependabot&style=flat" alt="Dependabot">
        </a>
      </td>
      <td valign="top" align="left">
        <a href="https://github.com/bsv-blockchain/go-p2p-message-bus/graphs/contributors">
          <img src="https://img.shields.io/github/contributors/bsv-blockchain/go-p2p-message-bus?style=flat&logo=contentful&logoColor=white" alt="Contributors">
        </a><br/>
        <a href="https://github.com/bsv-blockchain/go-p2p-message-bus/commits/main">
          <img src="https://img.shields.io/github/last-commit/bsv-blockchain/go-p2p-message-bus?style=flat&logo=clockify&logoColor=white" alt="Last commit">
        </a><br/>
        <a href="https://github.com/sponsors/bsv-blockchain">
          <img src="https://img.shields.io/badge/sponsor-BSV-181717.svg?logo=github&style=flat" alt="Sponsor">
        </a><br/>
      </td>
    </tr>
  </tbody>
</table>

<br/>

## 🗂️ Table of Contents
* [What's Inside?](#-whats-inside)
* [Quick Start](#-quick-start)
* [Installation](#-installation)
* [Documentation](#-documentation)
* [How It Works](#-how-it-works)
* [Examples & Tests](#-examples--tests)
* [Benchmarks](#-benchmarks)
* [Code Standards](#-code-standards)
* [AI Compliance](#-ai-compliance)
* [Maintainers](#-maintainers)
* [Contributing](#-contributing)
* [License](#-license)

<br/>

## 🧩 What's Inside?

## Features

- **Simple API**: Create a client, subscribe to topics, and publish messages with minimal code
- **Channel-based**: Receive messages through Go channels for idiomatic concurrent programming
- **Auto-discovery**: Automatic peer discovery via DHT, mDNS, and peer caching
- **NAT traversal**: Built-in support for hole punching and relay connections
- **Persistent peers**: Automatically caches and reconnects to known peers

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
<summary><strong><code>Repository Features</code></strong></summary>
<br/>

* **Continuous Integration on Autopilot** with [GitHub Actions](https://github.com/features/actions) – every push is built, tested, and reported in minutes.
* **Pull‑Request Flow That Merges Itself** thanks to [auto‑merge](.github/workflows/auto-merge-on-approval.yml) and hands‑free [Dependabot auto‑merge](.github/workflows/dependabot-auto-merge.yml).
* **One‑Command Builds** powered by battle‑tested [MAGE-X](https://github.com/mrz1836/mage-x) targets for linting, testing, releases, and more.
* **First‑Class Dependency Management** using native [Go Modules](https://github.com/golang/go/wiki/Modules).
* **Uniform Code Style** via [gofumpt](https://github.com/mvdan/gofumpt) plus zero‑noise linting with [golangci‑lint](https://github.com/golangci/golangci-lint).
* **Confidence‑Boosting Tests** with [testify](https://github.com/stretchr/testify), the Go [race detector](https://blog.golang.org/race-detector), crystal‑clear [HTML coverage](https://blog.golang.org/cover) snapshots, and automatic uploads to [Codecov](https://codecov.io/).
* **Hands‑Free Releases** delivered by [GoReleaser](https://github.com/goreleaser/goreleaser) whenever you create a [new Tag](https://git-scm.com/book/en/v2/Git-Basics-Tagging).
* **Relentless Dependency & Vulnerability Scans** via [Dependabot](https://dependabot.com), [Nancy](https://github.com/sonatype-nexus-community/nancy) and [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck).
* **Security Posture by Default** with [CodeQL](https://docs.github.com/en/github/finding-security-vulnerabilities-and-errors-in-your-code/about-code-scanning), [OpenSSF Scorecard](https://openssf.org) and secret‑leak detection via [gitleaks](https://github.com/gitleaks/gitleaks).
* **Automatic Syndication** to [pkg.go.dev](https://pkg.go.dev/) on every release for instant godoc visibility.
* **Polished Community Experience** using rich templates for [Issues & PRs](https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/configuring-issue-templates-for-your-repository).
* **All the Right Meta Files** (`LICENSE`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SUPPORT.md`, `SECURITY.md`) pre‑filled and ready.
* **Code Ownership** clarified through a [CODEOWNERS](.github/CODEOWNERS) file, keeping reviews fast and focused.
* **Zero‑Noise Dev Environments** with tuned editor settings (`.editorconfig`) plus curated *ignore* files for [VS Code](.editorconfig), [Docker](.dockerignore), and [Git](.gitignore).
* **Label Sync Magic**: your repo labels stay in lock‑step with [.github/labels.yml](.github/labels.yml).
* **Friendly First PR Workflow** – newcomers get a warm welcome thanks to a dedicated [workflow](.github/workflows/pull-request-management.yml).
* **Standards‑Compliant Docs** adhering to the [standard‑readme](https://github.com/RichardLitt/standard-readme/blob/main/spec.md) spec.
* **Instant Cloud Workspaces** via [Gitpod](https://gitpod.io/) – spin up a fully configured dev environment with automatic linting and tests.
* **Out‑of‑the‑Box VS Code Happiness** with a preconfigured [Go](https://code.visualstudio.com/docs/languages/go) workspace and [`.vscode`](.vscode) folder with all the right settings.
* **Optional Release Broadcasts** to your community via [Slack](https://slack.com), [Discord](https://discord.com), or [Twitter](https://twitter.com) – plug in your webhook.
* **AI Compliance Playbook** – machine‑readable guidelines ([AGENTS.md](.github/AGENTS.md), [CLAUDE.md](.github/CLAUDE.md), [.cursorrules](.cursorrules), [sweep.yaml](.github/sweep.yaml)) keep ChatGPT, Claude, Cursor & Sweep aligned with your repo's rules.
* **Go-Pre-commit System** - [High-performance Go-native pre-commit hooks](https://github.com/mrz1836/go-pre-commit) with 17x faster execution—run the same formatting, linting, and tests before every commit, just like CI.
* **Zero Python Dependencies** - Pure Go implementation with environment-based configuration via [.env.base](.github/.env.base).
* **DevContainers for Instant Onboarding** – Launch a ready-to-code environment in seconds with [VS Code DevContainers](https://containers.dev/) and the included [.devcontainer.json](.devcontainer.json) config.

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

The system is configured via [.env.base](.github/.env.base) and can be customized using also using [.env.custom](.github/.env.custom) and provides 17x faster execution than traditional Python-based pre-commit hooks. See the [complete documentation](http://github.com/mrz1836/go-pre-commit) for details.

</details>

<details>
<summary><strong><code>GitHub Workflows</code></strong></summary>
<br/>

### 🎛️ The Workflow Control Center

All GitHub Actions workflows in this repository are powered by a single configuration files – your one-stop shop for tweaking CI/CD behavior without touching a single YAML file! 🎯

**Configuration Files:**
- **[.env.base](.github/.env.base)** – Default configuration that works for most Go projects
- **[.env.custom](.github/.env.custom)** – Optional project-specific overrides

This magical file controls everything from:
- **⚙️ Go version matrix** (test on multiple versions or just one)
- **🏃 Runner selection** (Ubuntu or macOS, your wallet decides)
- **🔬 Feature toggles** (coverage, fuzzing, linting, race detection, benchmarks)
- **🛡️ Security tool versions** (gitleaks, nancy, govulncheck)
- **🤖 Auto-merge behaviors** (how aggressive should the bots be?)
- **🏷️ PR management rules** (size labels, auto-assignment, welcome messages)

<br/>

| Workflow Name                                                                      | Description                                                                                                            |
|------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------|
| [auto-merge-on-approval.yml](.github/workflows/auto-merge-on-approval.yml)         | Automatically merges PRs after approval and all required checks, following strict rules.                               |
| [codeql-analysis.yml](.github/workflows/codeql-analysis.yml)                       | Analyzes code for security vulnerabilities using [GitHub CodeQL](https://codeql.github.com/).                          |
| [dependabot-auto-merge.yml](.github/workflows/dependabot-auto-merge.yml)           | Automatically merges [Dependabot](https://github.com/dependabot) PRs that meet all requirements.                       |
| [fortress.yml](.github/workflows/fortress.yml)                                     | Runs the GoFortress security and testing workflow, including linting, testing, releasing, and vulnerability checks.    |
| [pull-request-management.yml](.github/workflows/pull-request-management.yml)       | Labels PRs by branch prefix, assigns a default user if none is assigned, and welcomes new contributors with a comment. |
| [scorecard.yml](.github/workflows/scorecard.yml)                                   | Runs [OpenSSF](https://openssf.org/) Scorecard to assess supply chain security.                                        |
| [stale.yml](.github/workflows/stale-check.yml)                                     | Warns about (and optionally closes) inactive issues and PRs on a schedule or manual trigger.                           |
| [sync-labels.yml](.github/workflows/sync-labels.yml)                               | Keeps GitHub labels in sync with the declarative manifest at [`.github/labels.yml`](./.github/labels.yml).             |

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

## 🤖 AI Compliance
This project documents expectations for AI assistants using a few dedicated files:

- [AGENTS.md](.github/AGENTS.md) — canonical rules for coding style, workflows, and pull requests used by [Codex](https://chatgpt.com/codex).
- [CLAUDE.md](.github/CLAUDE.md) — quick checklist for the [Claude](https://www.anthropic.com/product) agent.
- [.cursorrules](.cursorrules) — machine-readable subset of the policies for [Cursor](https://www.cursor.so/) and similar tools.
- [sweep.yaml](.github/sweep.yaml) — rules for [Sweep](https://github.com/sweepai/sweep), a tool for code review and pull request management.

Edit `AGENTS.md` first when adjusting these policies, and keep the other files in sync within the same pull request.

<br/>

## 👥 Maintainers
| [<img src="https://github.com/mrz1836.png" height="50" width="50" alt="MrZ" />](https://github.com/mrz1836) | [<img src="https://github.com/icellan.png" height="50" alt="Siggi" />](https://github.com/icellan) |
|:-----------------------------------------------------------------------------------------------------------:|:--------------------------------------------------------------------------------------------------:|
|                                      [MrZ](https://github.com/mrz1836)                                      |                                [Siggi](https://github.com/icellan)                                 |

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
