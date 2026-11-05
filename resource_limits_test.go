package p2p

import (
	"fmt"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// autoScaledDefaults is what go-libp2p's DefaultResourceManager builds when no
// resource manager is supplied: the library's own defaults, auto-scaled to the
// host's memory and file-descriptor limits.
func autoScaledDefaults() rcmgr.ConcreteLimitConfig {
	limits := rcmgr.DefaultLimits
	libp2p.SetDefaultServiceLimits(&limits)
	return limits.AutoScale()
}

// newLimitsTestClient starts a hermetic client (no DHT, no bootstrap peers).
func newLimitsTestClient(t *testing.T, limits ResourceLimits) *client {
	t.Helper()

	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:                         testPeerName,
		PrivateKey:                   privKey,
		DHTMode:                      "off",
		DisableDefaultBootstrapPeers: true,
		ResourceLimits:               limits,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cl.Close()) })

	return cl.(*client)
}

func systemLimit(t *testing.T, c *client) rcmgr.Limit {
	t.Helper()

	var limit rcmgr.Limit
	err := c.host.Network().ResourceManager().ViewSystem(func(s network.ResourceScope) error {
		limit = s.(rcmgr.ResourceScopeLimiter).Limit()
		return nil
	})
	require.NoError(t, err)

	return limit
}

func identifyServiceLimit(t *testing.T, c *client) rcmgr.Limit {
	t.Helper()

	var limit rcmgr.Limit
	err := c.host.Network().ResourceManager().ViewService(identify.ServiceName, func(s network.ServiceScope) error {
		limit = s.(rcmgr.ResourceScopeLimiter).Limit()
		return nil
	})
	require.NoError(t, err)

	return limit
}

// lowerInbound returns an inbound connection limit below the auto-scaled
// total, so it is valid without also setting Conns.
func lowerInbound(t *testing.T) int {
	t.Helper()

	total := autoScaledDefaults().ToPartialLimitConfig().System.Conns.Build(0)
	require.Greater(t, total, 10)

	return total - 7
}

func TestResourceLimitsSystemOverrideApplied(t *testing.T) {
	inbound := lowerInbound(t)
	c := newLimitsTestClient(t, ResourceLimits{ConnsInbound: inbound})

	defaults := autoScaledDefaults()
	limit := systemLimit(t, c)

	assert.Equal(t, inbound, limit.GetConnLimit(network.DirInbound))
	// Fields left unset keep the auto-scaled defaults.
	assert.Equal(t, defaults.ToPartialLimitConfig().System.ConnsOutbound.Build(0), limit.GetConnLimit(network.DirOutbound))
	assert.Equal(t, defaults.ToPartialLimitConfig().System.Conns.Build(0), limit.GetConnTotalLimit())
	assert.Equal(t, defaults.ToPartialLimitConfig().System.FD.Build(0), limit.GetFDLimit())
	// libp2p's bundled service limits (identify, ping, ...) are kept.
	assert.Equal(t,
		defaults.ToPartialLimitConfig().Service[identify.ServiceName].StreamsInbound.Build(0),
		identifyServiceLimit(t, c).GetStreamLimit(network.DirInbound))
}

func TestResourceLimitsUnsetKeepsAutoScaledDefaults(t *testing.T) {
	c := newLimitsTestClient(t, ResourceLimits{})

	want := autoScaledDefaults().ToPartialLimitConfig().System
	limit := systemLimit(t, c)

	assert.Equal(t, want.ConnsInbound.Build(0), limit.GetConnLimit(network.DirInbound))
	assert.Equal(t, want.ConnsOutbound.Build(0), limit.GetConnLimit(network.DirOutbound))
	assert.Equal(t, want.Conns.Build(0), limit.GetConnTotalLimit())
	assert.Equal(t, want.StreamsInbound.Build(0), limit.GetStreamLimit(network.DirInbound))
	assert.Equal(t, want.FD.Build(0), limit.GetFDLimit())
}

