package main

import (
	"context"
	"encoding/hex"
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
	"github.com/libp2p/go-libp2p/core/pnet"
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
	bootstrap := flag.String("bootstrap", "", "Comma-separated list of bootstrap node multiaddrs (overrides defaults)")
	pskString := flag.String("psk", "", "Preshared key for private network (hex encoded, 64 characters)")
	publicPeer := flag.Bool("public", false, "Run as public discovery peer (does not process topic messages)")
	flag.Parse()

	if *name == "" {
		log.Fatal("--name flag is required")
	}

	var bootstrapPeers []string
	if *bootstrap != "" {
		bootstrapPeers = strings.Split(*bootstrap, ",")
		for i, addr := range bootstrapPeers {
			bootstrapPeers[i] = strings.TrimSpace(addr)
		}
	} else {
		bootstrapPeers = bootstrapNodes
	}

	var psk pnet.PSK
	if *pskString != "" {
		var err error
		psk, err = parsePSK(*pskString)
		if err != nil {
			log.Fatalf("Failed to parse PSK: %v", err)
		}
		fmt.Println("Using private network with preshared key")
	}

	ctx, cancel := context.WithCancel(context.Background())

	h, err := createHost(ctx, psk)
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

	connectToBootstrapNodes(ctx, h, bootstrapPeers)

	cachedPeers := loadPeerCache()
	if len(cachedPeers) > 0 {
		fmt.Printf("Connecting to %d cached peers...\n", len(cachedPeers))
		connectToCachedPeers(ctx, h, cachedPeers)
	}

	var mdnsService mdns.Service
	var topic *pubsub.Topic
	var sub *pubsub.Subscription

	if *publicPeer {
		fmt.Println("Running as PUBLIC DISCOVERY PEER (not processing topic messages)")

		mdnsService = mdns.NewMdnsService(h, topicName, &discoveryNotifee{h: h, ctx: ctx})
		if err := mdnsService.Start(); err != nil {
			log.Printf("Warning: mDNS failed to start: %v", err)
		} else {
			fmt.Println("mDNS discovery started")
		}

		routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)
		go func() {
			time.Sleep(5 * time.Second)

			_, err := routingDiscovery.Advertise(ctx, topicName)
			if err != nil && ctx.Err() == nil {
				if strings.Contains(err.Error(), "failed to find any peer in table") {
					log.Printf("DHT routing table empty - peer discovery via mDNS or bootstrap nodes")
				} else {
					log.Printf("Failed to advertise: %v", err)
				}
			} else if err == nil {
				fmt.Println("Announcing presence on DHT")
			}
		}()

		go discoverPeers(ctx, h, routingDiscovery)

		h.Network().Notify(&network.NotifyBundle{
			ConnectedF: func(n network.Network, conn network.Conn) {
				fmt.Printf("Peer connected: %s\n", conn.RemotePeer().String()[:16])
			},
			DisconnectedF: func(n network.Network, conn network.Conn) {
				fmt.Printf("Peer disconnected: %s\n", conn.RemotePeer().String()[:16])
			},
		})

		subscribeToHolePunchEvents(ctx, h)

		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					allPeers := h.Network().Peers()
					fmt.Printf("\n[PUBLIC PEER] Total connections: %d\n", len(allPeers))
				}
			}
		}()
	} else {
		ps, err := pubsub.NewGossipSub(ctx, h)
		if err != nil {
			log.Fatalf("Failed to create pubsub: %v", err)
		}

		topic, err = ps.Join(topicName)
		if err != nil {
			log.Fatalf("Failed to join topic: %v", err)
		}

		sub, err = topic.Subscribe()
		if err != nil {
			log.Fatalf("Failed to subscribe: %v", err)
		}

		mdnsService = mdns.NewMdnsService(h, topicName, &discoveryNotifee{h: h, ctx: ctx})
		if err := mdnsService.Start(); err != nil {
			log.Printf("Warning: mDNS failed to start: %v", err)
		} else {
			fmt.Println("mDNS discovery started")
		}

		routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)
		go func() {
			time.Sleep(5 * time.Second)

			_, err := routingDiscovery.Advertise(ctx, topicName)
			if err != nil && ctx.Err() == nil {
				if strings.Contains(err.Error(), "failed to find any peer in table") {
					log.Printf("DHT routing table empty - peer discovery via mDNS or bootstrap nodes")
				} else {
					log.Printf("Failed to advertise: %v", err)
				}
			} else if err == nil {
				fmt.Println("Announcing presence on DHT")
			}
		}()

		peerTracker := NewPeerTracker()

		h.Network().Notify(&network.NotifyBundle{
			ConnectedF: func(n network.Network, conn network.Conn) {
				monitorRelayActivity(conn, peerTracker)
				monitorConnectionUpgrade(conn)
			},
			DisconnectedF: func(n network.Network, conn network.Conn) {
				peerID := conn.RemotePeer()
				topicPeers := topic.ListPeers()
				for _, tp := range topicPeers {
					if tp == peerID {
						fmt.Printf("\n[DISCONNECTED] Lost connection to topic peer %s\n", peerID.String()[:16])
						fmt.Printf("  Will attempt reconnection via cached peers and discovery...\n\n")
						return
					}
				}
			},
		})

		subscribeToHolePunchEvents(ctx, h)

		go discoverPeers(ctx, h, routingDiscovery)

		go receiveMessages(ctx, sub, h, peerTracker)

		go broadcastMessages(ctx, topic, h, *name)

		go printPeersPeriodically(ctx, h, topic, peerTracker)

		go maintainPeerConnections(ctx, h, topic, kadDHT, true)

		go maintainBootstrapConnections(ctx, h, bootstrapPeers)
	}

	fmt.Println("Press Ctrl+C to exit")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down...")
	cancel()

	shutdownDone := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		if sub != nil {
			sub.Cancel()
		}
		if topic != nil {
			topic.Close()
		}
		if mdnsService != nil {
			mdnsService.Close()
		}
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

