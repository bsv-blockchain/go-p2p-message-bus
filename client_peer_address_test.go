package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

const (
	testDirectAddr  = "/ip4/10.1.2.3/tcp/9905"
	testCircuitAddr = "/ip4/10.9.9.9/tcp/9905/p2p/QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N/p2p-circuit"

	// prefixDecodesLen is the length of the base58 prefix of an Ed25519 peer ID
	// that decodes to a different, valid identity-multihash peer ID.
	prefixDecodesLen = 12

	eventuallyTimeout = 5 * time.Second
	eventuallyTick    = 20 * time.Millisecond
)

var peerAddressProtocols = []string{peerAddressRequestProtocol, peerAddressRequestProtocolV2} //nolint:gochecknoglobals // test table

func newTestPeerID(t *testing.T) peer.ID {
	t.Helper()
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(privKey)
	require.NoError(t, err)
	return id
}

// newConnectedClients starts two DHT-off clients on loopback with a short
// peer-address stream deadline and connects b to a.
func newConnectedClients(t *testing.T, streamTimeout time.Duration) (a, b *client) {
	t.Helper()

	newClient := func(name string) *client {
		privKey, err := GeneratePrivateKey()
		require.NoError(t, err)
		cl, err := NewClient(Config{
			Name:                     name,
			PrivateKey:               privKey,
			Port:                     0,
			AllowPrivateIPs:          true,
			DHTMode:                  "off",
			peerAddressStreamTimeout: streamTimeout,
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, cl.Close()) })
		return cl.(*client)
	}

	a = newClient("peer-a")
	b = newClient("peer-b")
	require.Equal(t, streamTimeout, a.peerAddrStreamTimeout)

	addrs := a.host.Addrs()
	require.NotEmpty(t, addrs)
	addrBase := addrs[0]
	for _, addr := range addrs {
		if strings.Contains(addr.String(), "127.0.0.1") {
			addrBase = addr
			break
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
	defer cancel()
	require.NoError(t, b.Connect(ctx, fmt.Sprintf("%s/p2p/%s", addrBase, a.host.ID())))
	require.Eventually(t, func() bool {
		return a.host.Network().Connectedness(b.host.ID()) == network.Connected
	}, eventuallyTimeout, eventuallyTick)

	return a, b
}

func openPeerAddressStream(t *testing.T, from, to *client, proto string) network.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
	defer cancel()
	stream, err := from.host.NewStream(ctx, to.host.ID(), protocol.ID(proto))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Reset() })
	require.NoError(t, stream.SetDeadline(time.Now().Add(eventuallyTimeout)))
	return stream
}

