package p2p

import (
	"context"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

// a syntactically valid peer multiaddr for direct-peer tests.
const testDirectPeerAddr = "/ip4/1.2.3.4/tcp/9905/p2p/12D3KooWH5JVqGdaw7JEizmysCfRRcPGTFfvRJF7Hkure7oQWYnb"

func TestResolvePeerScoreConfig(t *testing.T) {
	customParams := DefaultPeerScoreParams()
	customThresholds := DefaultPeerScoreThresholds()

	tests := []struct {
		name           string
		config         Config
		wantParams     bool
		wantThresholds bool
		wantErr        bool
	}{
		{
			name:   "disabled by default",
			config: Config{},
		},
		{
			name:           "EnablePeerScoring installs defaults",
			config:         Config{EnablePeerScoring: true},
			wantParams:     true,
			wantThresholds: true,
		},
		{
			name:           "explicit params and thresholds pass through",
			config:         Config{PeerScoreParams: customParams, PeerScoreThresholds: customThresholds},
			wantParams:     true,
			wantThresholds: true,
		},
		{
			name:    "params without thresholds is an error",
			config:  Config{PeerScoreParams: customParams},
			wantErr: true,
		},
		{
			name:    "thresholds without params is an error",
			config:  Config{PeerScoreThresholds: customThresholds},
			wantErr: true,
		},
		{
			name:           "EnablePeerScoring fills in a missing threshold",
			config:         Config{EnablePeerScoring: true, PeerScoreParams: customParams},
			wantParams:     true,
			wantThresholds: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, thresholds, err := resolvePeerScoreConfig(tt.config)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrIncompletePeerScoreConfig)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantParams, params != nil)
			require.Equal(t, tt.wantThresholds, thresholds != nil)
		})
	}
}

func TestBuildPubSubOptions(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		wantOptions int
		wantErr     bool
		wantLog     string
	}{
		{
			name:        "default: peer exchange on, scoring off, warns",
			config:      Config{},
			wantOptions: 1,
			wantLog:     "without peer scoring",
		},
		{
			name:        "scoring enabled: peer exchange plus scoring",
			config:      Config{EnablePeerScoring: true},
			wantOptions: 2,
			wantLog:     "peer scoring enabled",
		},
		{
			name:        "scoring enabled with static peers: adds direct peers",
			config:      Config{EnablePeerScoring: true, StaticPeers: []string{testDirectPeerAddr}},
			wantOptions: 3, // peer exchange + scoring + direct peers
			wantLog:     "exempt as direct peers",
		},
		{
			name: "scoring enabled with inspect: adds inspect option",
			config: Config{
				EnablePeerScoring: true,
				PeerScoreInspect:  func(map[peer.ID]*pubsub.PeerScoreSnapshot) {},
			},
			wantOptions: 3, // peer exchange + scoring + inspect
			wantLog:     "peer scoring enabled",
		},
		{
			name:        "scoring enabled with peer exchange disabled: scoring only",
			config:      Config{EnablePeerScoring: true, DisablePeerExchange: true},
			wantOptions: 1,
			wantLog:     "peer exchange disabled",
		},
		{
			name:        "peer exchange disabled, no scoring: no options",
			config:      Config{DisablePeerExchange: true},
			wantOptions: 0,
		},
		{
			name:    "incomplete scoring config is an error",
			config:  Config{PeerScoreParams: DefaultPeerScoreParams()},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &captureLogger{}
			opts, err := buildPubSubOptions(tt.config, log)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrIncompletePeerScoreConfig)
				return
			}

			require.NoError(t, err)
			require.Len(t, opts, tt.wantOptions)
			if tt.wantLog != "" {
				require.Contains(t, log.String(), tt.wantLog)
			}
		})
	}
}

// TestDefaultPeerScoreParamsAreValid proves the built-in defaults satisfy the
// pubsub validator by constructing a real GossipSub through NewClient - pubsub
// rejects invalid PeerScoreParams/Thresholds at construction time.
func TestDefaultPeerScoreParamsAreValid(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:              testPeerName,
		PrivateKey:        privKey,
		EnablePeerScoring: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cl.Close()) })

	// Subscribe/Publish should work with scoring enabled. Sleep so the async join
	// in Subscribe completes before Publish (matches the repo's other subscribe-then
	// -publish tests and avoids a double-join on the same topic).
	ch := cl.Subscribe(testTopicName)
	require.NotNil(t, ch)
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, cl.Publish(context.Background(), testTopicName, []byte(testData)))
}

