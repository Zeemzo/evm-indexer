// StreamConsumer reads the indexer's Redis Stream via a consumer group
// and feeds each event into the AnalyticsStore. Consumer groups give
// at-least-once delivery: messages are acked only after they are
// applied, so a crash redelivers un-acked work to a surviving replica.
//
// On a cold start the group is created at offset "0", so this consumer
// replays the stream's full retained window to rebuild aggregates before
// tailing live.

import type { Redis } from 'ioredis';
import type { AnalyticsStore } from './analytics/store.js';
import type { IndexedEvent } from './types.js';
import type { Logger } from './logger.js';

// Field names must match the Go publisher (internal/stream/publisher.go).
const FIELD_DATA = 'data';

export interface ConsumerOptions {
  stream: string;
  group: string;
  consumer: string;
}

export class StreamConsumer {
  private running = false;

  constructor(
    private readonly redis: Redis,
    private readonly store: AnalyticsStore,
    private readonly opts: ConsumerOptions,
    private readonly log: Logger,
    // onEvent fires after each event is applied to the store, so the
    // WebSocket layer can fan it out live. Optional; nil in tests.
    private readonly onEvent?: (e: IndexedEvent) => void,
  ) {}

  /** ensureGroup creates the consumer group, tolerating "already exists". */
  async ensureGroup(): Promise<void> {
    try {
      await this.redis.xgroup('CREATE', this.opts.stream, this.opts.group, '0', 'MKSTREAM');
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      if (!msg.includes('BUSYGROUP')) throw err;
    }
  }

  async start(): Promise<void> {
    this.running = true;
    this.log.info({ ...this.opts }, 'consumer started');

    while (this.running) {
      // Block up to 5s for new messages ('>' = never-delivered to this group).
      const res = await this.redis.xreadgroup(
        'GROUP', this.opts.group, this.opts.consumer,
        'COUNT', 200,
        'BLOCK', 5000,
        'STREAMS', this.opts.stream, '>',
      );
      if (!res) continue; // idle timeout

      for (const [, messages] of res as [string, [string, string[]][]][]) {
        const ackIds: string[] = [];
        for (const [id, fields] of messages) {
          this.applyMessage(fields);
          ackIds.push(id);
        }
        if (ackIds.length > 0) {
          await this.redis.xack(this.opts.stream, this.opts.group, ...ackIds);
        }
      }
    }
  }

  stop(): void {
    this.running = false;
  }

  /** applyMessage parses the flat [k,v,k,v,...] field array and reduces it. */
  private applyMessage(fields: string[]): void {
    const raw = fieldValue(fields, FIELD_DATA);
    if (!raw) return;
    try {
      const event = JSON.parse(raw) as IndexedEvent;
      this.store.apply(event);
      this.onEvent?.(event);
    } catch (err) {
      this.log.warn({ err }, 'failed to parse stream event');
    }
  }
}

/** fieldValue pulls a value out of Redis's flat field array. */
function fieldValue(fields: string[], key: string): string | undefined {
  for (let i = 0; i + 1 < fields.length; i += 2) {
    if (fields[i] === key) return fields[i + 1];
  }
  return undefined;
}
