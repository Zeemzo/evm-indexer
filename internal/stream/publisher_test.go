package stream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zeemzo/evm-indexer/internal/events"
)

// Noop must satisfy Publisher and never error, so the indexer runs
// unchanged when no broker is configured.
func TestNoopPublisherIsInert(t *testing.T) {
	var p Publisher = Noop{}
	err := p.Publish(context.Background(), events.Event{
		Type:     events.TypeTransfer,
		Transfer: &events.Transfer{BlockNumber: 1},
	})
	require.NoError(t, err)
	require.NoError(t, p.Close())
}

// A malformed REDIS_URL must be reported at construction, not deferred.
func TestNewRedisPublisherRejectsBadURL(t *testing.T) {
	_, err := NewRedisPublisher(context.Background(), "not-a-url", "evm.events", 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse REDIS_URL")
}
