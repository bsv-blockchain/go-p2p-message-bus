package p2p

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

// testEnvelopeMessage wraps payload in the bus's JSON envelope and returns it
// as a pubsub message authored by author, the shape a topic validator sees.
func testEnvelopeMessage(t *testing.T, topic string, author peer.ID, payload []byte) *pubsub.Message {
	t.Helper()

	raw, err := json.Marshal(envelope{Name: "peer", Data: payload})
	require.NoError(t, err)

	return testRawMessage(topic, author, raw)
}

func testRawMessage(topic string, author peer.ID, raw []byte) *pubsub.Message {
	return &pubsub.Message{Message: &pb.Message{Data: raw, Topic: &topic, From: []byte(author)}}
}

func TestTopicValidator(t *testing.T) {
	const (
		topic      = "tv-topic"
		maxPayload = 64
	)

	author := generateTestPeerID(t)

	t.Run("payload at the limit is accepted and the decoded envelope is handed on", func(t *testing.T) {
		payload := []byte(strings.Repeat("a", maxPayload))
		msg := testEnvelopeMessage(t, topic, author, payload)

		v := topicValidator(topic, nil, TopicValidator{MaxPayloadSize: maxPayload}, &captureLogger{})

		require.Equal(t, pubsub.ValidationAccept, v(context.Background(), author, msg))

		env, ok := msg.ValidatorData.(*envelope)
		require.True(t, ok, "accepted messages must carry the decoded envelope for receiveMessages")
		require.Equal(t, payload, env.Data)
		require.Equal(t, "peer", env.Name)
	})

	t.Run("payload over the limit is rejected", func(t *testing.T) {
		msg := testEnvelopeMessage(t, topic, author, []byte(strings.Repeat("a", maxPayload+1)))

		v := topicValidator(topic, nil, TopicValidator{MaxPayloadSize: maxPayload}, &captureLogger{})

		require.Equal(t, pubsub.ValidationReject, v(context.Background(), author, msg))
		require.Nil(t, msg.ValidatorData)
	})

	t.Run("oversized envelope is rejected before decoding or calling Validate", func(t *testing.T) {
		// Valid JSON whose size alone exceeds the envelope ceiling: decoding it
		// would succeed, so only a pre-decode size check can reject it.
		raw := []byte(`{"name":"` + strings.Repeat("n", maxEnvelopeSize(maxPayload)) + `","data":""}`)
		msg := testRawMessage(topic, author, raw)

		called := false
		v := topicValidator(topic, nil, TopicValidator{
			MaxPayloadSize: maxPayload,
			Validate: func(context.Context, peer.ID, []byte) pubsub.ValidationResult {
				called = true
				return pubsub.ValidationAccept
			},
		}, &captureLogger{})

		require.Equal(t, pubsub.ValidationReject, v(context.Background(), author, msg))
		require.False(t, called)
	})

	t.Run("malformed envelope is rejected", func(t *testing.T) {
		msg := testRawMessage(topic, author, []byte("not json"))

		v := topicValidator(topic, nil, TopicValidator{MaxPayloadSize: maxPayload}, &captureLogger{})

		require.Equal(t, pubsub.ValidationReject, v(context.Background(), author, msg))
	})

	t.Run("Validate sees the decoded payload and the author, and its verdict is returned", func(t *testing.T) {
		for _, verdict := range []pubsub.ValidationResult{pubsub.ValidationAccept, pubsub.ValidationReject, pubsub.ValidationIgnore} {
			payload := []byte("hello")
			msg := testEnvelopeMessage(t, topic, author, payload)

			var gotFrom peer.ID
			var gotData []byte
			v := topicValidator(topic, nil, TopicValidator{
				MaxPayloadSize: maxPayload,
				Validate: func(_ context.Context, from peer.ID, data []byte) pubsub.ValidationResult {
					gotFrom, gotData = from, data
					return verdict
				},
			}, &captureLogger{})

			// The propagation source differs from the author on purpose: Validate
			// must be told who wrote the message, not who forwarded it.
			require.Equal(t, verdict, v(context.Background(), generateTestPeerID(t), msg))
			require.Equal(t, author, gotFrom)
			require.Equal(t, payload, gotData)

			if verdict == pubsub.ValidationAccept {
				require.NotNil(t, msg.ValidatorData)
			} else {
				require.Nil(t, msg.ValidatorData, "a dropped message must not carry validator data")
			}
		}
	})

	t.Run("rejections are reported per topic", func(t *testing.T) {
		log := &captureLogger{}
		msg := testRawMessage(topic, author, []byte("not json"))

		v := topicValidator(topic, nil, TopicValidator{MaxPayloadSize: maxPayload}, log)
		v(context.Background(), author, msg)

		require.Contains(t, log.String(), "Dropped 1 message(s) on topic tv-topic that failed validation")
	})
}

