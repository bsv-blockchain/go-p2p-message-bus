package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/multiformats/go-multiaddr"
)

const (
	topicName     = "broadcast_p2p_poc"
	peerCacheFile = "peer_cache.json"
)

type Message struct {
	Name    string `json:"name"`
	Counter int    `json:"counter"`
}

type CachedPeer struct {
	ID    string   `json:"id"`
	Name  string   `json:"name,omitempty"`
	Addrs []string `json:"addrs"`
}

type PeerTracker struct {
	mu         sync.RWMutex
	names      map[peer.ID]string
	relayCount int
	isRelaying map[string]bool
	topicPeers map[peer.ID]bool
	lastSeen   map[peer.ID]time.Time
}

func NewPeerTracker() *PeerTracker {
	return &PeerTracker{
		names:      make(map[peer.ID]string),
		isRelaying: make(map[string]bool),
		topicPeers: make(map[peer.ID]bool),
		lastSeen:   make(map[peer.ID]time.Time),
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

func (pt *PeerTracker) RecordRelay(srcPeer, dstPeer peer.ID) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	key := srcPeer.String() + "->" + dstPeer.String()
	if !pt.isRelaying[key] {
		pt.isRelaying[key] = true
		pt.relayCount++
		fmt.Printf("\n[RELAY] Acting as relay: %s -> %s (total relays: %d)\n\n", srcPeer.String()[:16], dstPeer.String()[:16], pt.relayCount)
	}
}

func (pt *PeerTracker) GetRelayCount() int {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.relayCount
}

func (pt *PeerTracker) RecordMessageFrom(peerID peer.ID) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.topicPeers[peerID] = true
	pt.lastSeen[peerID] = time.Now()
}

func (pt *PeerTracker) GetAllTopicPeers() []peer.ID {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	peers := make([]peer.ID, 0, len(pt.topicPeers))
	for peerID := range pt.topicPeers {
		peers = append(peers, peerID)
	}
	return peers
}

func (pt *PeerTracker) GetLastSeen(peerID peer.ID) time.Time {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.lastSeen[peerID]
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

	bootstrapPeers := dht.GetDefaultBootstrapPeerAddrInfos()

	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer), dht.BootstrapPeers(bootstrapPeers...))
	if err != nil {
		log.Fatalf("Failed to create DHT: %v", err)
	}

	if err := kadDHT.Bootstrap(ctx); err != nil {
		log.Fatalf("Failed to bootstrap DHT: %v", err)
	}

	for _, peerInfo := range bootstrapPeers {
		go func(pi peer.AddrInfo) {
			if err := h.Connect(ctx, pi); err == nil {
				fmt.Printf("Connected to bootstrap peer: %s\n", pi.ID.String())
			}
		}(peerInfo)
	}

	cachedPeers := loadPeerCache()
	if len(cachedPeers) > 0 {
		fmt.Printf("Connecting to %d cached peers...\n", len(cachedPeers))
		connectToCachedPeers(ctx, h, cachedPeers)
	}

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
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		timeout := time.After(10 * time.Second)

		for {
			select {
			case <-ctx.Done():
				return
			case <-timeout:
				log.Printf("Timeout waiting for DHT peers - will rely on mDNS and peer cache")
				return
			case <-ticker.C:
				if len(kadDHT.RoutingTable().ListPeers()) > 0 {
					_, err := routingDiscovery.Advertise(ctx, topicName)
					if err != nil {
						log.Printf("Failed to advertise: %v", err)
					} else {
						fmt.Println("Announcing presence on DHT")
					}
					return
				}
			}
		}
	}()

	peerTracker := NewPeerTracker()

	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(n network.Network, conn network.Conn) {
			monitorRelayActivity(conn, peerTracker)
			monitorConnectionUpgrade(conn)

			go func() {
				time.Sleep(500 * time.Millisecond)
				peerID := conn.RemotePeer()
				topicPeers := topic.ListPeers()
				for _, tp := range topicPeers {
					if tp == peerID {
						name := peerTracker.GetName(peerID)
						addr := conn.RemoteMultiaddr().String()

						fmt.Printf("\n[CONNECTED] Topic peer %s [%s] %s\n\n", peerID.String(), name, addr)

						var cachedPeers []CachedPeer
						for _, p := range topicPeers {
							if conns := h.Network().ConnsToPeer(p); len(conns) > 0 {
								var addrs []string
								for _, c := range conns {
									addrs = append(addrs, c.RemoteMultiaddr().String())
								}
								cachedPeers = append(cachedPeers, CachedPeer{
									ID:    p.String(),
									Name:  peerTracker.GetName(p),
									Addrs: addrs,
								})
							}
						}
						if len(cachedPeers) > 0 {
							savePeerCache(cachedPeers)
						}
						return
					}
				}
			}()
		},
		DisconnectedF: func(n network.Network, conn network.Conn) {
			peerID := conn.RemotePeer()
			topicPeers := topic.ListPeers()
			for _, tp := range topicPeers {
				if tp == peerID {
					fmt.Printf("\n[DISCONNECTED] Lost connection to topic peer %s\n", peerID.String()[:16])
					return
				}
			}
		},
	})

	subscribeToHolePunchEvents(ctx, h)

	go discoverPeers(ctx, h, routingDiscovery)

	go receiveMessages(ctx, sub, h, peerTracker)

	go broadcastMessages(ctx, topic, h, *name)

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