func TestReadPeerIDRequest(t *testing.T) {
	validID := newTestPeerID(t)
	encoded := validID.String()
	v1 := protocol.ID(peerAddressRequestProtocol)
	v2 := protocol.ID(peerAddressRequestProtocolV2)

	// The premise of the minPeerIDBytes guard: this prefix decodes cleanly.
	prefixID, err := peer.Decode(encoded[:prefixDecodesLen])
	require.NoError(t, err)
	require.NotEqual(t, validID, prefixID)
	require.Less(t, len(prefixID), minPeerIDBytes)

	tests := []struct {
		name    string
		proto   protocol.ID
		reader  io.Reader
		wantID  peer.ID
		wantErr error
	}{
		{name: "v2 single write then EOF", proto: v2, reader: strings.NewReader(encoded), wantID: validID},
		{name: "v2 fragmented one byte at a time", proto: v2, reader: iotest.OneByteReader(strings.NewReader(encoded)), wantID: validID},
		{name: "v2 oversized", proto: v2, reader: bytes.NewReader(bytes.Repeat([]byte("Q"), maxPeerIDRequestBytes+1)), wantErr: errPeerIDRequestTooLarge},
		{name: "v2 oversized split across reads", proto: v2, reader: iotest.HalfReader(bytes.NewReader(bytes.Repeat([]byte("Q"), 2*maxPeerIDRequestBytes))), wantErr: errPeerIDRequestTooLarge},
		{name: "v2 garbage", proto: v2, reader: strings.NewReader("not-a-peer-id"), wantErr: errPeerIDRequestInvalid},
		{name: "v2 empty", proto: v2, reader: strings.NewReader(""), wantErr: errPeerIDRequestInvalid},
		{name: "v2 truncated", proto: v2, reader: strings.NewReader(encoded[:len(encoded)/2]), wantErr: errPeerIDRequestInvalid},
		{name: "v2 prefix that decodes as a short peer ID", proto: v2, reader: strings.NewReader(encoded[:prefixDecodesLen]), wantErr: errPeerIDRequestInvalid},
		{name: "v2 read error", proto: v2, reader: iotest.ErrReader(io.ErrClosedPipe), wantErr: io.ErrClosedPipe},

		// 1.0.0 requesters never half-close: the single read is the request.
		{name: "v1 single write no EOF", proto: v1, reader: io.MultiReader(strings.NewReader(encoded), neverReader{}), wantID: validID},
		{name: "v1 single write then EOF", proto: v1, reader: strings.NewReader(encoded), wantID: validID},
		{name: "v1 oversized", proto: v1, reader: bytes.NewReader(bytes.Repeat([]byte("Q"), 4*maxPeerIDRequestBytes)), wantErr: errPeerIDRequestTooLarge},
		{name: "v1 garbage", proto: v1, reader: strings.NewReader("not-a-peer-id"), wantErr: errPeerIDRequestInvalid},
		{name: "v1 empty then EOF", proto: v1, reader: strings.NewReader(""), wantErr: errPeerIDRequestInvalid},
		{name: "v1 read error", proto: v1, reader: iotest.ErrReader(io.ErrClosedPipe), wantErr: io.ErrClosedPipe},
		// A fragmented legacy request is not reassembled. Whatever the single
		// read returned is decoded once; a prefix that decodes to a different,
		// too-short peer ID is rejected instead of being answered for.
		{name: "v1 fragmented is not reassembled", proto: v1, reader: iotest.OneByteReader(strings.NewReader(encoded)), wantErr: errPeerIDRequestInvalid},
		{name: "v1 prefix that decodes as a short peer ID", proto: v1, reader: io.MultiReader(strings.NewReader(encoded[:prefixDecodesLen]), neverReader{}), wantErr: errPeerIDRequestInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := readPeerIDRequest(tt.reader, tt.proto)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Empty(t, id)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantID, id)
		})
	}
}

// neverReader models a 1.0.0 peer that has written its request and now waits
// for the reply: it never half-closes, so a second Read would block until the
// deadline. The legacy path must not get that far.
type neverReader struct{}

func (neverReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("unexpected read past a complete legacy request: %w", io.ErrUnexpectedEOF)
}

func TestSanitizePeerName(t *testing.T) {
	long := strings.Repeat("a", maxPeerNameLen+50)
	multibyte := strings.Repeat("é", maxPeerNameLen) // 2 bytes each

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "teranode-1", want: "teranode-1"},
		{name: "empty", in: "", want: ""},
		{name: "spaces kept", in: "node one", want: "node one"},
		{name: "newline and control stripped", in: "evil\nname\r\x00\x1b[31m", want: "evilname[31m"},
		{name: "truncated", in: long, want: long[:maxPeerNameLen]},
		{name: "truncated on rune boundary", in: multibyte, want: strings.Repeat("é", maxPeerNameLen/2)},
		{name: "invalid utf8 bounded", in: strings.Repeat("\xff", 1<<20), want: strings.Repeat("�", maxPeerNameLen/3)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizePeerName(tt.in)
			require.Equal(t, tt.want, got)
			require.LessOrEqual(t, len(got), maxPeerNameLen)
		})
	}
}

func TestPeerTrackerUpdateNameSanitizes(t *testing.T) {
	tracker := newPeerTracker()
	id := newTestPeerID(t)

	stored := tracker.updateName(id, strings.Repeat("x", 10*maxPeerNameLen)+"\n")
	require.Len(t, stored, maxPeerNameLen)
	require.Equal(t, stored, tracker.getName(id))
}