func createHost(ctx context.Context, psk pnet.PSK) (host.Host, error) {
	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/0",
			"/ip6/::/tcp/0",
		),
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
		libp2p.EnableRelay(),
	}

	if psk != nil {
		opts = append(opts, libp2p.PrivateNetwork(psk))
	}

	return libp2p.New(opts...)
}

func parsePSK(s string) (pnet.PSK, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")

	data, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decoding hex PSK: %w", err)
	}

	if len(data) != 32 {
		return nil, fmt.Errorf("PSK must be 32 bytes (64 hex characters), got %d bytes", len(data))
	}

	var psk [32]byte
	copy(psk[:], data)

	return psk[:], nil
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

		if msg.ReceivedFrom != author {
			relayIP := extractIPAddress(h, msg.ReceivedFrom)
			fmt.Printf("[%s via relay %s] %s: %d\n", ipAddr, relayIP, m.Name, m.Counter)
		} else {
			fmt.Printf("[%s] %s: %d\n", ipAddr, m.Name, m.Counter)
		}
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
				fmt.Printf("[%s] %s: %d\n", localIP, msg.Name, msg.Counter)
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

func printPeersPeriodically(ctx context.Context, h host.Host, topic *pubsub.Topic, tracker *PeerTracker) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			allPeers := h.Network().Peers()
			meshPeers := topic.ListPeers()
			allTopicPeers := tracker.GetAllTopicPeers()

			meshPeerSet := make(map[peer.ID]bool)
			for _, p := range meshPeers {
				meshPeerSet[p] = true
			}

			relayCount := tracker.GetRelayCount()
			fmt.Printf("\n[Total connections: %d | Topic peers: %d (in mesh: %d) | Acting as relay: %d]\n",
				len(allPeers), len(allTopicPeers), len(meshPeers), relayCount)

			var cachedPeers []CachedPeer
			if len(allTopicPeers) > 0 {
				fmt.Println("Topic peers:")
				for _, p := range allTopicPeers {
					name := tracker.GetName(p)
					conns := h.Network().ConnsToPeer(p)
					lastSeen := tracker.GetLastSeen(p)
					timeSince := time.Since(lastSeen)

					meshStatus := "NOT IN MESH"
					if meshPeerSet[p] {
						meshStatus = "IN MESH"
					}

					var peerAddrs []string
					for _, conn := range conns {
						addr := conn.RemoteMultiaddr().String()
						peerAddrs = append(peerAddrs, addr)

						connType := "DIRECT"
						if isRelayedConnection(addr) {
							connType = "RELAYED"
						}

						fmt.Printf("  - %s (%s) [%s] [%s] (last seen: %s ago) %s\n",
							p.String()[:16], name, meshStatus, connType, timeSince.Round(time.Second), addr)
					}

					if len(conns) == 0 {
						fmt.Printf("  - %s (%s) [%s] [NO CONNECTION] (last seen: %s ago)\n",
							p.String()[:16], name, meshStatus, timeSince.Round(time.Second))
					} else {
						cachedPeers = append(cachedPeers, CachedPeer{
							ID:    p.String(),
							Name:  name,
							Addrs: peerAddrs,
						})
					}
				}

				savePeerCache(cachedPeers)
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

func maintainPeerConnections(ctx context.Context, h host.Host, topic *pubsub.Topic, dhtInst *dht.IpfsDHT, checkImmediately bool) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	reconnectPeers := func() {
		cachedPeers := loadPeerCache()

		for _, cp := range cachedPeers {
			peerID, err := peer.Decode(cp.ID)
			if err != nil {
				continue
			}

			connectedness := h.Network().Connectedness(peerID)
			if connectedness == network.Connected {
				continue
			}

			fmt.Printf("\n[RECONNECT] Attempting to reconnect to topic peer %s (%s)...\n", cp.Name, peerID.String()[:16])

			connCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			peerInfo, err := dhtInst.FindPeer(connCtx, peerID)
			if err != nil {
				fmt.Printf("  DHT lookup failed: %v, trying cached addresses...\n", err)

				var maddrs []multiaddr.Multiaddr
				for _, addrStr := range cp.Addrs {
					maddr, err := multiaddr.NewMultiaddr(addrStr)
					if err != nil {
						continue
					}
					maddrs = append(maddrs, maddr)
				}

				if len(maddrs) > 0 {
					peerInfo = peer.AddrInfo{
						ID:    peerID,
						Addrs: maddrs,
					}
				} else {
					fmt.Printf("  No valid addresses available\n\n")
					continue
				}
			}

			if err := h.Connect(connCtx, peerInfo); err != nil {
				fmt.Printf("  Reconnection failed: %v\n\n", err)
			} else {
				fmt.Printf("  Reconnected successfully!\n\n")
			}
		}
	}

	if checkImmediately {
		time.Sleep(2 * time.Second)
		reconnectPeers()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconnectPeers()
		}
	}
}

func maintainBootstrapConnections(ctx context.Context, h host.Host, bootstrapPeers []string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, addr := range bootstrapPeers {
				maddr, err := multiaddr.NewMultiaddr(addr)
				if err != nil {
					continue
				}

				peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
				if err != nil {
					continue
				}

				if h.Network().Connectedness(peerInfo.ID) != network.Connected {
					if err := h.Connect(ctx, *peerInfo); err != nil {
						log.Printf("Failed to maintain bootstrap connection to %s: %v", peerInfo.ID.String()[:16], err)
					} else {
						fmt.Printf("\n[BOOTSTRAP] Reconnected to %s to maintain NAT mapping\n\n", peerInfo.ID.String()[:16])
					}
				}
			}
		}
	}
}
