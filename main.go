package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const topicName = "broadcast"

type Message struct {
	Name    string `json:"name"`
	Counter int    `json:"counter"`
}

var relayNodes = []string{
	"/dnsaddr/relay.libp2p.io/p2p/12D3KooWPjceQrSwdWXPyLLeABRXmuqt69Rg3sBYbU1Nft9HyQ6X",
	"/dnsaddr/relay.libp2p.io/p2p/12D3KooWAJjbRkVdHihWhxbKsuGN4mhV5jB5B9K5xzL5YEqjQcfC",
}

func main() {
	name := flag.String("name", "Node", "Your node name")
	flag.Parse()

	if *name == "" {
		log.Fatal("Name cannot be empty")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := createHost(ctx)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}

	defer h.Close()

	fmt.Printf("Host created. ID: %s\n", h.ID())
	fmt.Printf("Listening on: %v\n", h.Addrs())

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		log.Fatalf("Failed to create pubsub: %v", err)
	}

	topic, err := ps.Join(topicName)
	if err != nil {
		log.Fatalf("Failed to join topic: %v", err)
	}

	defer topic.Close()

	sub, err := topic.Subscribe()
	if err != nil {
		log.Fatalf("Failed to subscribe: %v", err)
	}

	defer sub.Cancel()

	if err := connectToRelays(ctx, h); err != nil {
		log.Printf("Warning: Failed to connect to some relays: %v", err)
	}

	go receiveMessages(ctx, sub, h.ID())

	go broadcastMessages(ctx, topic, *name)

	fmt.Println("Press Ctrl+C to exit")
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	fmt.Println("\nShutting down...")
}

func createHost(ctx context.Context) (host.Host, error) {
	return libp2p.New(
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/0",
			"/ip6/::/tcp/0",
		),
		libp2p.EnableNATService(),
		libp2p.EnableAutoRelayWithStaticRelays(parseRelayAddrs()),
		libp2p.EnableHolePunching(),
	)
}

func parseRelayAddrs() []peer.AddrInfo {
	var addrs []peer.AddrInfo

	for _, relay := range relayNodes {
		maddr, err := multiaddr.NewMultiaddr(relay)
		if err != nil {
			log.Printf("Failed to parse relay address %s: %v", relay, err)
			continue
		}

		addrInfo, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			log.Printf("Failed to get peer info from %s: %v", relay, err)
			continue
		}

		addrs = append(addrs, *addrInfo)
	}

	return addrs
}

func connectToRelays(ctx context.Context, h host.Host) error {
	for _, relay := range relayNodes {
		maddr, err := multiaddr.NewMultiaddr(relay)
		if err != nil {
			log.Printf("Failed to parse relay address %s: %v", relay, err)
			continue
		}

		addrInfo, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			log.Printf("Failed to get peer info from %s: %v", relay, err)
			continue
		}

		if err := h.Connect(ctx, *addrInfo); err != nil {
			log.Printf("Failed to connect to relay %s: %v", addrInfo.ID, err)
		} else {
			fmt.Printf("Connected to relay: %s\n", addrInfo.ID)
		}
	}

	return nil
}

func receiveMessages(ctx context.Context, sub *pubsub.Subscription, selfID peer.ID) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			log.Printf("Error reading message: %v", err)
			continue
		}

		if msg.ReceivedFrom == selfID {
			continue
		}

		var m Message
		if err := json.Unmarshal(msg.Data, &m); err != nil {
			log.Printf("Error unmarshaling message: %v", err)
			continue
		}

		fmt.Printf("%s: %d\n", m.Name, m.Counter)
	}
}

func broadcastMessages(ctx context.Context, topic *pubsub.Topic, name string) {
	counter := 0
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			counter++
			msg := Message{
				Name:    name,
				Counter: counter,
			}

			data, err := json.Marshal(msg)
			if err != nil {
				log.Printf("Error marshaling message: %v", err)
				continue
			}

			if err := topic.Publish(ctx, data); err != nil {
				log.Printf("Error publishing message: %v", err)
			}
		}
	}
}