func TestPeerAddressLimiterAllow(t *testing.T) {
	limiter := newPeerAddressLimiter()
	attacker := newTestPeerID(t)
	honest := newTestPeerID(t)
	now := time.Now()

	for i := 0; i < peerAddressRequestsPerWindow; i++ {
		allowed, first := limiter.allow(attacker, now)
		require.True(t, allowed, "request %d within budget", i)
		require.False(t, first)
	}

	allowed, first := limiter.allow(attacker, now)
	require.False(t, allowed)
	require.True(t, first, "first rejection is flagged for logging")

	allowed, first = limiter.allow(attacker, now.Add(peerAddressRequestWindow/2))
	require.False(t, allowed)
	require.False(t, first, "later rejections in the window are not flagged")

	// One peer's budget does not affect another's.
	allowed, _ = limiter.allow(honest, now)
	require.True(t, allowed)

	// The budget resets once the window has elapsed and stale entries are gone.
	later := now.Add(peerAddressRequestWindow)
	allowed, _ = limiter.allow(attacker, later)
	require.True(t, allowed)
	limiter.mu.Lock()
	_, honestTracked := limiter.windows[honest]
	limiter.mu.Unlock()
	require.False(t, honestTracked, "expired windows must be pruned")
}

func TestShouldRequestPeerAddresses(t *testing.T) {
	requested := make(map[peer.ID]time.Time)
	target := newTestPeerID(t)
	other := newTestPeerID(t)
	now := time.Now()

	require.True(t, shouldRequestPeerAddresses(requested, target, now))
	requested[target] = now
	require.False(t, shouldRequestPeerAddresses(requested, target, now))
	require.False(t, shouldRequestPeerAddresses(requested, target, now.Add(peerAddressRetryInterval-time.Second)))
	require.True(t, shouldRequestPeerAddresses(requested, target, now.Add(peerAddressRetryInterval)))

	requested[other] = now
	pruneRequested(requested, map[peer.ID]struct{}{target: {}})
	require.Contains(t, requested, target)
	require.NotContains(t, requested, other)
}

func TestHandlePeerAddressRequestReturnsDirectAddressesOnly(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)

	target := newTestPeerID(t)
	direct := multiaddr.StringCast(testDirectAddr)
	circuit := multiaddr.StringCast(testCircuitAddr)
	a.host.Peerstore().AddAddrs(target, []multiaddr.Multiaddr{direct, circuit}, peerstore.PermanentAddrTTL)

	stream := openPeerAddressStream(t, b, a, peerAddressRequestProtocolV2)

	// Write the ID in two fragments to exercise reassembly on the handler side.
	encoded := []byte(target.String())
	_, err := stream.Write(encoded[:10])
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	_, err = stream.Write(encoded[10:])
	require.NoError(t, err)
	require.NoError(t, stream.CloseWrite())

	raw, err := io.ReadAll(stream)
	require.NoError(t, err)

	var addrs []string
	require.NoError(t, json.Unmarshal(raw, &addrs))
	require.Equal(t, []string{testDirectAddr}, addrs)
}

// A 1.0.0 requester writes once and never half-closes; it must still be answered.
func TestHandlePeerAddressRequestLegacyFramingStillAnswered(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)

	target := newTestPeerID(t)
	direct := multiaddr.StringCast(testDirectAddr)
	a.host.Peerstore().AddAddrs(target, []multiaddr.Multiaddr{direct}, peerstore.PermanentAddrTTL)

	stream := openPeerAddressStream(t, b, a, peerAddressRequestProtocol)
	_, err := stream.Write([]byte(target.String()))
	require.NoError(t, err)

	raw, err := io.ReadAll(stream)
	require.NoError(t, err)

	var addrs []string
	require.NoError(t, json.Unmarshal(raw, &addrs))
	require.Equal(t, []string{testDirectAddr}, addrs)
}

func TestHandlePeerAddressRequestUnknownPeerReturnsEmptyList(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)

	stream := openPeerAddressStream(t, b, a, peerAddressRequestProtocolV2)
	_, err := stream.Write([]byte(newTestPeerID(t).String()))
	require.NoError(t, err)
	require.NoError(t, stream.CloseWrite())

	raw, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.JSONEq(t, "[]", string(raw))
}

