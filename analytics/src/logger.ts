import { pino } from 'pino';

export function createLogger(level: string) {
  return pino({
    level,
    transport: process.stdout.isTTY
      ? { target: 'pino-pretty', options: { colorize: true } }
      : undefined,
  });
}

export type Logger = ReturnType<typeof createLogger>;