func receiveMessages(ctx context.Context, sub *pubsub.Subscription, h host.Host, tracker *PeerTracker) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			log.Printf("Error reading message: %v", err)
			continue
		}

		author := msg.GetFrom()
		if author == h.ID() {
			continue
		}

		var m Message
		if err := json.Unmarshal(msg.Data, &m); err != nil {
			log.Printf("Error unmarshaling message: %v", err)
			continue
		}

		tracker.UpdateName(author, m.Name)
		tracker.RecordMessageFrom(author)

		ipAddr := extractIPAddress(h, author)

		relayStr := ""

		if msg.ReceivedFrom != author {
			relayIP := extractIPAddress(h, msg.ReceivedFrom)
			relayStr = fmt.Sprintf("[via relay %s]", relayIP)
		}
		fmt.Printf("[%-20s] %-30s: %s %d\n", ipAddr, relayStr, m.Name, m.Counter)
	}
}

func extractIPAddress(h host.Host, peerID peer.ID) string {
	conns := h.Network().ConnsToPeer(peerID)
	if len(conns) == 0 {
		return "unknown"
	}

	addr := conns[0].RemoteMultiaddr().String()

	parts := strings.Split(addr, "/")
	for i, part := range parts {
		if (part == "ip4" || part == "ip6") && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	return "unknown"
}

func broadcastMessages(ctx context.Context, topic *pubsub.Topic, h host.Host, name string) {
	counter := 0
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	localIP := getLocalIP(h)

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
				fmt.Printf("[%-20s] %-30s: %s %d\n", localIP, "", msg.Name, msg.Counter)
			}
		}
	}
}

func getLocalIP(h host.Host) string {
	addrs := h.Addrs()
	for _, addr := range addrs {
		addrStr := addr.String()
		parts := strings.Split(addrStr, "/")
		for i, part := range parts {
			if (part == "ip4" || part == "ip6") && i+1 < len(parts) {
				ip := parts[i+1]
				if ip != "127.0.0.1" && ip != "::1" {
					return ip
				}
			}
		}
	}
	return "localhost"
}

func isRelayedConnection(addr string) bool {
	return strings.Contains(addr, "/p2p-circuit/")
}

func loadPeerCache() []CachedPeer {
	file, err := os.Open(peerCacheFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: failed to open peer cache: %v", err)
		}
		return nil
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		log.Printf("Warning: failed to read peer cache: %v", err)
		return nil
	}

	var peers []CachedPeer
	if err := json.Unmarshal(data, &peers); err != nil {
		log.Printf("Warning: failed to parse peer cache: %v", err)
		return nil
	}

	return peers
}

func savePeerCache(peers []CachedPeer) {
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		log.Printf("Warning: failed to marshal peer cache: %v", err)
		return
	}

	if err := os.WriteFile(peerCacheFile, data, 0644); err != nil {
		log.Printf("Warning: failed to write peer cache: %v", err)
	}
}

func connectToCachedPeers(ctx context.Context, h host.Host, cachedPeers []CachedPeer) {
	for _, cp := range cachedPeers {
		peerID, err := peer.Decode(cp.ID)
		if err != nil {
			log.Printf("Invalid cached peer ID %s: %v", cp.ID, err)
			continue
		}

		if h.Network().Connectedness(peerID) == network.Connected {
			continue
		}

		var maddrs []multiaddr.Multiaddr
		for _, addrStr := range cp.Addrs {
			maddr, err := multiaddr.NewMultiaddr(addrStr)
			if err != nil {
				continue
			}
			maddrs = append(maddrs, maddr)
		}

		if len(maddrs) == 0 {
			continue
		}

		addrInfo := peer.AddrInfo{
			ID:    peerID,
			Addrs: maddrs,
		}

		go func(ai peer.AddrInfo) {
			if err := h.Connect(ctx, ai); err == nil {
				fmt.Printf("Reconnected to cached peer: %s\n", ai.ID.String())
			}
		}(addrInfo)
	}
}

func monitorRelayActivity(conn network.Conn, tracker *PeerTracker) {
	go func() {
		streams := conn.GetStreams()
		for _, stream := range streams {
			protocol := stream.Protocol()
			if protocol == proto.ProtoIDv2Hop || protocol == proto.ProtoIDv2Stop {
				localAddr := conn.LocalMultiaddr().String()
				remoteAddr := conn.RemoteMultiaddr().String()

				if strings.Contains(localAddr, "/p2p-circuit/") || strings.Contains(remoteAddr, "/p2p-circuit/") {
					remotePeer := conn.RemotePeer()
					tracker.RecordRelay(remotePeer, remotePeer)
				}
			}
		}
	}()
}

func monitorConnectionUpgrade(conn network.Conn) {
	addr := conn.RemoteMultiaddr().String()
	if strings.Contains(addr, "/p2p-circuit/") {
		fmt.Printf("\n[RELAY CONNECTION] Connected via relay to %s\n", conn.RemotePeer().String()[:16])
		fmt.Printf("  Waiting for hole punch to establish direct connection...\n\n")
	}
}

func subscribeToHolePunchEvents(ctx context.Context, h host.Host) {
	bus := h.EventBus()

	sub, err := bus.Subscribe(new(holepunch.Event))
	if err != nil {
		log.Printf("Warning: failed to subscribe to hole punch events: %v", err)
		return
	}

	go func() {
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-sub.Out():
				hpEvt, ok := evt.(holepunch.Event)
				if !ok {
					continue
				}

				switch hpEvt.Type {
				case "StartHolePunch":
					fmt.Printf("\n[HOLE PUNCH] Starting hole punch with %s\n\n", hpEvt.Remote.String()[:16])
				case "EndHolePunch":
					fmt.Printf("\n[HOLE PUNCH] Completed attempt with %s (check connection type in peer list)\n\n", hpEvt.Remote.String()[:16])
				case "HolePunchAttempt":
					fmt.Printf("\n[HOLE PUNCH] Attempting direct connection to %s...\n\n", hpEvt.Remote.String()[:16])
				}
			}
		}
	}()
}
