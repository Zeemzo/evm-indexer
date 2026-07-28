# EVM Analytics Service (TypeScript)

A TypeScript analytics service that consumes the Go indexer's event stream and serves derived analytics over REST + WebSocket. It is the read/analytics tier of a **polyglot, event-driven** system:

```
Go indexer ──XADD──▶ Redis Stream (evm.events) ──consumer group──▶ TS analytics ──▶ REST + WS
  (ingest)            (durable event bus)                          (derive + serve)
```

The two services share nothing but the stream contract: the indexer knows nothing about analytics, and this service scales independently (run N replicas in one consumer group and the stream load-balances across them).

## What it derives

From the ERC-20 Transfer and Uniswap V2/V3 Swap events on the stream, it maintains in memory (rebuilt from the stream's retained window on cold start):

- **Token leaderboard** — transfer count and summed volume per token (BigInt, no precision loss)
- **Pool leaderboard** — swap count per pool
- **Portfolios** — per-address net token flow (received − sent), excluding the zero address
- **Recent activity** — a rolling live feed
- **Totals** — events / transfers / swaps / last block

## API

| Endpoint | Description |
|---|---|
| `GET /health` | Readiness (200/503) |
| `GET /stats` | Totals |
| `GET /analytics/tokens?by=volume\|transfers&limit=N` | Token leaderboard |
| `GET /analytics/pools?limit=N` | Pool leaderboard |
| `GET /portfolio/:address` | Net token flow for an address |
| `GET /activity/recent?limit=N` | Recent events, newest first |
| `GET /ws` | WebSocket live feed of newly indexed events |

Amounts are decimal strings (uint256 doesn't fit a JSON number), matching the indexer.

## Run

```bash
npm install

# Needs the Go indexer running with REDIS_URL set, publishing to the stream.
cp .env.example .env
npm run dev        # or: npm run build && npm start
```

```bash
curl localhost:8090/stats
curl "localhost:8090/analytics/tokens?by=volume&limit=5"
curl localhost:8090/portfolio/0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174
websocat ws://localhost:8090/ws
```

## Test

```bash
npm test
```

Unit tests cover the aggregation reducer: BigInt volume summation, volume-vs-count ranking, net-flow math, zero-address exclusion, bounded recent buffer, and malformed-amount tolerance. The reducer ([src/analytics/store.ts](src/analytics/store.ts)) is pure and I/O-free, so it tests without Redis.

## Design notes

- **Consumer group, at-least-once:** messages are acked only after they're applied, so a crash redelivers un-acked work to a surviving replica.
- **Cold-start replay:** the group is created at offset `0`, so a fresh consumer rebuilds aggregates from the stream's retained window before tailing live.
- **BigInt end to end:** token amounts are parsed to BigInt for arithmetic and serialized back to strings at the API edge.
