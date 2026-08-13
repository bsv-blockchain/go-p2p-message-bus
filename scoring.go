package p2p

import (
	"errors"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
)

// defaultPeerScoreInspectPeriod is how often PeerScoreInspect is invoked when set
// but Config.PeerScoreInspectPeriod is left zero.
const defaultPeerScoreInspectPeriod = 10 * time.Second

// ErrIncompletePeerScoreConfig is returned when only one of PeerScoreParams /
// PeerScoreThresholds is provided. GossipSub requires both together.
var ErrIncompletePeerScoreConfig = errors.New("PeerScoreParams and PeerScoreThresholds must both be set")

// DefaultPeerScoreParams returns penalty-only GossipSub peer score parameters that
// provide baseline Sybil resistance without requiring per-topic knowledge:
//
//   - IPColocationFactor penalizes many peers sharing the same IP address, so an
//     attacker cannot cheaply run a swarm of Sybils from a handful of hosts.
//   - BehaviourPenalty punishes GRAFT/PRUNE flooding and broken IWANT promises,
//     re-enabling gossipsub's own flood defenses (which are no-ops when scoring is off).
//
// The scheme is penalty-only: a well-behaved peer sits at score 0 and only
// misbehavior or colocation drives its score negative. There are no positive terms
// (no per-topic TopicScoreParams, AppSpecificScore returns 0), so no peer scores above
// 0 by default. Callers who want a positive buffer for honest/trusted peers should set
// per-topic TimeInMeshWeight/FirstMessageDeliveriesWeight and/or Config.AppSpecificScore.
//
// A returned pointer is fresh on every call, so callers may mutate it safely.
func DefaultPeerScoreParams() *pubsub.PeerScoreParams {
	return &pubsub.PeerScoreParams{
		Topics: make(map[string]*pubsub.TopicScoreParams),

		// App-specific scoring is unused by default, but the pubsub validator requires
		// a non-nil function whenever atomic validation is on. Return 0 (no effect).
		// Overridden by Config.AppSpecificScore when set.
		AppSpecificScore:  func(peer.ID) float64 { return 0 },
		AppSpecificWeight: 1,

		// Penalize peers sharing an exact IP once more than the threshold do. pubsub
		// keys colocation by exact remote IP (not a subnet), and the penalty scales
		// with the square of the surplus over the threshold, so a Sybil swarm crossing
		// the gossip/graylist thresholds drops out of the mesh quickly. Trusted static
		// and bootstrap peers are exempt (registered as direct peers).
		IPColocationFactorWeight:    -35,
		IPColocationFactorThreshold: 10,

		// Penalize protocol misbehavior (excess GRAFT, broken IWANT promises). Only
		// the surplus over the threshold is penalized, so bursts under heavy gossip are
		// tolerated before any penalty applies.
		BehaviourPenaltyWeight:    -16,
		BehaviourPenaltyThreshold: 6,
		BehaviourPenaltyDecay:     0.928, // penalty half-life ~1.9 min at DecayInterval

		DecayInterval: 12 * time.Second,
		DecayToZero:   0.01,

		// Keep scores for a while after disconnect so churn does not wash out penalties.
		RetainScore: 6 * time.Hour,
	}
}

// DefaultPeerScoreThresholds returns GossipSub score thresholds matched to the
// penalty-only DefaultPeerScoreParams. AcceptPXThreshold is 0 (not a positive value):
// because the defaults award no positive score, a positive threshold would be
// unreachable and reject all peer exchange. At 0, peer records offered via PRUNE are
// accepted from any non-negatively-scored peer and rejected from peers that scoring has
// driven negative (Sybil colocation or misbehavior) - which is the vector the mesh
// needs closed. The negative thresholds leave generous headroom so honest peers behind
// a shared IP or with a transient behaviour blip are not graylisted.
func DefaultPeerScoreThresholds() *pubsub.PeerScoreThresholds {
	return &pubsub.PeerScoreThresholds{
		GossipThreshold:             -2000,
		PublishThreshold:            -4000,
		GraylistThreshold:           -8000,
		AcceptPXThreshold:           0,
		OpportunisticGraftThreshold: 0,
	}
}

