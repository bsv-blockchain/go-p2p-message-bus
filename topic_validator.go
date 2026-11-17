package p2p

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
)

var (
	// ErrInvalidTopicValidator is returned when a Config.TopicValidators entry
	// has no positive MaxPayloadSize.
	ErrInvalidTopicValidator = errors.New("topic validator MaxPayloadSize must be positive")
	// ErrInvalidMaxMessageSize is returned when Config.MaxMessageSize is negative.
	ErrInvalidMaxMessageSize = errors.New("MaxMessageSize must not be negative")
)

// maxEnvelopeOverhead is the room an envelope may take beyond its base64
// payload: the JSON keys and quotes, plus a sender name that JSON escaping can
// expand up to 6x. Publish sends the name sanitized to maxPeerNameLen bytes,
// so 6*maxPeerNameLen plus the keys fits.
const maxEnvelopeOverhead = 512

// TopicValidator checks messages on one topic before GossipSub delivers or
// forwards them. See Config.TopicValidators.
type TopicValidator struct {
	// MaxPayloadSize is the largest payload, in bytes, accepted on the topic.
	// It must be positive. The envelope is size-checked against it before it
	// is decoded, so an oversized message costs no decoding work.
	MaxPayloadSize int

	// Validate optionally runs a further check on the decoded payload. from is
	// the message's authenticated author, not the peer that forwarded it.
	// Return ValidationReject for an invalid message, ValidationIgnore to drop
	// it without penalty. Messages from authors outside AllowedPublisherIDs are
	// ignored before Validate is called.
	//
	// It runs inline on pubsub's validation workers, for every inbound message
	// on the topic and for this node's own publishes, so it must be cheap and
	// must not block: a slow Validate stalls validation for every topic.
	Validate func(ctx context.Context, from peer.ID, data []byte) pubsub.ValidationResult
}

// envelope is the JSON wrapper Publish puts around every payload.
type envelope struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// envelopeOf returns msg's decoded envelope, reusing the one a topic validator
// already decoded when there is one.
func envelopeOf(msg *pubsub.Message) (*envelope, error) {
	if env, ok := msg.ValidatorData.(*envelope); ok {
		return env, nil
	}

	env := &envelope{}
	if err := json.Unmarshal(msg.Data, env); err != nil {
		return nil, err
	}

	return env, nil
}

// maxEnvelopeSize is the largest encoded envelope that can carry a payload of
// maxPayload bytes.
func maxEnvelopeSize(maxPayload int) int {
	return base64.StdEncoding.EncodedLen(maxPayload) + maxEnvelopeOverhead
}

// validateValidationConfig checks MaxMessageSize and TopicValidators.
func validateValidationConfig(config Config) error {
	if config.MaxMessageSize < 0 {
		return ErrInvalidMaxMessageSize
	}

	for topic, tv := range config.TopicValidators {
		if tv.MaxPayloadSize <= 0 {
			return fmt.Errorf("%w: topic %q", ErrInvalidTopicValidator, topic)
		}
	}

	return nil
}

// registerTopicValidators registers a validator for every configured topic.
// Registration is keyed by topic name and independent of joining the topic, so
// doing it once here covers both Subscribe and Publish.
//
// Validators run inline: the work is bounded by the pre-decode size check,
// and inline matches the allowlist validator. A rejected message is dropped
// before it is delivered or forwarded.
func registerTopicValidators(ps *pubsub.PubSub, config Config, allowlist *peerAllowlist, log logger) error {
	for topic, tv := range config.TopicValidators {
		if err := ps.RegisterTopicValidator(topic, topicValidator(topic, allowlist, tv, log), pubsub.WithValidatorInline(true)); err != nil {
			return fmt.Errorf("failed to register validator for topic %s: %w", topic, err)
		}
	}

	return nil
}

// topicValidator builds the pubsub validator for one topic. An accepted
// message carries its decoded envelope in ValidatorData, so receiveMessages
// does not decode it again.
func topicValidator(topic string, allowlist *peerAllowlist, tv TopicValidator, log logger) pubsub.ValidatorEx {
	limit := maxEnvelopeSize(tv.MaxPayloadSize)
	drops := newDropReporter(fmt.Sprintf("on topic %s that failed validation", topic))

	return func(ctx context.Context, _ peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
		// pubsub keeps running inline validators after the allowlist's Ignore and
		// lets a later Reject win, which would penalize the forwarding peer for a
		// message the allowlist promises to drop without blame. Ignore it here too;
		// the allowlist validator has already counted the drop.
		if !allowlist.allows(msg.GetFrom()) {
			return pubsub.ValidationIgnore
		}

		// The raw length check short-circuits, so an oversized envelope is never decoded.
		env := &envelope{}
		if len(msg.Data) > limit || json.Unmarshal(msg.Data, env) != nil || len(env.Data) > tv.MaxPayloadSize {
			drops.recordDrop(log)
			return pubsub.ValidationReject
		}

		if tv.Validate != nil {
			if result := tv.Validate(ctx, msg.GetFrom(), env.Data); result != pubsub.ValidationAccept {
				drops.recordDrop(log)
				return result
			}
		}

		msg.ValidatorData = env

		return pubsub.ValidationAccept
	}
}