func TestResourceLimitsPeerOverridesApplied(t *testing.T) {
	c := newLimitsTestClient(t, ResourceLimits{PeerConns: 6, PeerConnsInbound: 3, PeerStreamsInbound: 77})

	other, err := GeneratePrivateKey()
	require.NoError(t, err)
	otherID, err := peer.IDFromPrivateKey(other)
	require.NoError(t, err)

	var limit rcmgr.Limit
	err = c.host.Network().ResourceManager().ViewPeer(otherID, func(s network.PeerScope) error {
		limit = s.(rcmgr.ResourceScopeLimiter).Limit()
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, 6, limit.GetConnTotalLimit())
	assert.Equal(t, 3, limit.GetConnLimit(network.DirInbound))
	assert.Equal(t, 77, limit.GetStreamLimit(network.DirInbound))
	assert.Equal(t,
		autoScaledDefaults().ToPartialLimitConfig().PeerDefault.ConnsOutbound.Build(0),
		limit.GetConnLimit(network.DirOutbound))
}

// openInbound opens n inbound connection scopes from addr and returns how many
// the resource manager admitted. Admitted scopes are released on cleanup.
func openInbound(t *testing.T, rm network.ResourceManager, addr string, n int) int {
	t.Helper()

	maddr, err := multiaddr.NewMultiaddr(addr)
	require.NoError(t, err)

	admitted := 0
	for range n {
		scope, err := rm.OpenConnection(network.DirInbound, true, maddr)
		if err != nil {
			continue
		}
		admitted++
		t.Cleanup(scope.Done)
	}

	return admitted
}

func TestResourceLimitsSubnetOverrideApplied(t *testing.T) {
	c := newLimitsTestClient(t, ResourceLimits{
		IPv4SubnetLimits: []SubnetConnLimit{{PrefixLength: 32, Conns: 2}},
	})
	rm := c.host.Network().ResourceManager()

	assert.Equal(t, 2, openInbound(t, rm, "/ip4/203.0.113.7/tcp/4001", 5))
	// IPv6 was not overridden: go-libp2p's default of 8 per /56 still applies.
	assert.Equal(t, 8, openInbound(t, rm, "/ip6/2001:db8::7/tcp/4001", 12))
}

func TestResourceLimitsSubnetDefaultsWhenUnset(t *testing.T) {
	for _, tt := range []struct {
		name   string
		limits ResourceLimits
	}{
		{name: "nil", limits: ResourceLimits{Conns: 1000}},
		{name: "empty", limits: ResourceLimits{Conns: 1000, IPv4SubnetLimits: []SubnetConnLimit{}, IPv6SubnetLimits: []SubnetConnLimit{}}},
		{name: "only empty", limits: ResourceLimits{IPv4SubnetLimits: []SubnetConnLimit{}, IPv6SubnetLimits: []SubnetConnLimit{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rm := newLimitsTestClient(t, tt.limits).host.Network().ResourceManager()

			// go-libp2p's defaults: 8 connections per IPv4 address, 8 per IPv6 /56.
			assert.Equal(t, 8, openInbound(t, rm, "/ip4/203.0.113.8/tcp/4001", 12))
			assert.Equal(t, 8, openInbound(t, rm, "/ip6/2001:db8::8/tcp/4001", 12))
		})
	}
}

func TestResourceLimitsInvalidRejected(t *testing.T) {
	tests := []struct {
		name   string
		limits ResourceLimits
		config func(*Config)
	}{
		{name: "negative system field", limits: ResourceLimits{ConnsInbound: -1}},
		{name: "negative peer field", limits: ResourceLimits{PeerStreamsInbound: -5}},
		{name: "inbound above total", limits: ResourceLimits{Conns: 100, ConnsInbound: 200}},
		{
			// An unset total is the auto-scaled one; rcmgr would silently cap
			// inbound at it.
			name:   "inbound above auto-scaled total",
			limits: ResourceLimits{ConnsInbound: autoScaledDefaults().ToPartialLimitConfig().System.Conns.Build(0) + 1},
		},
		{
			name:   "inbound streams above auto-scaled total",
			limits: ResourceLimits{StreamsInbound: autoScaledDefaults().ToPartialLimitConfig().System.Streams.Build(0) + 1},
		},
		{
			name:   "peer inbound above auto-scaled peer total",
			limits: ResourceLimits{PeerConnsInbound: autoScaledDefaults().ToPartialLimitConfig().PeerDefault.Conns.Build(0) + 1},
		},
		{name: "outbound above total", limits: ResourceLimits{Conns: 100, ConnsOutbound: 101}},
		{name: "inbound streams above total", limits: ResourceLimits{Streams: 10, StreamsInbound: 11}},
		{name: "peer inbound above peer total", limits: ResourceLimits{PeerConns: 2, PeerConnsInbound: 3}},
		{name: "total below default MaxConnections", limits: ResourceLimits{Conns: 34}},
		{
			name:   "total below configured MaxConnections",
			limits: ResourceLimits{Conns: 99},
			config: func(c *Config) { c.MaxConnections = 100; c.MinConnections = 50 },
		},
		{name: "IPv4 prefix too long", limits: ResourceLimits{IPv4SubnetLimits: []SubnetConnLimit{{PrefixLength: 33, Conns: 1}}}},
		{name: "IPv6 prefix negative", limits: ResourceLimits{IPv6SubnetLimits: []SubnetConnLimit{{PrefixLength: -1, Conns: 1}}}},
		{name: "subnet zero conns", limits: ResourceLimits{IPv4SubnetLimits: []SubnetConnLimit{{PrefixLength: 24, Conns: 0}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			cfg := Config{
				Name:                         testPeerName,
				PrivateKey:                   privKey,
				DHTMode:                      "off",
				DisableDefaultBootstrapPeers: true,
				ResourceLimits:               tt.limits,
			}
			if tt.config != nil {
				tt.config(&cfg)
			}

			cl, err := NewClient(cfg)
			if cl != nil {
				_ = cl.Close()
			}
			require.ErrorIs(t, err, errInvalidResourceLimits)
		})
	}
}

func TestResourceLimitsValidBoundariesAccepted(t *testing.T) {
	// Conns equal to MaxConnections, parts equal to their totals, and the
	// widest legal prefixes are all valid.
	c := newLimitsTestClient(t, ResourceLimits{
		Conns:            35,
		ConnsInbound:     35,
		ConnsOutbound:    35,
		PeerConns:        4,
		PeerConnsInbound: 4,
		IPv4SubnetLimits: []SubnetConnLimit{{PrefixLength: 0, Conns: 1000}, {PrefixLength: 32, Conns: 4}},
		IPv6SubnetLimits: []SubnetConnLimit{{PrefixLength: 128, Conns: 4}},
	})

	assert.Equal(t, 35, systemLimit(t, c).GetConnTotalLimit())
}

func TestResourceLimitsLoggedAtStartup(t *testing.T) {
	for _, tt := range []struct {
		name   string
		limits ResourceLimits
		want   string
	}{
		{name: "overridden", limits: ResourceLimits{Conns: 8000, ConnsInbound: 4321}, want: "conns=8000 inbound=4321 "},
		{
			name: "auto-scaled",
			want: fmt.Sprintf("inbound=%d ", autoScaledDefaults().ToPartialLimitConfig().System.ConnsInbound.Build(0)),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			privKey, err := GeneratePrivateKey()
			require.NoError(t, err)

			logs := &captureLogger{}
			cl, err := NewClient(Config{
				Name:                         testPeerName,
				PrivateKey:                   privKey,
				Logger:                       logs,
				DHTMode:                      "off",
				DisableDefaultBootstrapPeers: true,
				ResourceLimits:               tt.limits,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, cl.Close()) })

			var line string
			for _, l := range strings.Split(logs.String(), "\n") {
				if strings.Contains(l, "Resource manager system limits:") {
					require.Empty(t, line, "system limits logged more than once")
					line = l
				}
			}
			require.NotEmpty(t, line, "system limits not logged")
			assert.Contains(t, line, "[INFO]")
			assert.Contains(t, line, tt.want)
		})
	}
}

// TestResourceLimitsReportedToPrometheus reads the process-global
// libp2p_rcmgr_limit gauge, which every new resource manager overwrites. It
// relies on no test in this package running in parallel.
func TestResourceLimitsReportedToPrometheus(t *testing.T) {
	newLimitsTestClient(t, ResourceLimits{Conns: 5000, ConnsInbound: 2345})

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	var found bool
	for _, mf := range families {
		if mf.GetName() != "libp2p_rcmgr_limit" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["scope"] == "system" && labels["resource"] == "conns_inbound" {
				found = true
				assert.InDelta(t, 2345, m.GetGauge().GetValue(), 0)
			}
		}
	}
	require.True(t, found, "libp2p_rcmgr_limit{scope=system,resource=conns_inbound} not exported")
}