// resolvePeerScoreConfig returns the effective peer score params/thresholds for a
// Config, or (nil, nil) when scoring is disabled. It errors if exactly one of the two
// is supplied. Config.AppSpecificScore, when set, overrides the params' function.
func resolvePeerScoreConfig(config Config) (*pubsub.PeerScoreParams, *pubsub.PeerScoreThresholds, error) {
	params, thresholds := config.PeerScoreParams, config.PeerScoreThresholds

	if config.EnablePeerScoring {
		if params == nil {
			params = DefaultPeerScoreParams()
		}
		if thresholds == nil {
			thresholds = DefaultPeerScoreThresholds()
		}
	}

	if params == nil && thresholds == nil {
		return nil, nil, nil
	}
	if params == nil || thresholds == nil {
		return nil, nil, ErrIncompletePeerScoreConfig
	}

	if config.AppSpecificScore != nil {
		params.AppSpecificScore = config.AppSpecificScore
	}

	return params, thresholds, nil
}

// buildPubSubOptions assembles the GossipSub options from a Config: peer exchange
// (on unless disabled), peer scoring (off unless configured), trusted direct peers, and
// an optional score-inspection callback. It warns when the mesh is left in the
// spec-violating state of peer exchange on with no scoring.
func buildPubSubOptions(config Config, log logger) ([]pubsub.Option, error) {
	var opts []pubsub.Option

	if config.DisablePeerExchange {
		log.Infof("GossipSub peer exchange disabled")
	} else {
		opts = append(opts, pubsub.WithPeerExchange(true))
	}

	params, thresholds, err := resolvePeerScoreConfig(config)
	if err != nil {
		return nil, err
	}

	switch {
	case params != nil:
		opts = append(opts, pubsub.WithPeerScore(params, thresholds))

		// Exempt trusted peers from scoring: direct peers bypass the mesh score checks
		// entirely, so a graylisted score can never eclipse a static or bootstrap link.
		if direct := directPeers(config, log); len(direct) > 0 {
			opts = append(opts, pubsub.WithDirectPeers(direct))
			log.Infof("GossipSub scoring: %d trusted peer(s) exempt as direct peers", len(direct))
		}

		if config.PeerScoreInspect != nil {
			period := config.PeerScoreInspectPeriod
			if period <= 0 {
				period = defaultPeerScoreInspectPeriod
			}
			opts = append(opts, pubsub.WithPeerScoreInspect(config.PeerScoreInspect, period))
		}

		log.Infof("GossipSub peer scoring enabled (AcceptPXThreshold=%.0f, GraylistThreshold=%.0f)",
			thresholds.AcceptPXThreshold, thresholds.GraylistThreshold)
	case !config.DisablePeerExchange:
		log.Warnf("GossipSub peer exchange is enabled without peer scoring; Sybil peers can capture the mesh - set EnablePeerScoring or DisablePeerExchange")
	}

	return opts, nil
}

// directPeers is the deduplicated set of StaticPeers and explicitly-configured
// BootstrapPeers, used as GossipSub direct peers. Default IPFS bootstrap peers (used
// when BootstrapPeers is empty) are intentionally excluded - they are not trusted.
func directPeers(config Config, log logger) []peer.AddrInfo {
	seen := make(map[peer.ID]struct{})

	var out []peer.AddrInfo
	for _, ai := range append(parsePeerMultiaddrs(config.StaticPeers, log), parsePeerMultiaddrs(config.BootstrapPeers, log)...) {
		if _, ok := seen[ai.ID]; ok {
			continue
		}
		seen[ai.ID] = struct{}{}
		out = append(out, ai)
	}

	return out
}
