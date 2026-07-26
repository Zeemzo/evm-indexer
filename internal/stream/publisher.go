// Package stream publishes decoded events to a durable Redis Stream so
// independent services (analytics, notifiers, a WebSocket gateway) can
// consume them without coupling to the indexer. Postgres remains the
// source of truth; the stream is a best-effort, at-least-once event bus
// layered on top, letting the platform grow event-driven and horizontally.
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"

	"github.com/zeemzo/evm-indexer/internal/events"
)

// Publisher appends indexed events to the bus. Publish must be safe to
// call from the indexing hot path; implementations should not block on
// slow consumers.
type Publisher interface {
	Publish(ctx context.Context, e events.Event) error
	Close() error
}

// Noop is used when no broker is configured. The indexer runs unchanged,
// serving only its in-process WebSocket fan-out.
type Noop struct{}

func (Noop) Publish(context.Context, events.Event) error { return nil }
func (Noop) Close() error                                { return nil }

// Fields are the Redis Stream entry fields for one event.
const (
	FieldType  = "type"
	FieldBlock = "block"
	FieldData  = "data"
)

// RedisPublisher writes events to a capped Redis Stream via XADD.
type RedisPublisher struct {
	rdb    *redis.Client
	stream string
	maxLen int64
}

// NewRedisPublisher dials Redis and verifies connectivity. maxLen caps
// the stream length (approximate trimming) so an unattended stream can't
// grow without bound.
func NewRedisPublisher(ctx context.Context, url, stream string, maxLen int64) (*RedisPublisher, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	if maxLen <= 0 {
		maxLen = 1_000_000
	}
	return &RedisPublisher{rdb: rdb, stream: stream, maxLen: maxLen}, nil
}

func (p *RedisPublisher) Publish(ctx context.Context, e events.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return p.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: p.stream,
		MaxLen: p.maxLen,
		Approx: true, // MAXLEN ~ N: cheap amortized trimming
		Values: map[string]any{
			FieldType:  string(e.Type),
			FieldBlock: strconv.FormatUint(e.BlockNumber(), 10),
			FieldData:  payload,
		},
	}).Err()
}

func (p *RedisPublisher) Close() error {
	return p.rdb.Close()
}
