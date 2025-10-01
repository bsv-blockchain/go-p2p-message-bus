package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/ordishs/gocore"
	p2p "github.com/ordishs/p2p_poc"
)

func main() {
	name := flag.String("name", "", "Your node name")
	privateKey := flag.String("key", "", "Private key hex (will generate if not provided)")
	topics := flag.String("topics", "broadcast_p2p_poc", "Comma-separated list of topics to subscribe to")
	noBroadcast := flag.Bool("no-broadcast", false, "Disable message broadcasting")

	flag.Parse()

	logger := gocore.Log("p2p_poc")

	if *name == "" {
		logger.Fatal("--name flag is required")
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
			logger.Fatalf("Failed to generate private key: %v", err)
		}

		keyHex, _ = p2p.PrivateKeyToHex(privKey)
		fmt.Printf("Generated new private key: %s\n", keyHex)
		fmt.Println("Save this key and use it next time with --key flag or P2P_PRIVATE_KEY env var")
	} else {
		// Load key from hex
		privKey, err = p2p.PrivateKeyFromHex(keyHex)
		if err != nil {
			logger.Fatalf("Failed to load private key: %v", err)
		}
	}

	// Create P2P client
	client, err := p2p.NewClient(p2p.Config{
		Name:          *name,
		Logger:        logger,
		PrivateKey:    privKey,
		PeerCacheFile: "peer_cache.json", // Enable peer persistence
	})
	if err != nil {
		logger.Fatalf("Failed to create P2P client: %v", err)
	}
	defer client.Close()

	// Parse topics list
	topicList := strings.Split(*topics, ",")
	for i, t := range topicList {
		topicList[i] = strings.TrimSpace(t)
	}

	logger.Infof("Subscribing to topics: %v", topicList)

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

		logger.Infof("Subscribed to topic: %s", topic)
	}

	// Start message receiver
	go func() {
		for msg := range allMsgChan {
			fmt.Printf("[%-52s] %s: %s (topic: %s)\n", msg.FromID, msg.From, string(msg.Data), msg.Topic)
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
					fmt.Printf("[%-52s] %s: %s\n", "local", *name, data)
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
					fmt.Printf("\n=== Connected Peers: %d ===\n", len(peers))
					for _, peer := range peers {
						fmt.Printf("  - %s [%s]\n", peer.Name, peer.ID)
						for _, addr := range peer.Addrs {
							fmt.Printf("    %s\n", addr)
						}
					}
					fmt.Println()
				}
			}
		}
	}()

	fmt.Println("P2P client started. Press Ctrl+C to exit")

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down...")
	cancel()
}
