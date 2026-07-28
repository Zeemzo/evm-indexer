import { describe, it, expect } from 'vitest';
import { AnalyticsStore } from '../src/analytics/store.js';
import type { IndexedEvent } from '../src/types.js';

function transfer(over: Partial<IndexedEvent['transfer']> = {}): IndexedEvent {
  return {
    type: 'erc20_transfer',
    transfer: {
      blockNumber: 100,
      blockTime: '2026-01-01T00:00:00Z',
      txHash: '0xabc',
      logIndex: 0,
      tokenAddress: '0xToken',
      from: '0xAAA0000000000000000000000000000000000001',
      to: '0xBBB0000000000000000000000000000000000002',
      value: '1000',
      ...over,
    },
  };
}

function swap(over: Partial<IndexedEvent['swap']> = {}): IndexedEvent {
  return {
    type: 'swap',
    swap: {
      blockNumber: 100,
      blockTime: '2026-01-01T00:00:00Z',
      txHash: '0xdef',
      logIndex: 1,
      poolAddress: '0xPool',
      sender: '0xSender',
      recipient: '0xRecipient',
      amount0In: '0',
      amount1In: '500',
      amount0Out: '495',
      amount1Out: '0',
      venue: 'v3',
      ...over,
    },
  };
}

describe('AnalyticsStore', () => {
  it('accumulates totals across event types', () => {
    const s = new AnalyticsStore();
    s.apply(transfer());
    s.apply(transfer({ blockNumber: 105 }));
    s.apply(swap({ blockNumber: 110 }));

    const t = s.stats();
    expect(t.events).toBe(3);
    expect(t.transfers).toBe(2);
    expect(t.swaps).toBe(1);
    expect(t.lastBlock).toBe(110);
  });

  it('sums token volume as BigInt without precision loss', () => {
    const s = new AnalyticsStore();
    // Values beyond Number.MAX_SAFE_INTEGER must not lose precision.
    const big = '90000000000000000000'; // 90e18
    s.apply(transfer({ value: big }));
    s.apply(transfer({ value: big }));

    const [top] = s.topTokens(10);
    expect(top?.token).toBe('0xToken');
    expect(top?.transfers).toBe(2);
    expect(top?.volume).toBe(180000000000000000000n);
  });

  it('ranks tokens by volume and by transfer count independently', () => {
    const s = new AnalyticsStore();
    // Token A: two small transfers. Token B: one large transfer.
    s.apply(transfer({ tokenAddress: '0xA', value: '10' }));
    s.apply(transfer({ tokenAddress: '0xA', value: '10' }));
    s.apply(transfer({ tokenAddress: '0xB', value: '1000' }));

    expect(s.topTokens(1, 'volume')[0]?.token).toBe('0xB');
    expect(s.topTokens(1, 'transfers')[0]?.token).toBe('0xA');
  });

  it('computes per-address net flow (received minus sent)', () => {
    const s = new AnalyticsStore();
    const alice = '0xA1ce000000000000000000000000000000000001';
    const bob = '0xB0b0000000000000000000000000000000000002';
    // Alice sends 300 to Bob, Bob sends 100 back.
    s.apply(transfer({ from: alice, to: bob, value: '300', tokenAddress: '0xTok' }));
    s.apply(transfer({ from: bob, to: alice, value: '100', tokenAddress: '0xTok' }));

    expect(s.portfolio(alice)).toEqual([{ token: '0xTok', netFlow: -200n }]);
    expect(s.portfolio(bob)).toEqual([{ token: '0xTok', netFlow: 200n }]);
  });

  it('excludes the zero address from portfolios (mint/burn sink)', () => {
    const s = new AnalyticsStore();
    const zero = '0x0000000000000000000000000000000000000000';
    const alice = '0xA1ce000000000000000000000000000000000001';
    // A mint: zero address -> alice.
    s.apply(transfer({ from: zero, to: alice, value: '1000' }));

    expect(s.portfolio(zero)).toEqual([]);
    expect(s.portfolio(alice)[0]?.netFlow).toBe(1000n);
  });

  it('ranks pools by swap count', () => {
    const s = new AnalyticsStore();
    s.apply(swap({ poolAddress: '0xP1' }));
    s.apply(swap({ poolAddress: '0xP1' }));
    s.apply(swap({ poolAddress: '0xP2' }));

    const pools = s.topPools(10);
    expect(pools[0]?.pool).toBe('0xP1');
    expect(pools[0]?.swaps).toBe(2);
    expect(pools[1]?.pool).toBe('0xP2');
  });

  it('keeps recent activity most-recent-first and bounded', () => {
    const s = new AnalyticsStore();
    for (let i = 0; i < 150; i++) s.apply(transfer({ blockNumber: i, txHash: `0x${i}` }));

    const recent = s.recent(5);
    expect(recent).toHaveLength(5);
    // Most recent (highest block) first.
    expect(recent[0]?.block).toBe(149);
    expect(recent[4]?.block).toBe(145);
  });

  it('tolerates malformed amount strings without throwing', () => {
    const s = new AnalyticsStore();
    s.apply(transfer({ value: 'not-a-number' }));
    expect(s.topTokens(1)[0]?.volume).toBe(0n);
  });
});
