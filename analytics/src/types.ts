// Event shapes mirror the Go indexer's internal/events model exactly, so
// the JSON published to the Redis Stream deserializes here unchanged.
// uint256 amounts are decimal strings on the wire; we parse them to
// BigInt for arithmetic and render them back to strings in API output.

export type EventType = 'erc20_transfer' | 'swap';
export type Venue = 'v2' | 'v3';

export interface Transfer {
  blockNumber: number;
  blockTime: string;
  txHash: string;
  logIndex: number;
  tokenAddress: string;
  from: string;
  to: string;
  value: string;
}

export interface Swap {
  blockNumber: number;
  blockTime: string;
  txHash: string;
  logIndex: number;
  poolAddress: string;
  sender: string;
  recipient: string;
  amount0In: string;
  amount1In: string;
  amount0Out: string;
  amount1Out: string;
  venue: Venue;
}

export interface IndexedEvent {
  type: EventType;
  transfer?: Transfer;
  swap?: Swap;
}

/** blockNumber of the underlying event regardless of type. */
export function eventBlock(e: IndexedEvent): number {
  return e.transfer?.blockNumber ?? e.swap?.blockNumber ?? 0;
}
