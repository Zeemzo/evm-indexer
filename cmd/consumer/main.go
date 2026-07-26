// Command consumer is an example downstream service that reads the
// indexer's Redis Stream via a consumer group and maintains live
// aggregates (per-type counts, most recent block). It demonstrates the
// event-driven decoupling: the indexer knows nothing about consumers,
// and any number of them can scale independently off the same stream.
//
// Consumer groups give at-least-once delivery with per-message acks, so
// a crashed consumer's un-acked messages are redelivered to a peer.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/zeemzo/evm-indexer/internal/stream"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	redisURL := getenv("REDIS_URL", "redis://localhost:6379/0")
	streamName := getenv("STREAM_NAME", "evm.events")
	group := getenv("CONSUMER_GROUP", "analytics")
	consumerName := getenv("CONSUMER_NAME", "analytics-1")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}

	// Create the group at the stream's start ("0"); MKSTREAM creates the
	// stream if the indexer hasn't produced yet. BUSYGROUP = already made.
	if err := rdb.XGroupCreateMkStream(ctx, streamName, group, "0").Err(); err != nil &&
		err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return err
	}
	log.Info("consumer started", "stream", streamName, "group", group, "consumer", consumerName)

	agg := &aggregates{byType: map[string]uint64{}}

	// Periodically report the running aggregates.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				agg.report(log)
			}
		}
	}()

	err = consume(ctx, rdb, streamName, group, consumerName, log, agg)
	wg.Wait()
	agg.report(log)
	return err
}

func consume(ctx context.Context, rdb *redis.Client, streamName, group, consumer string, log *slog.Logger, agg *aggregates) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		// Block up to 5s waiting for new messages ('>' = never-delivered).
		res, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: consumer,
			Streams:  []string{streamName, ">"},
			Count:    100,
			Block:    5 * time.Second,
		}).Result()
		if err == redis.Nil || len(res) == 0 {
			continue // idle timeout, poll again
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Warn("xreadgroup failed", "error", err)
			time.Sleep(time.Second)
			continue
		}

		for _, s := range res {
			ackIDs := make([]string, 0, len(s.Messages))
			for _, msg := range s.Messages {
				agg.observe(msg)
				ackIDs = append(ackIDs, msg.ID)
			}
			// Ack only after processing, so a crash mid-batch redelivers.
			if len(ackIDs) > 0 {
				if err := rdb.XAck(ctx, streamName, group, ackIDs...).Err(); err != nil {
					log.Warn("xack failed", "error", err)
				}
			}
		}
	}
}

// aggregates holds the running totals this consumer derives from the
// stream, protected for the concurrent reporter goroutine.
type aggregates struct {
	mu        sync.Mutex
	total     uint64
	byType    map[string]uint64
	lastBlock uint64
}

func (a *aggregates) observe(msg redis.XMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.total++
	if t, ok := msg.Values[stream.FieldType].(string); ok {
		a.byType[t]++
	}
	if b, ok := msg.Values[stream.FieldBlock].(string); ok {
		if n, err := strconv.ParseUint(b, 10, 64); err == nil && n > a.lastBlock {
			a.lastBlock = n
		}
	}
}

func (a *aggregates) report(log *slog.Logger) {
	a.mu.Lock()
	defer a.mu.Unlock()
	log.Info("aggregates",
		"totalEvents", a.total,
		"transfers", a.byType["erc20_transfer"],
		"swaps", a.byType["swap"],
		"lastBlock", a.lastBlock)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
