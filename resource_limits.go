package p2p

import (
	"errors"
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
)

// ResourceLimits overrides limits of the libp2p resource manager.
//
// Every field is optional. A zero value (or empty slice) keeps the go-libp2p
// default for that limit, which is auto-scaled to the host's memory and
// file-descriptor limits. A zero ResourceLimits leaves the resource manager
// entirely to go-libp2p, exactly as before this option existed. Negative values
// are rejected.
//
// Overrides are applied on top of the auto-scaled defaults, so setting one
// field never changes the others. An inbound or outbound limit must not exceed
// its total, whether that total is set here or left at its auto-scaled
// default: raising ConnsInbound above the auto-scaled Conns needs Conns set
// too, or NewClient fails. The resource manager's Prometheus metrics
// (libp2p_rcmgr_*) report the effective limits either way.
type ResourceLimits struct {
	// Conns is the system-wide limit on open connections, inbound and outbound
	// combined. It must not be lower than MaxConnections (the connection
	// manager's high water mark), or the connection manager could never reach it.
	Conns int

	// ConnsInbound is the system-wide limit on inbound connections. Raise it on
	// nodes that accept heavy inbound churn, such as bootstrap or DHT server
	// nodes. Must not exceed Conns (set or auto-scaled).
	ConnsInbound int

	// ConnsOutbound is the system-wide limit on outbound connections. Must not
	// exceed Conns (set or auto-scaled).
	ConnsOutbound int

	// Streams is the system-wide limit on open streams, inbound and outbound
	// combined.
	Streams int

	// StreamsInbound is the system-wide limit on inbound streams. Must not
	// exceed Streams (set or auto-scaled).
	StreamsInbound int

	// StreamsOutbound is the system-wide limit on outbound streams. Must not
	// exceed Streams (set or auto-scaled).
	StreamsOutbound int

	// FD is the system-wide limit on file descriptors used for connections.
	FD int

	// PeerConns is the limit on connections to or from any single peer.
	PeerConns int

	// PeerConnsInbound is the limit on inbound connections from any single
	// peer. Must not exceed PeerConns (set or auto-scaled).
	PeerConnsInbound int

	// PeerStreamsInbound is the limit on inbound streams from any single peer.
	PeerStreamsInbound int

	// IPv4SubnetLimits caps connections per IPv4 subnet, matched by source
	// address. When non-empty it replaces the go-libp2p default (8 connections per
	// /32, i.e. per IP address) as a whole, so include every prefix you want
	// limited. Loopback stays unlimited.
	IPv4SubnetLimits []SubnetConnLimit

	// IPv6SubnetLimits caps connections per IPv6 subnet. When non-empty it replaces
	// the go-libp2p defaults (8 per /56 and 64 per /48) as a whole.
	IPv6SubnetLimits []SubnetConnLimit
}

// SubnetConnLimit caps the number of connections from all addresses sharing
// the same PrefixLength-bit network prefix.
type SubnetConnLimit struct {
	// PrefixLength is the subnet size in bits: 0-32 for IPv4, 0-128 for IPv6.
	// 32 (IPv4) or 128 (IPv6) limits each address on its own.
	PrefixLength int

	// Conns is the maximum number of connections per subnet. Must be positive.
	Conns int
}

var (
	errInvalidResourceLimits      = errors.New("invalid resource limits")
	errSystemLimitsNotInspectable = errors.New("system scope does not expose its limits")
)

type namedLimit struct {
	name  string
	value int
}

// counts returns the scalar limits by field name.
func (l ResourceLimits) counts() []namedLimit {
	return []namedLimit{
		{"Conns", l.Conns},
		{"ConnsInbound", l.ConnsInbound},
		{"ConnsOutbound", l.ConnsOutbound},
		{"Streams", l.Streams},
		{"StreamsInbound", l.StreamsInbound},
		{"StreamsOutbound", l.StreamsOutbound},
		{"FD", l.FD},
		{"PeerConns", l.PeerConns},
		{"PeerConnsInbound", l.PeerConnsInbound},
		{"PeerStreamsInbound", l.PeerStreamsInbound},
	}
}

