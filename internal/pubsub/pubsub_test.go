package pubsub

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscribeAndPublish(t *testing.T) {
	b := NewBroker[int](4)
	defer b.Close()

	ch, cancel := b.Subscribe()
	defer cancel()

	b.Publish(1)
	b.Publish(2)
	assert.Equal(t, 1, <-ch)
	assert.Equal(t, 2, <-ch)
}

func TestMultipleSubscribersEachReceive(t *testing.T) {
	b := NewBroker[string](4)
	defer b.Close()

	ch1, cancel1 := b.Subscribe()
	ch2, cancel2 := b.Subscribe()
	defer cancel1()
	defer cancel2()

	b.Publish("event")
	assert.Equal(t, "event", <-ch1)
	assert.Equal(t, "event", <-ch2)
	assert.Equal(t, 2, b.SubscriberCount())
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	b := NewBroker[int](4)
	defer b.Close()

	ch, cancel := b.Subscribe()
	cancel()

	_, open := <-ch
	assert.False(t, open, "channel must be closed after cancel")
	assert.Equal(t, 0, b.SubscriberCount())

	// Publishing after unsubscribe must not panic.
	b.Publish(1)

	// Double-cancel must be safe.
	cancel()
}

func TestSlowConsumerDropsInsteadOfBlocking(t *testing.T) {
	b := NewBroker[int](2)
	defer b.Close()

	_, cancel := b.Subscribe()
	defer cancel()

	// Fill the buffer, then overflow: publishes must not block.
	for i := 0; i < 5; i++ {
		b.Publish(i)
	}
	assert.Equal(t, uint64(3), b.Dropped())
}

func TestCloseShutsDownSubscribers(t *testing.T) {
	b := NewBroker[int](4)
	ch, _ := b.Subscribe()

	b.Close()
	_, open := <-ch
	require.False(t, open)

	// Subscribe after close returns a closed channel.
	ch2, cancel2 := b.Subscribe()
	_, open = <-ch2
	assert.False(t, open)
	cancel2()

	// Idempotent close.
	b.Close()
}
