package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	p2p "github.com/bsv-blockchain/go-p2p-message-bus"
	"github.com/libp2p/go-libp2p/core/crypto"
)

func main() {
	name := flag.String("name", "", "Your node name")
	privateKey := flag.String("key", "", "Private key hex (will generate if not provided)")
	topics := flag.String("topics", "broadcast_p2p_poc", "Comma-separated list of topics to subscribe to")
	port := flag.Int("port", 0, "port to listen on (0 for random)")
	noBroadcast := flag.Bool("no-broadcast", false, "Disable message broadcasting")
	prettyJson := flag.Bool("pretty-json", false, "Pretty print JSON messages")
	relays := flag.String("relays", "", "Comma-separated list of relay peer multiaddrs (e.g., /ip4/1.2.3.4/tcp/4001/p2p/PeerID)")

	flag.Parse()

	logger := &p2p.DefaultLogger{}

	if *name == "" {
		logger.Errorf("--name flag is required")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get or generate private key
	keyHex := *privateKey
	if keyHex == "" {
		// Try to load from environment variable
		keyHex = os.Getenv("P2P_PRIVATE_KEY")
	}

	var privKey crypto.PrivKey
	var err error

	if keyHex == "" {
		// Generate a new key
		privKey, err = p2p.GeneratePrivateKey()
		if err != nil {
			logger.Errorf("Failed to generate private key: %v", err)
			return
		}

		keyHex, _ = p2p.PrivateKeyToHex(privKey)
		logger.Infof("Generated new private key: %s\n", keyHex)
		logger.Infof("Save this key and use it next time with --key flag or P2P_PRIVATE_KEY env var")
	} else {
		// Load key from hex
		privKey, err = p2p.PrivateKeyFromHex(keyHex)
		if err != nil {
			logger.Errorf("Failed to load private key: %v", err)
			return
		}
	}

	// Parse relay peers list
	var relayPeers []string
	if *relays != "" {
		for _, r := range strings.Split(*relays, ",") {
			relayPeers = append(relayPeers, strings.TrimSpace(r))
		}
	}

	// Create P2P client
	client, err := p2p.NewClient(p2p.Config{
		Name:          *name,
		Logger:        logger,
		PrivateKey:    privKey,
		Port:          *port,
		PeerCacheFile: "peer_cache.json", // Enable peer persistence
		RelayPeers:    relayPeers,
	})
	if err != nil {
		logger.Errorf("Failed to create P2P client: %v", err)
		return
	}

	defer client.Close()

	// Parse topics list
	topicList := strings.Split(*topics, ",")
	for i, t := range topicList {
		topicList[i] = strings.TrimSpace(t)
	}

	// Subscribe to all topics and merge messages into single channel
	allMsgChan := make(chan p2p.Message, 100)

	for _, topic := range topicList {
		msgChan := client.Subscribe(topic)
		logger.Infof("Subscribed to topic: %s", topic)

		go func(ch <-chan p2p.Message) {
			for msg := range ch {
				allMsgChan <- msg
			}
		}(msgChan)
	}

	// Start message receiver
	go func() {
		for msg := range allMsgChan {
			var data string

			if *prettyJson {
				// Try to unmarshal and re-marshal for pretty printing
				var jsonObj interface{}
				if err := json.Unmarshal(msg.Data, &jsonObj); err == nil {
					if jsonBytes, err := json.MarshalIndent(jsonObj, "", "  "); err == nil {
						data = string(jsonBytes)
					} else {
						data = string(msg.Data)
					}
				} else {
					data = string(msg.Data)
				}
			} else {
				data = string(msg.Data)
			}

			logger.Infof("[%-52s] %s: (%s)\n%s", msg.FromID, msg.From, msg.Topic, data)
		}
	}()

	// Start message broadcaster (publishes to all topics)
	if !*noBroadcast {
		go func() {
			counter := 0
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					counter++
					data := fmt.Sprintf("Message #%d", counter)

					for _, topic := range topicList {
						if err := client.Publish(ctx, topic, []byte(data)); err != nil {
							return
						}
					}
					logger.Infof("[%-52s] %s: %s\n", "local", *name, data)
				}
			}
		}()
	}

	// Periodically display peer information
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				peers := client.GetPeers()
				if len(peers) > 0 {
					sb := strings.Builder{}
					sb.WriteString(fmt.Sprintf("\n=== Connected Peers: %d ===\n", len(peers)))
					for _, peer := range peers {
						sb.WriteString(fmt.Sprintf("  - %s [%s]\n", peer.Name, peer.ID))
						for _, addr := range peer.Addrs {
							sb.WriteString(fmt.Sprintf("    %s\n", addr))
						}
					}
					logger.Infof("%s", sb.String())
				}
			}
		}
	}()

	logger.Infof("P2P client started. Press Ctrl+C to exit")

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Infof("\nShutting down...")
	cancel()
}
