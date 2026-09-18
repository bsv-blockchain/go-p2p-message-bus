package p2p

import (
	"context"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// messageFromAuthor builds the only part of an inbound pubsub message the
// allowlist validator reads: the authenticated author. pubsub.Message embeds
// *pb.Message and (*pubsub.Message).GetFrom reads that struct's From field,
// which carries the author's peer ID in its wire form - so the validator's
// verdict can be exercised without a host, a network or a running pubsub
// instance.
func messageFromAuthor(author peer.ID) *pubsub.Message {
	return &pubsub.Message{Message: &pb.Message{From: []byte(author)}}
}

// newEnabledTestAllowlist returns an enabled allowlist containing exactly the
// given publishers plus a throwaway self ID (newPeerAllowlist always adds the
// local node, since locally published messages run through the same
// validators).
func newEnabledTestAllowlist(t *testing.T, allowed ...peer.ID) *peerAllowlist {
	t.Helper()

	ids := make([]string, 0, len(allowed))
	for _, id := range allowed {
		ids = append(ids, id.String())
	}

	allowlist, err := newPeerAllowlist(
		Config{AllowedPublisherIDs: ids},
		newTestPeerID(t),
		&captureLogger{},
	)
	require.NoError(t, err)
	require.True(t, allowlist.enabled())

	return allowlist
}

// TestAllowlistValidatorDecidesOnAuthorNotPropagationSource pins the property
// the whole feature rests on: the verdict comes from the message's
// authenticated author, and the propagation source - the peer that handed us
// the message, which is the validator's second parameter - has no influence on
// it whatsoever.
//
// Both mixed cases are required, and neither alone is sufficient:
//
//   - "allowed author forwarded by a stranger" alone would still pass if the
//     check became allows(src) || allows(author), i.e. if an allowlisted
//     forwarder could launder a blocked peer's messages - the actual bypass.
//   - "stranger author forwarded by an allowed peer" alone would still pass if
//     the check became allows(src) && allows(author), which silently blackholes
//     an allowlisted author whose message arrives via any other peer - normal
//     GossipSub relaying.
//
// Together they pin src as ignored in both directions, so any mutation that
// consults it at all fails here.
func TestAllowlistValidatorDecidesOnAuthorNotPropagationSource(t *testing.T) {
	allowed := newTestPeerID(t)
	stranger := newTestPeerID(t)

	validate := allowlistValidator(newEnabledTestAllowlist(t, allowed), &captureLogger{})

	tests := []struct {
		name   string
		src    peer.ID
		author peer.ID
		want   pubsub.ValidationResult
	}{
		{
			name:   "allowed author delivered by the author",
			src:    allowed,
			author: allowed,
			want:   pubsub.ValidationAccept,
		},
		{
			name:   "allowed author forwarded by a stranger",
			src:    stranger,
			author: allowed,
			want:   pubsub.ValidationAccept,
		},
		{
			name:   "stranger author forwarded by an allowed peer",
			src:    allowed,
			author: stranger,
			want:   pubsub.ValidationIgnore,
		},
		{
			name:   "stranger author delivered by the author",
			src:    stranger,
			author: stranger,
			want:   pubsub.ValidationIgnore,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validate(context.Background(), tt.src, messageFromAuthor(tt.author))

			assert.Equal(t, tt.want, got,
				"the verdict must follow the message author, never the peer that delivered it")
		})
	}
}

// TestAllowlistValidatorIgnoresStrangersAndNeverRejectsThem pins the second
// load-bearing property: a message from outside the allowlist is dropped with
// ValidationIgnore, never ValidationReject.
//
// The distinction is not cosmetic. ValidationReject feeds the sender's
// InvalidMessageDeliveries penalty, so any peer running peer scoring would
// graylist an honest peer this node merely does not listen to - the opposite
// of what Config.AllowedPublisherIDs and the README promise. Reject is therefore ruled
// out by name and separately from Accept: an assertion that only said "not
// Accept" would keep passing if the verdict were switched to Reject.
func TestAllowlistValidatorIgnoresStrangersAndNeverRejectsThem(t *testing.T) {
	stranger := newTestPeerID(t)

	validate := allowlistValidator(newEnabledTestAllowlist(t, newTestPeerID(t)), &captureLogger{})

	got := validate(context.Background(), stranger, messageFromAuthor(stranger))

	assert.Equal(t, pubsub.ValidationIgnore, got, "a non-allowlisted author's message must be ignored")
	assert.NotEqual(t, pubsub.ValidationReject, got,
		"rejecting would penalize the sender's peer score: a peer we simply do not listen to has not misbehaved")
	assert.NotEqual(t, pubsub.ValidationAccept, got, "a non-allowlisted author's message must not be delivered")
}

// TestAllowlistValidatorCountsOneDropPerFilteredMessage pins the drop
// accounting on the rejection path: exactly one count per dropped message, and
// none for an accepted one.
func TestAllowlistValidatorCountsOneDropPerFilteredMessage(t *testing.T) {
	allowed := newTestPeerID(t)
	stranger := newTestPeerID(t)
	allowlist := newEnabledTestAllowlist(t, allowed)

	// Anchor start in the past and claim the current logging window, so drops
	// accumulate in the counter instead of being logged and reset by the first
	// one (see TestDropReporterSuppressesWithinInterval, which sets up the same
	// suppressed window). The counter is what this test reads; the logging
	// itself is covered by TestAllowlistValidatorReportsDropsThroughTheLogger.
	allowlist.drops.start = time.Now().Add(-time.Minute)
	allowlist.drops.lastLog.Store(int64(time.Minute))

	validate := allowlistValidator(allowlist, &captureLogger{})
	ctx := context.Background()

	require.Equal(t, pubsub.ValidationAccept, validate(ctx, allowed, messageFromAuthor(allowed)))
	assert.Equal(t, uint64(0), allowlist.drops.dropped.Load(), "an accepted message is not a drop")

	require.Equal(t, pubsub.ValidationIgnore, validate(ctx, allowed, messageFromAuthor(stranger)))
	assert.Equal(t, uint64(1), allowlist.drops.dropped.Load(), "a filtered message counts exactly once")

	// A second accepted message, this time forwarded by the stranger, must not
	// advance the counter either: the count tracks authors we drop, not peers we
	// dislike.
	require.Equal(t, pubsub.ValidationAccept, validate(ctx, stranger, messageFromAuthor(allowed)))
	assert.Equal(t, uint64(1), allowlist.drops.dropped.Load(), "the counter must not advance on an accepted message")

	require.Equal(t, pubsub.ValidationIgnore, validate(ctx, stranger, messageFromAuthor(stranger)))
	assert.Equal(t, uint64(2), allowlist.drops.dropped.Load())
}

// TestAllowlistValidatorReportsDropsThroughTheLogger pins that the validator
// wires its drop path to the logger it was built with - the only operational
// signal that this node is filtering traffic - and that an accepted message
// produces no such line.
func TestAllowlistValidatorReportsDropsThroughTheLogger(t *testing.T) {
	allowed := newTestPeerID(t)
	log := &captureLogger{}

	validate := allowlistValidator(newEnabledTestAllowlist(t, allowed), log)
	ctx := context.Background()

	require.Equal(t, pubsub.ValidationAccept, validate(ctx, allowed, messageFromAuthor(allowed)))
	assert.Empty(t, log.String(), "an accepted message must not report a drop")

	require.Equal(t, pubsub.ValidationIgnore, validate(ctx, allowed, messageFromAuthor(newTestPeerID(t))))
	assert.Contains(t, log.String(), "Dropped 1 message(s) from non-allowlisted peers since the last report")
}