// isZero reports whether no limit has been overridden.
func (l ResourceLimits) isZero() bool {
	if len(l.IPv4SubnetLimits) > 0 || len(l.IPv6SubnetLimits) > 0 {
		return false
	}
	for _, f := range l.counts() {
		if f.value != 0 {
			return false
		}
	}

	return true
}

// validate checks the overrides on their own and against the connection
// manager's high water mark.
func (l ResourceLimits) validate(maxConns int) error {
	for _, f := range l.counts() {
		if f.value < 0 {
			return fmt.Errorf("%w: %s must not be negative, got %d", errInvalidResourceLimits, f.name, f.value)
		}
	}

	if l.Conns > 0 && l.Conns < maxConns {
		return fmt.Errorf("%w: Conns (%d) is below MaxConnections (%d); the connection manager could never reach its high water mark",
			errInvalidResourceLimits, l.Conns, maxConns)
	}

	if err := validateSubnetLimits("IPv4SubnetLimits", l.IPv4SubnetLimits, 32); err != nil {
		return err
	}

	return validateSubnetLimits("IPv6SubnetLimits", l.IPv6SubnetLimits, 128)
}

func validateSubnetLimits(name string, limits []SubnetConnLimit, maxPrefix int) error {
	for i, s := range limits {
		if s.PrefixLength < 0 || s.PrefixLength > maxPrefix {
			return fmt.Errorf("%w: %s[%d].PrefixLength must be between 0 and %d, got %d",
				errInvalidResourceLimits, name, i, maxPrefix, s.PrefixLength)
		}
		if s.Conns <= 0 {
			return fmt.Errorf("%w: %s[%d].Conns must be positive, got %d", errInvalidResourceLimits, name, i, s.Conns)
		}
	}

	return nil
}

// resourceManagerOptions returns the host option installing a resource manager
// built from l, or no options when nothing is overridden.
func resourceManagerOptions(l ResourceLimits, maxConns int) ([]libp2p.Option, error) {
	mgr, err := buildResourceManager(l, maxConns)
	if err != nil || mgr == nil {
		return nil, err
	}

	return []libp2p.Option{libp2p.ResourceManager(mgr)}, nil
}

// checkDirectionalLimits rejects an inbound or outbound limit above its total.
// The total is either overridden too or the auto-scaled default; rcmgr checks
// both, so a larger directional limit would be capped at the total silently.
func checkDirectionalLimits(l ResourceLimits, effective rcmgr.ConcreteLimitConfig) error {
	partial := effective.ToPartialLimitConfig()
	sys, peerDefault := partial.System, partial.PeerDefault

	pairs := []struct {
		part, total       string
		partSet, totalSet bool
		partVal, totalVal rcmgr.LimitVal
	}{
		{"ConnsInbound", "Conns", l.ConnsInbound > 0, l.Conns > 0, sys.ConnsInbound, sys.Conns},
		{"ConnsOutbound", "Conns", l.ConnsOutbound > 0, l.Conns > 0, sys.ConnsOutbound, sys.Conns},
		{"StreamsInbound", "Streams", l.StreamsInbound > 0, l.Streams > 0, sys.StreamsInbound, sys.Streams},
		{"StreamsOutbound", "Streams", l.StreamsOutbound > 0, l.Streams > 0, sys.StreamsOutbound, sys.Streams},
		{"PeerConnsInbound", "PeerConns", l.PeerConnsInbound > 0, l.PeerConns > 0, peerDefault.ConnsInbound, peerDefault.Conns},
	}
	for _, p := range pairs {
		if !p.partSet && !p.totalSet {
			continue // both go-libp2p defaults
		}

		part, total := p.partVal.Build(0), p.totalVal.Build(0)
		if part <= total {
			continue
		}
		if p.totalSet {
			return fmt.Errorf("%w: %s (%d) exceeds %s (%d)", errInvalidResourceLimits, p.part, part, p.total, total)
		}

		return fmt.Errorf("%w: %s (%d) exceeds the auto-scaled %s (%d); set %s as well",
			errInvalidResourceLimits, p.part, part, p.total, total, p.total)
	}

	return nil
}

