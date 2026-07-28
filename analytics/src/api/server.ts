// HTTP + WebSocket API over the derived analytics. BigInt values are
// rendered as decimal strings (JSON has no bigint), matching the Go
// indexer's own amount encoding.

import http from 'node:http';
import express, { type Request, type Response } from 'express';
import { WebSocketServer, WebSocket } from 'ws';
import type { AnalyticsStore } from '../analytics/store.js';
import type { IndexedEvent } from '../types.js';
import type { Logger } from '../logger.js';

export interface Server {
  listen(port: number): Promise<void>;
  close(): Promise<void>;
  /** broadcast pushes a live event to every connected WebSocket client. */
  broadcast(event: IndexedEvent): void;
}

export function createServer(store: AnalyticsStore, log: Logger, isReady: () => boolean): Server {
  const app = express();

  app.get('/health', (_req: Request, res: Response) => {
    const ready = isReady();
    res.status(ready ? 200 : 503).json({ healthy: ready });
  });

  app.get('/stats', (_req, res) => {
    res.json(store.stats());
  });

  // Token leaderboard. ?by=volume|transfers&limit=N
  app.get('/analytics/tokens', (req, res) => {
    const by = req.query.by === 'transfers' ? 'transfers' : 'volume';
    res.json({ tokens: store.topTokens(intParam(req.query.limit), by).map(serializeToken) });
  });

  // Pool leaderboard by swap count.
  app.get('/analytics/pools', (req, res) => {
    res.json({ pools: store.topPools(intParam(req.query.limit)) });
  });

  // Per-address net token flow ("portfolio") since indexing began.
  app.get('/portfolio/:address', (req, res) => {
    const address = req.params.address;
    res.json({
      address,
      balances: store.portfolio(address).map((e) => ({ token: e.token, netFlow: e.netFlow.toString() })),
    });
  });

  app.get('/activity/recent', (req, res) => {
    res.json({ events: store.recent(intParam(req.query.limit)) });
  });

  const httpServer = http.createServer(app);

  // WebSocket live feed of newly applied events.
  const wss = new WebSocketServer({ server: httpServer, path: '/ws' });
  wss.on('connection', (ws: WebSocket) => {
    log.info('websocket client connected');
    ws.on('error', (err) => log.warn({ err }, 'websocket error'));
  });

  function broadcast(event: IndexedEvent): void {
    if (wss.clients.size === 0) return;
    const payload = JSON.stringify(event);
    for (const client of wss.clients) {
      if (client.readyState === WebSocket.OPEN) client.send(payload);
    }
  }

  return {
    broadcast,
    listen(port: number) {
      return new Promise<void>((resolve) => {
        httpServer.listen(port, () => {
          log.info({ port }, 'analytics api listening');
          resolve();
        });
      });
    },
    close() {
      return new Promise<void>((resolve) => {
        for (const client of wss.clients) client.terminate();
        wss.close(() => httpServer.close(() => resolve()));
      });
    },
  };
}

function serializeToken(t: { token: string; transfers: number; volume: bigint }) {
  return { token: t.token, transfers: t.transfers, volume: t.volume.toString() };
}

function intParam(v: unknown): number {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}
