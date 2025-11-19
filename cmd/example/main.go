// Package main provides an example P2P messaging application with topic subscription and broadcasting.
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

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/bsv-blockchain/go-p2p-message-bus"
)

func main() {
	name := flag.String("name", "", "Your node name")
	privateKey := flag.String("key", "", "Private key hex (will generate if not provided)")
	topics := flag.String("topics", "broadcast_p2p_poc", "Comma-separated list of topics to subscribe to")
	port := flag.Int("port", 0, "port to listen on (0 for random)")
	noBroadcast := flag.Bool("no-broadcast", false, "Disable message broadcasting")
	prettyJSON := flag.Bool("pretty-json", false, "Pretty print JSON messages")
	bootstrap := flag.String("bootstrap", "", "Comma-separated list of bootstrap peer multiaddrs (e.g., /ip4/1.2.3.4/tcp/4001/p2p/PeerID)")
	dhtMode := flag.String("dht-mode", "client", "DHT mode: server, client, or off (off = topic-only, no DHT crawling)")
	peerCache := flag.String("peer-cache", "", "Path to peer cache file for persistence (empty = disabled)")

	flag.Parse()

	logger := &p2p.DefaultLogger{}

	if *name == "" {
		logger.Errorf("--name flag is required")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get or generate private key
	privKey, err := getOrGeneratePrivateKey(*privateKey, logger)
	if err != nil {
		logger.Errorf("Failed to get private key: %v", err)
		return
	}

	// Parse bootstrap peers list
	bootstrapPeers := parseBootstrapPeers(*bootstrap)

	// Create P2P client
	client, err := p2p.NewClient(p2p.Config{
		Name:           *name,
		Logger:         logger,
		PrivateKey:     privKey,
		Port:           *port,
		PeerCacheFile:  *peerCache,
		BootstrapPeers: bootstrapPeers,
		DHTMode:        *dhtMode,
	})
	if err != nil {
		logger.Errorf("Failed to create P2P client: %v", err)
		return
	}

	defer func() {
		if err := client.Close(); err != nil {
			logger.Errorf("Failed to close client: %v", err)
		}
	}()

	// Parse and subscribe to topics
	topicList := parseTopics(*topics)
	allMsgChan := subscribeToTopics(client, topicList, logger)

	// Start message receiver
	go receiveMessages(allMsgChan, *prettyJSON, logger)

	// Start message broadcaster (publishes to all topics)
	if !*noBroadcast {
		go broadcastMessages(ctx, client, topicList, *name, logger)
	}

	// Periodically display peer information
	go displayPeers(ctx, client, logger)

	logger.Infof("P2P client started. Press Ctrl+C to exit")

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Infof("\nShutting down...")
	cancel()
}

// Helper functions

func getOrGeneratePrivateKey(keyHex string, logger *p2p.DefaultLogger) (crypto.PrivKey, error) {
	if keyHex == "" {
		keyHex = os.Getenv("P2P_PRIVATE_KEY")
	}

	if keyHex == "" {
		privKey, err := p2p.GeneratePrivateKey()
		if err != nil {
			return nil, err
		}
		newKeyHex, _ := p2p.PrivateKeyToHex(privKey)
		logger.Infof("Generated new private key: %s\n", newKeyHex)
		logger.Infof("Save this key and use it next time with --key flag or P2P_PRIVATE_KEY env var")
		return privKey, nil
	}

	return p2p.PrivateKeyFromHex(keyHex)
}

func parseBootstrapPeers(bootstrap string) []string {
	if bootstrap == "" {
		return nil
	}

	parts := strings.Split(bootstrap, ",")
	bootstrapPeers := make([]string, 0, len(parts))
	for _, b := range parts {
		bootstrapPeers = append(bootstrapPeers, strings.TrimSpace(b))
	}
	return bootstrapPeers
}

func parseTopics(topics string) []string {
	topicList := strings.Split(topics, ",")
	for i, t := range topicList {
		topicList[i] = strings.TrimSpace(t)
	}
	return topicList
}

func subscribeToTopics(client p2p.Client, topics []string, logger *p2p.DefaultLogger) chan p2p.Message {
	allMsgChan := make(chan p2p.Message, 100)

	for _, topic := range topics {
		msgChan := client.Subscribe(topic)
		logger.Infof("Subscribed to topic: %s", topic)

		go func(ch <-chan p2p.Message) {
			for msg := range ch {
				allMsgChan <- msg
			}
		}(msgChan)
	}

	return allMsgChan
}

func receiveMessages(msgChan <-chan p2p.Message, prettyJSON bool, logger *p2p.DefaultLogger) {
	for msg := range msgChan {
		data := formatMessageData(msg.Data, prettyJSON)
		logger.Infof("[%-52s] %s: (%s)\n%s", msg.FromID, msg.From, msg.Topic, data)
	}
}

func formatMessageData(data []byte, prettyJSON bool) string {
	if !prettyJSON {
		return string(data)
	}

	var jsonObj interface{}
	if err := json.Unmarshal(data, &jsonObj); err == nil {
		if jsonBytes, err := json.MarshalIndent(jsonObj, "", "  "); err == nil {
			return string(jsonBytes)
		}
	}
	return string(data)
}

func broadcastMessages(ctx context.Context, client p2p.Client, topics []string, name string, logger *p2p.DefaultLogger) {
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

			for _, topic := range topics {
				if err := client.Publish(ctx, topic, []byte(data)); err != nil {
					return
				}
			}
			logger.Infof("[%-52s] %s: %s\n", "local", name, data)
		}
	}
}

func displayPeers(ctx context.Context, client p2p.Client, logger *p2p.DefaultLogger) {
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
}