func TestHandlePeerAddressRequestCapsSharedAddresses(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)

	target := newTestPeerID(t)
	many := make([]multiaddr.Multiaddr, 0, 2*maxSharedPeerAddresses)
	for i := 0; i < 2*maxSharedPeerAddresses; i++ {
		many = append(many, multiaddr.StringCast(fmt.Sprintf("/ip4/10.0.%d.%d/tcp/9905", i/256, i%256)))
	}
	a.host.Peerstore().AddAddrs(target, many, peerstore.PermanentAddrTTL)

	stream := openPeerAddressStream(t, b, a, peerAddressRequestProtocolV2)
	addrs, err := b.exchangePeerAddressRequest(stream, target)
	require.NoError(t, err)
	require.Len(t, addrs, maxSharedPeerAddresses)
}

func TestHandlePeerAddressRequestSilentStreamIsReleased(t *testing.T) {
	const handlerTimeout = 300 * time.Millisecond
	a, b := newConnectedClients(t, handlerTimeout)

	for _, proto := range peerAddressProtocols {
		t.Run(proto, func(t *testing.T) {
			stream := openPeerAddressStream(t, b, a, proto)

			// Write nothing. The handler must give up on its own and reset the
			// stream, which the requester observes as an error well before its
			// own deadline.
			start := time.Now()
			raw, err := io.ReadAll(stream)
			require.Less(t, time.Since(start), 3*time.Second, "handler did not release the silent stream")
			require.Empty(t, raw)
			require.Error(t, err, "an abandoned exchange must surface as a reset, not a clean EOF")
		})
	}
}

func TestHandlePeerAddressRequestOversizedIsRejected(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)

	for _, proto := range peerAddressProtocols {
		t.Run(proto, func(t *testing.T) {
			stream := openPeerAddressStream(t, b, a, proto)
			_, err := stream.Write(bytes.Repeat([]byte("Q"), 4*maxPeerIDRequestBytes))
			require.NoError(t, err)
			require.NoError(t, stream.CloseWrite())

			raw, err := io.ReadAll(stream)
			require.Empty(t, raw, "oversized request must not be answered")
			require.Error(t, err)
		})
	}
}

func TestHandlePeerAddressRequestRateLimited(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	target := newTestPeerID(t)

	// Spend b's budget on a's limiter directly; the wire test then only needs
	// one real exchange on each side of the limit.
	start := time.Now()
	for i := 0; i < peerAddressRequestsPerWindow-1; i++ {
		allowed, _ := a.peerAddrLimiter.allow(b.host.ID(), start)
		require.True(t, allowed)
	}

	stream := openPeerAddressStream(t, b, a, peerAddressRequestProtocolV2)
	addrs, err := b.exchangePeerAddressRequest(stream, target)
	require.NoError(t, err, "last request within budget is answered")
	require.Empty(t, addrs)

	stream = openPeerAddressStream(t, b, a, peerAddressRequestProtocolV2)
	_, err = b.exchangePeerAddressRequest(stream, target)
	require.Less(t, time.Since(start), peerAddressRequestWindow, "test must complete inside one window")
	require.Error(t, err, "request over budget must be reset, not answered")
}

// installBlackHoleHandler makes c accept peer-address streams on both protocol
// versions and never respond until the test ends, modelling a Sybil that wants
// to wedge the requester. restore reinstates the real handler.
func installBlackHoleHandler(t *testing.T, c *client) (accepted *atomic.Int32, restore func()) {
	t.Helper()
	accepted = new(atomic.Int32)
	done := make(chan struct{})
	handler := func(s network.Stream) {
		accepted.Add(1)
		<-done
		_ = s.Reset()
	}
	for _, proto := range peerAddressProtocols {
		c.host.SetStreamHandler(protocol.ID(proto), handler)
	}
	t.Cleanup(func() { close(done) })
	return accepted, func() {
		for _, proto := range peerAddressProtocols {
			c.host.SetStreamHandler(protocol.ID(proto), c.handlePeerAddressRequest)
		}
	}
}

func lookupInFlight(c *client, target peer.ID) bool {
	c.addrLookupsMu.Lock()
	defer c.addrLookupsMu.Unlock()
	_, ok := c.addrLookupsInFlight[target]
	return ok
}

