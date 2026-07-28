// Entry point: wires Redis → stream consumer → analytics store → HTTP/WS
// API, and coordinates graceful shutdown. The Go indexer is the ingestion
// engine; this TypeScript service is a downstream, independently
// scalable analytics consumer of its event stream.

import { Redis } from 'ioredis';
import { loadConfig } from './config.js';
import { createLogger } from './logger.js';
import { AnalyticsStore } from './analytics/store.js';
import { StreamConsumer } from './consumer.js';
import { createServer } from './api/server.js';

async function main(): Promise<void> {
  const cfg = loadConfig();
  const log = createLogger(cfg.logLevel);
  log.info({ stream: cfg.streamName, group: cfg.consumerGroup }, 'starting evm-analytics');

  const redis = new Redis(cfg.redisUrl, { maxRetriesPerRequest: null });
  redis.on('error', (err) => log.warn({ err }, 'redis error'));

  const store = new AnalyticsStore();
  let ready = false;

  const server = createServer(store, log, () => ready);
  const consumer = new StreamConsumer(
    redis,
    store,
    { stream: cfg.streamName, group: cfg.consumerGroup, consumer: cfg.consumerName },
    log,
    (event) => server.broadcast(event),
  );

  // Serve first so /health answers (503, not-ready) even while Redis is
  // still unreachable — the readiness probe can then gate traffic
  // correctly instead of the pod appearing dead.
  await server.listen(cfg.port);
  await consumer.ensureGroup();
  ready = true;

  // Run the consumer loop until shutdown; if it throws, exit non-zero so
  // the orchestrator restarts us.
  const consuming = consumer.start().catch((err) => {
    log.error({ err }, 'consumer loop failed');
    process.exitCode = 1;
  });

  const shutdown = async (signal: string) => {
    log.info({ signal }, 'shutting down');
    ready = false;
    consumer.stop();
    await consuming;
    await server.close();
    redis.disconnect();
    log.info('shutdown complete');
    process.exit(0);
  };
  process.on('SIGTERM', () => void shutdown('SIGTERM'));
  process.on('SIGINT', () => void shutdown('SIGINT'));
}

main().catch((err) => {
  console.error('fatal:', err);
  process.exit(1);
});