// buildResourceManager returns a resource manager with the overrides applied
// on top of the same auto-scaled defaults go-libp2p would otherwise use. It
// returns nil when nothing is overridden, so that go-libp2p builds its own
// default resource manager unchanged.
func buildResourceManager(l ResourceLimits, maxConns int) (network.ResourceManager, error) {
	if l.isZero() {
		//nolint:nilnil // nil manager with nil error means "use go-libp2p's default resource manager".
		return nil, nil
	}

	if err := l.validate(maxConns); err != nil {
		return nil, err
	}

	// Mirror libp2p.DefaultResourceManager: library defaults plus the bundled
	// service limits (identify, ping, autonat, ...), scaled to this host.
	defaults := rcmgr.DefaultLimits
	libp2p.SetDefaultServiceLimits(&defaults)

	overrides := rcmgr.PartialLimitConfig{
		System: rcmgr.ResourceLimits{
			Conns:           rcmgr.LimitVal(l.Conns),
			ConnsInbound:    rcmgr.LimitVal(l.ConnsInbound),
			ConnsOutbound:   rcmgr.LimitVal(l.ConnsOutbound),
			Streams:         rcmgr.LimitVal(l.Streams),
			StreamsInbound:  rcmgr.LimitVal(l.StreamsInbound),
			StreamsOutbound: rcmgr.LimitVal(l.StreamsOutbound),
			FD:              rcmgr.LimitVal(l.FD),
		},
		PeerDefault: rcmgr.ResourceLimits{
			Conns:          rcmgr.LimitVal(l.PeerConns),
			ConnsInbound:   rcmgr.LimitVal(l.PeerConnsInbound),
			StreamsInbound: rcmgr.LimitVal(l.PeerStreamsInbound),
		},
	}

	effective := overrides.Build(defaults.AutoScale())
	if err := checkDirectionalLimits(l, effective); err != nil {
		return nil, err
	}

	limiter := rcmgr.NewFixedLimiter(effective)

	// A nil result leaves that address family's go-libp2p default in place.
	opts := []rcmgr.Option{
		rcmgr.WithLimitPerSubnet(toConnLimitPerSubnet(l.IPv4SubnetLimits), toConnLimitPerSubnet(l.IPv6SubnetLimits)),
	}

	mgr, err := rcmgr.NewResourceManager(limiter, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource manager: %w", err)
	}

	return mgr, nil
}

func toConnLimitPerSubnet(limits []SubnetConnLimit) []rcmgr.ConnLimitPerSubnet {
	if len(limits) == 0 {
		return nil // keep go-libp2p's default for this address family
	}

	out := make([]rcmgr.ConnLimitPerSubnet, len(limits))
	for i, s := range limits {
		out[i] = rcmgr.ConnLimitPerSubnet{PrefixLength: s.PrefixLength, ConnCount: s.Conns}
	}

	return out
}

// logSystemLimits logs the resource manager's effective system-scope limits,
// whether they came from ResourceLimits or from go-libp2p's auto-scaling.
func logSystemLimits(rm network.ResourceManager, log logger) {
	err := rm.ViewSystem(func(s network.ResourceScope) error {
		limiter, ok := s.(rcmgr.ResourceScopeLimiter)
		if !ok {
			return errSystemLimitsNotInspectable
		}

		limit := limiter.Limit()
		log.Infof("Resource manager system limits: conns=%d inbound=%d outbound=%d streams=%d streamsInbound=%d streamsOutbound=%d fd=%d memory=%d",
			limit.GetConnTotalLimit(),
			limit.GetConnLimit(network.DirInbound),
			limit.GetConnLimit(network.DirOutbound),
			limit.GetStreamTotalLimit(),
			limit.GetStreamLimit(network.DirInbound),
			limit.GetStreamLimit(network.DirOutbound),
			limit.GetFDLimit(),
			limit.GetMemoryLimit(),
		)

		return nil
	})
	if err != nil {
		log.Warnf("Could not read resource manager system limits: %v", err)
	}
}
