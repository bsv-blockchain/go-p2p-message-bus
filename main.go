package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/multiformats/go-multiaddr"
)

const topicName = "broadcast_p2p_poc"

type Message struct {
	Name    string `json:"name"`
	Counter int    `json:"counter"`
}

type PeerTracker struct {
	mu    sync.RWMutex
	names map[peer.ID]string
}

func NewPeerTracker() *PeerTracker {
	return &PeerTracker{
		names: make(map[peer.ID]string),
	}
}

func (pt *PeerTracker) UpdateName(peerID peer.ID, name string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.names[peerID] = name
}

func (pt *PeerTracker) GetName(peerID peer.ID) string {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	if name, ok := pt.names[peerID]; ok {
		return name
	}
	return "unknown"
}

var bootstrapNodes = []string{
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
}

type discoveryNotifee struct {
	h   host.Host
	ctx context.Context
}

func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if n.h.ID() == pi.ID {
		return
	}

	if err := n.h.Connect(n.ctx, pi); err == nil {
		fmt.Printf("Connected to peer: %s\n", pi.ID.String())
	}
}

func main() {
	name := flag.String("name", "", "Your node name")
	flag.Parse()

	if *name == "" {
		log.Fatal("--name flag is required")
	}

	ctx, cancel := context.WithCancel(context.Background())

	h, err := createHost(ctx)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}

	fmt.Printf("Host created. ID: %s\n", h.ID())
	fmt.Printf("Listening on: %v\n", h.Addrs())

	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		log.Fatalf("Failed to create DHT: %v", err)
	}

	if err := kadDHT.Bootstrap(ctx); err != nil {
		log.Fatalf("Failed to bootstrap DHT: %v", err)
	}

	connectToBootstrapNodes(ctx, h, bootstrapNodes)

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		log.Fatalf("Failed to create pubsub: %v", err)
	}

	topic, err := ps.Join(topicName)
	if err != nil {
		log.Fatalf("Failed to join topic: %v", err)
	}

	sub, err := topic.Subscribe()
	if err != nil {
		log.Fatalf("Failed to subscribe: %v", err)
	}

	mdnsService := mdns.NewMdnsService(h, topicName, &discoveryNotifee{h: h, ctx: ctx})
	if err := mdnsService.Start(); err != nil {
		log.Printf("Warning: mDNS failed to start: %v", err)
	} else {
		fmt.Println("mDNS discovery started")
	}

	routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)
	go func() {
		_, err := routingDiscovery.Advertise(ctx, topicName)
		if err != nil && ctx.Err() == nil {
			log.Printf("Failed to advertise: %v", err)
		} else if err == nil {
			fmt.Println("Announcing presence on DHT")
		}
	}()

	peerTracker := NewPeerTracker()

	go discoverPeers(ctx, h, routingDiscovery)

	go receiveMessages(ctx, sub, h.ID(), peerTracker)

	go broadcastMessages(ctx, topic, *name)

	go printPeersPeriodically(ctx, h, topic, peerTracker)

	fmt.Println("Press Ctrl+C to exit")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down...")
	cancel()

	shutdownDone := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		sub.Cancel()
		topic.Close()
		mdnsService.Close()
		kadDHT.Close()
		h.Close()
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
		fmt.Println("Clean shutdown complete")
	case <-time.After(1 * time.Second):
		fmt.Println("Shutdown timeout, forcing exit")
		os.Exit(0)
	}
}

func createHost(ctx context.Context) (host.Host, error) {
	return libp2p.New(
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/0",
			"/ip6/::/tcp/0",
		),
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
		libp2p.EnableRelay(),
	)
}

func connectToBootstrapNodes(ctx context.Context, h host.Host, bootstrapPeers []string) {
	var wg sync.WaitGroup

	for _, addr := range bootstrapPeers {
		wg.Add(1)

		go func(addr string) {
			defer wg.Done()

			maddr, err := multiaddr.NewMultiaddr(addr)
			if err != nil {
				log.Printf("Failed to parse bootstrap address %s: %v", addr, err)
				return
			}

			peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
			if err != nil {
				log.Printf("Failed to get peer info from %s: %v", addr, err)
				return
			}

			if err := h.Connect(ctx, *peerInfo); err != nil {
				log.Printf("Failed to connect to bootstrap node %s: %v", peerInfo.ID, err)
			} else {
				fmt.Printf("Connected to bootstrap node: %s\n", peerInfo.ID)
			}
		}(addr)
	}

	wg.Wait()
}

func discoverPeers(ctx context.Context, h host.Host, routingDiscovery *drouting.RoutingDiscovery) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			peerChan, err := routingDiscovery.FindPeers(ctx, topicName)
			if err != nil {
				log.Printf("Failed to find peers: %v", err)
				continue
			}

			for peer := range peerChan {
				if peer.ID == h.ID() {
					continue
				}

				if h.Network().Connectedness(peer.ID) != 1 {
					if err := h.Connect(ctx, peer); err == nil {
						fmt.Printf("Connected to discovered peer: %s\n", peer.ID)
					}
				}
			}
		}
	}
}

func receiveMessages(ctx context.Context, sub *pubsub.Subscription, selfID peer.ID, tracker *PeerTracker) {
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

		tracker.UpdateName(msg.ReceivedFrom, m.Name)
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
			} else {
				fmt.Printf("%s: %d\n", msg.Name, msg.Counter)
			}
		}
	}
}

func printPeersPeriodically(ctx context.Context, h host.Host, topic *pubsub.Topic, tracker *PeerTracker) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			allPeers := h.Network().Peers()
			topicPeers := topic.ListPeers()

			fmt.Printf("\n[Total connections: %d | Topic peers: %d]\n", len(allPeers), len(topicPeers))
			if len(topicPeers) > 0 {
				fmt.Println("Topic peers:")
				for _, p := range topicPeers {
					name := tracker.GetName(p)
					conns := h.Network().ConnsToPeer(p)

					for _, conn := range conns {
						addr := conn.RemoteMultiaddr().String()
						connType := "DIRECT"
						if isRelayedConnection(addr) {
							connType = "RELAYED"
						}

						fmt.Printf("  - %s (%s) [%s] %s\n", p.String(), name, connType, addr)
					}

					if len(conns) == 0 {
						fmt.Printf("  - %s (%s) [NO CONNECTION]\n", p.String(), name)
					}
				}
			} else {
				fmt.Println("  (No peers on topic yet)")
			}
			fmt.Println()
		}
	}
}

func isRelayedConnection(addr string) bool {
	return strings.Contains(addr, "/p2p-circuit/")
}