// TestNewClientRejectsIncompletePeerScoreConfig verifies the error path surfaces
// through NewClient, not only the internal helper.
func TestNewClientRejectsIncompletePeerScoreConfig(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:                testPeerName,
		PrivateKey:          privKey,
		PeerScoreThresholds: DefaultPeerScoreThresholds(), // params missing
	})
	require.Error(t, err)
	require.Nil(t, cl)
	require.ErrorIs(t, err, ErrIncompletePeerScoreConfig)
}

// TestNewClientCustomPeerScoreParams verifies a caller-supplied params/thresholds
// pair (including per-topic invalid-message penalties) is accepted.
func TestNewClientCustomPeerScoreParams(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	params := DefaultPeerScoreParams()
	params.Topics[testTopicName] = &pubsub.TopicScoreParams{
		TopicWeight:                    1,
		TimeInMeshWeight:               0.01,
		TimeInMeshQuantum:              time.Second,
		TimeInMeshCap:                  10,
		InvalidMessageDeliveriesWeight: -100,
		InvalidMessageDeliveriesDecay:  0.9,
		FirstMessageDeliveriesWeight:   0, // disabled
		MeshMessageDeliveriesWeight:    0, // disabled
		MeshFailurePenaltyWeight:       0, // disabled
	}

	cl, err := NewClient(Config{
		Name:                testPeerName,
		PrivateKey:          privKey,
		PeerScoreParams:     params,
		PeerScoreThresholds: DefaultPeerScoreThresholds(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cl.Close()) })
}

// TestNewClientDisablePeerExchange verifies the interim mitigation path builds.
func TestNewClientDisablePeerExchange(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	log := &captureLogger{}
	cl, err := NewClient(Config{
		Name:                testPeerName,
		PrivateKey:          privKey,
		DisablePeerExchange: true,
		Logger:              log,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cl.Close()) })

	require.Contains(t, log.String(), "peer exchange disabled")
}

// TestDefaultPeerScoreThresholds pins the security-critical property that
// AcceptPXThreshold is 0 (reachable), not a positive value that the penalty-only
// defaults could never meet, and that the negative thresholds are correctly ordered.
func TestDefaultPeerScoreThresholds(t *testing.T) {
	th := DefaultPeerScoreThresholds()

	// AcceptPXThreshold must be reachable: with no positive scoring, a positive value
	// would reject all peer exchange. 0 gates PX against negatively-scored peers only.
	require.Zero(t, th.AcceptPXThreshold)

	require.LessOrEqual(t, th.GossipThreshold, 0.0)
	require.LessOrEqual(t, th.PublishThreshold, th.GossipThreshold)
	require.LessOrEqual(t, th.GraylistThreshold, th.PublishThreshold)
}

// TestDirectPeers verifies static and bootstrap peers are collected (and deduplicated)
// as direct peers, while an empty config yields none.
func TestDirectPeers(t *testing.T) {
	log := &captureLogger{}

	// Same peer as both static and bootstrap should be deduplicated to one.
	dp := directPeers(Config{
		StaticPeers:    []string{testDirectPeerAddr},
		BootstrapPeers: []string{testDirectPeerAddr},
	}, log)
	require.Len(t, dp, 1)

	require.Empty(t, directPeers(Config{}, log))
}

// TestAppSpecificScoreOverride verifies Config.AppSpecificScore replaces the params'
// default zero function when scoring is enabled.
func TestAppSpecificScoreOverride(t *testing.T) {
	called := false
	fn := func(peer.ID) float64 {
		called = true
		return 5
	}

	params, _, err := resolvePeerScoreConfig(Config{EnablePeerScoring: true, AppSpecificScore: fn})
	require.NoError(t, err)
	require.NotNil(t, params.AppSpecificScore)

	require.InDelta(t, 5.0, params.AppSpecificScore(peer.ID("")), 0)
	require.True(t, called)
}
