// AnalyticsStore holds the derived state computed from the event stream:
// token/pool leaderboards, per-address net token flows ("portfolios"),
// running totals, and a recent-activity buffer. It is a pure in-memory
// reducer over events — no I/O — which keeps the aggregation logic fully
// unit-testable and independent of Redis.
//
// Aggregates cover the stream's retained window (the consumer replays it
// from the start on a cold boot). Amounts are BigInt internally and
// serialized to decimal strings at the API boundary.

import type { IndexedEvent, Transfer, Swap, Venue } from '../types.js';
import { eventBlock } from '../types.js';

export interface TokenStat {
  token: string;
  transfers: number;
  volume: bigint; // summed transfer value
}

export interface PoolStat {
  pool: string;
  swaps: number;
  venue: Venue;
}

export interface PortfolioEntry {
  token: string;
  netFlow: bigint; // received minus sent, since indexing
}

export interface Totals {
  events: number;
  transfers: number;
  swaps: number;
  lastBlock: number;
}

export interface RecentEntry {
  type: string;
  block: number;
  txHash: string;
  summary: string;
}

const RECENT_CAPACITY = 100;

export class AnalyticsStore {
  private tokens = new Map<string, TokenStat>();
  private pools = new Map<string, PoolStat>();
  // address -> token -> net flow
  private portfolios = new Map<string, Map<string, bigint>>();
  private totals: Totals = { events: 0, transfers: 0, swaps: 0, lastBlock: 0 };
  private recentBuf: RecentEntry[] = [];

  apply(e: IndexedEvent): void {
    this.totals.events++;
    const block = eventBlock(e);
    if (block > this.totals.lastBlock) this.totals.lastBlock = block;

    if (e.type === 'erc20_transfer' && e.transfer) {
      this.applyTransfer(e.transfer);
    } else if (e.type === 'swap' && e.swap) {
      this.applySwap(e.swap);
    }
  }

  private applyTransfer(t: Transfer): void {
    this.totals.transfers++;
    const value = safeBig(t.value);

    const token = this.tokens.get(t.tokenAddress) ?? { token: t.tokenAddress, transfers: 0, volume: 0n };
    token.transfers++;
    token.volume += value;
    this.tokens.set(t.tokenAddress, token);

    // Net flow: sender loses value, recipient gains it.
    this.addFlow(t.from, t.tokenAddress, -value);
    this.addFlow(t.to, t.tokenAddress, value);

    this.pushRecent({
      type: 'transfer',
      block: t.blockNumber,
      txHash: t.txHash,
      summary: `${short(t.from)} → ${short(t.to)} ${t.value} of ${short(t.tokenAddress)}`,
    });
  }

  private applySwap(s: Swap): void {
    this.totals.swaps++;
    const pool = this.pools.get(s.poolAddress) ?? { pool: s.poolAddress, swaps: 0, venue: s.venue };
    pool.swaps++;
    pool.venue = s.venue;
    this.pools.set(s.poolAddress, pool);

    this.pushRecent({
      type: 'swap',
      block: s.blockNumber,
      txHash: s.txHash,
      summary: `${s.venue} swap on ${short(s.poolAddress)} by ${short(s.sender)}`,
    });
  }

  private addFlow(address: string, token: string, delta: bigint): void {
    // The zero address is the mint/burn sink; skip it so it doesn't
    // dominate every portfolio leaderboard.
    if (isZeroAddress(address)) return;
    let byToken = this.portfolios.get(address);
    if (!byToken) {
      byToken = new Map<string, bigint>();
      this.portfolios.set(address, byToken);
    }
    byToken.set(token, (byToken.get(token) ?? 0n) + delta);
  }

  private pushRecent(entry: RecentEntry): void {
    this.recentBuf.push(entry);
    if (this.recentBuf.length > RECENT_CAPACITY) this.recentBuf.shift();
  }

  // --- queries ---

  stats(): Totals {
    return { ...this.totals };
  }

  topTokens(limit: number, by: 'volume' | 'transfers' = 'volume'): TokenStat[] {
    const all = [...this.tokens.values()];
    all.sort((a, b) => {
      if (by === 'transfers') return b.transfers - a.transfers;
      return a.volume < b.volume ? 1 : a.volume > b.volume ? -1 : 0;
    });
    return all.slice(0, clampLimit(limit));
  }

  topPools(limit: number): PoolStat[] {
    const all = [...this.pools.values()];
    all.sort((a, b) => b.swaps - a.swaps);
    return all.slice(0, clampLimit(limit));
  }

  portfolio(address: string): PortfolioEntry[] {
    const byToken = this.portfolios.get(address);
    if (!byToken) return [];
    return [...byToken.entries()]
      .map(([token, netFlow]) => ({ token, netFlow }))
      .sort((a, b) => (a.netFlow < b.netFlow ? 1 : a.netFlow > b.netFlow ? -1 : 0));
  }

  recent(limit: number): RecentEntry[] {
    return this.recentBuf.slice(-clampLimit(limit)).reverse();
  }
}

function safeBig(s: string): bigint {
  try {
    return BigInt(s);
  } catch {
    return 0n;
  }
}

function clampLimit(limit: number): number {
  if (!Number.isFinite(limit) || limit <= 0) return 25;
  return Math.min(limit, 500);
}

function isZeroAddress(a: string): boolean {
  return /^0x0{40}$/i.test(a);
}

function short(addr: string): string {
  return addr.length > 12 ? `${addr.slice(0, 6)}…${addr.slice(-4)}` : addr;
}