func TestRequestPeerAddressesBlackHoleResponderDoesNotWedge(t *testing.T) {
	const streamTimeout = 300 * time.Millisecond
	a, b := newConnectedClients(t, streamTimeout)
	target := newTestPeerID(t)

	accepted, restore := installBlackHoleHandler(t, a)

	// A responder that never answers must not pin the requester, and two
	// concurrent lookups for one target must collapse into a single exchange.
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.requestPeerAddresses(target)
		}()
	}
	wg.Wait()
	require.Less(t, time.Since(start), 3*time.Second, "requester wedged on black-hole responder")
	require.Eventually(t, func() bool { return accepted.Load() == 1 }, eventuallyTimeout, eventuallyTick)
	require.Empty(t, b.host.Peerstore().Addrs(target))
	require.False(t, lookupInFlight(b, target))

	// Once the responder behaves, the very same requester learns the address:
	// the failed attempt left no lasting damage.
	restore()
	direct := multiaddr.StringCast(testDirectAddr)
	a.host.Peerstore().AddAddrs(target, []multiaddr.Multiaddr{direct}, peerstore.PermanentAddrTTL)

	b.requestPeerAddresses(target)

	learned := b.host.Peerstore().Addrs(target)
	require.Len(t, learned, 1)
	require.True(t, learned[0].Equal(direct))
}

// The scan retries a target after peerAddressRetryInterval instead of marking
// it requested forever.
func TestScanTopicPeersRetriesFailedLookup(t *testing.T) {
	const streamTimeout = 300 * time.Millisecond
	a, b := newConnectedClients(t, streamTimeout)
	target := newTestPeerID(t)

	accepted, _ := installBlackHoleHandler(t, a)
	b.peerTracker.recordMessageFrom(target) // target is a known, unconnected topic peer

	requested := make(map[peer.ID]time.Time)
	now := time.Now()

	b.scanTopicPeersForAddresses(requested, now)
	require.Contains(t, requested, target)
	require.Eventually(t, func() bool { return accepted.Load() == 1 }, eventuallyTimeout, eventuallyTick)

	// Within the retry interval the target is not asked for again.
	b.scanTopicPeersForAddresses(requested, now.Add(peerAddressRetryInterval-time.Second))
	require.Eventually(t, func() bool { return !lookupInFlight(b, target) }, eventuallyTimeout, eventuallyTick)
	require.EqualValues(t, 1, accepted.Load())

	// Once the interval has elapsed the lookup is retried.
	b.scanTopicPeersForAddresses(requested, now.Add(peerAddressRetryInterval))
	require.Eventually(t, func() bool { return accepted.Load() == 2 }, eventuallyTimeout, eventuallyTick)
}

func TestExchangePeerAddressRequestRejectsOversizedResponse(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	target := newTestPeerID(t)

	huge, err := json.Marshal([]string{strings.Repeat("x", maxPeerAddressResponseBytes)})
	require.NoError(t, err)
	a.host.SetStreamHandler(protocol.ID(peerAddressRequestProtocolV2), func(s network.Stream) {
		defer func() { _ = s.Close() }()
		_, _ = s.Write(huge)
	})

	stream := openPeerAddressStream(t, b, a, peerAddressRequestProtocolV2)
	_, err = b.exchangePeerAddressRequest(stream, target)
	require.ErrorIs(t, err, errPeerAddressResponseTooLarge)
}

func TestRequestPeerAddressesCapsLearnedAddresses(t *testing.T) {
	a, b := newConnectedClients(t, eventuallyTimeout)
	target := newTestPeerID(t)

	many := make([]string, 0, 2*maxSharedPeerAddresses)
	for i := 0; i < 2*maxSharedPeerAddresses; i++ {
		many = append(many, fmt.Sprintf("/ip4/10.0.%d.%d/tcp/9905", i/256, i%256))
	}
	response, err := json.Marshal(many)
	require.NoError(t, err)
	a.host.SetStreamHandler(protocol.ID(peerAddressRequestProtocolV2), func(s network.Stream) {
		defer func() { _ = s.Close() }()
		_, _ = s.Write(response)
	})

	b.requestPeerAddresses(target)
	require.Len(t, b.host.Peerstore().Addrs(target), maxSharedPeerAddresses)
}
