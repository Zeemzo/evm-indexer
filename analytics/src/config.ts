import 'dotenv/config';

export interface Config {
  redisUrl: string;
  streamName: string;
  consumerGroup: string;
  consumerName: string;
  port: number;
  logLevel: string;
}

export function loadConfig(): Config {
  return {
    redisUrl: env('REDIS_URL', 'redis://localhost:6379/0'),
    streamName: env('STREAM_NAME', 'evm.events'),
    consumerGroup: env('CONSUMER_GROUP', 'analytics'),
    // Unique per replica so a consumer group can load-balance across them.
    consumerName: env('CONSUMER_NAME', `analytics-${process.pid}`),
    port: Number(env('PORT', '8090')),
    logLevel: env('LOG_LEVEL', 'info'),
  };
}

function env(key: string, fallback: string): string {
  const v = process.env[key];
  return v && v.length > 0 ? v : fallback;
}