func TestNewClientRejectsInvalidValidationConfig(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	t.Run("topic validator without a payload limit", func(t *testing.T) {
		cl, err := NewClient(Config{
			Name:            testPeerName,
			PrivateKey:      privKey,
			TopicValidators: map[string]TopicValidator{"t": {}},
		})
		require.ErrorIs(t, err, ErrInvalidTopicValidator)
		require.Nil(t, cl)
	})

	t.Run("negative max message size", func(t *testing.T) {
		cl, err := NewClient(Config{
			Name:           testPeerName,
			PrivateKey:     privKey,
			MaxMessageSize: -1,
		})
		require.ErrorIs(t, err, ErrInvalidMaxMessageSize)
		require.Nil(t, cl)
	})
}

// TestPublishRejectedByLocalTopicValidator pins that local publishes pass
// through the topic validator too, and that a rejection reaches the caller as
// an error rather than a silent drop.
func TestPublishRejectedByLocalTopicValidator(t *testing.T) {
	const topic = "tv-local-publish"

	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:            testPeerName,
		PrivateKey:      privKey,
		DHTMode:         "off",
		TopicValidators: map[string]TopicValidator{topic: {MaxPayloadSize: 16}},
	})
	require.NoError(t, err)

	defer func() {
		require.NoError(t, cl.Close())
	}()

	require.NoError(t, cl.Publish(context.Background(), topic, []byte(strings.Repeat("a", 16))))

	err = cl.Publish(context.Background(), topic, []byte(strings.Repeat("a", 17)))
	require.Error(t, err)

	var verr pubsub.ValidationError
	require.ErrorAs(t, err, &verr)
	require.Equal(t, pubsub.RejectValidationFailed, verr.Reason)
}

// TestTopicValidatorDefersToAllowlist pins that a message the allowlist drops
// is ignored, never rejected: pubsub's inline loop does not stop on the
// allowlist's ValidationIgnore, so a Reject from the topic validator would
// override it and penalize the peer that forwarded the message.
func TestTopicValidatorDefersToAllowlist(t *testing.T) {
	const topic = "tv-allowlist"

	allowed := generateTestPeerID(t)
	author := generateTestPeerID(t)
	allowlist := &peerAllowlist{allowed: map[peer.ID]struct{}{allowed: {}}, drops: newDropReporter("from non-allowlisted peers")}

	called := false
	log := &captureLogger{}
	v := topicValidator(topic, allowlist, TopicValidator{
		MaxPayloadSize: 4,
		Validate: func(context.Context, peer.ID, []byte) pubsub.ValidationResult {
			called = true
			return pubsub.ValidationReject
		},
	}, log)

	// Oversized, so the topic validator alone would reject it.
	msg := testEnvelopeMessage(t, topic, author, []byte("too long"))

	require.Equal(t, pubsub.ValidationIgnore, v(context.Background(), allowed, msg))
	require.False(t, called)
	require.Empty(t, log.String(), "the allowlist already reports its own drops")

	// An allowlisted author is still validated.
	msg = testEnvelopeMessage(t, topic, allowed, []byte("too long"))
	require.Equal(t, pubsub.ValidationReject, v(context.Background(), allowed, msg))
}

// TestEnvelopeOfReusesValidatorData pins that receiveMessages takes the
// envelope a topic validator decoded instead of decoding the message again.
func TestEnvelopeOfReusesValidatorData(t *testing.T) {
	msg := testRawMessage("t", generateTestPeerID(t), []byte("not json"))
	want := &envelope{Name: "peer", Data: []byte("decoded")}
	msg.ValidatorData = want

	got, err := envelopeOf(msg)
	require.NoError(t, err)
	require.Same(t, want, got)
}

// TestPublishWithLongNameFitsTopicValidator pins that a long Config.Name cannot
// push a small payload over the envelope bound: Publish sends the name in the
// sanitized form receivers keep anyway, which fits maxEnvelopeOverhead.
func TestPublishWithLongNameFitsTopicValidator(t *testing.T) {
	const topic = "tv-long-name"

	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cl, err := NewClient(Config{
		Name:            strings.Repeat("<", 1000), // JSON-escaped to 6 bytes each
		PrivateKey:      privKey,
		DHTMode:         "off",
		TopicValidators: map[string]TopicValidator{topic: {MaxPayloadSize: 16}},
	})
	require.NoError(t, err)

	defer func() {
		require.NoError(t, cl.Close())
	}()

	require.NoError(t, cl.Publish(context.Background(), topic, []byte("small")))
}
