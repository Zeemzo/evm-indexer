# EVM Event Indexer (Go)

A production-grade blockchain indexing service for EVM chains, written in Go. Ships with presets for **Ethereum, Polygon, Arbitrum, Optimism and Base** — one binary, `CHAIN=<name>` selects the network.

> **Pipeline:** Safe-head polling → topic-filtered log fetch (concurrent) → ABI decoding → atomic Postgres writes with checkpointing → REST API + WebSocket streaming + Prometheus metrics

Indexes **ERC-20 `Transfer`** and **Uniswap V2/V3 `Swap`** events, normalizing both swap layouts into a single model. Live-verified against Ethereum and Polygon mainnet.

## Multi-chain

The pipeline is EVM-generic; only per-network defaults differ, bundled as **presets** (see [chains.go](internal/config/chains.go)). `CHAIN=<name>` seeds `RPC_URL`, `CHAIN_ID`, `POLL_INTERVAL`, `BATCH_SIZE`, and the safe-head strategy; every value stays overridable via its own env var. Run one instance per chain, each with its own database.

```bash
CHAIN=ethereum DATABASE_URL=...eth   ./indexer   # finalized-tag, batch 200, 12s
CHAIN=polygon  DATABASE_URL=...poly  ./indexer   # 64 confirmations, batch 500, 2s
```

**Two safe-head strategies**, chosen per chain, are the core correctness decision:

| Strategy | Chains | How | Why |
|---|---|---|---|
| **Finalized tag** | Ethereum, L2s | Index only up to the `finalized` block | Post-Merge PoS finality is cheap and fast; finalized blocks *cannot* be reorged — the strongest guarantee |
| **Confirmation depth** | Polygon | Index `head − CONFIRMATIONS` blocks | Polygon's checkpoint finality can lag minutes; a fixed margin plus the hash-verify + rewind safety net is more practical |

---

## Why it's built this way

Correctness under chain reorganizations is the core design constraint — Polygon reorgs shallowly but frequently:

| Mechanism | What it does |
|---|---|
| **Safe-head gating** | Only blocks up to the `finalized` tag (Ethereum/L2s) or `head − CONFIRMATIONS` (Polygon) are indexed, so reorgs never reach the database — see [Multi-chain](#multi-chain) |
| **Checkpoint hash verification** | The last indexed block's hash is stored with the checkpoint; every cycle re-fetches that header and compares. A mismatch means a deep reorg happened |
| **Automatic rewind** | On mismatch, all data above `checkpoint − REORG_REWIND` is deleted and re-indexed in one transaction |
| **Idempotent writes** | Every insert is `ON CONFLICT (tx_hash, log_index) DO NOTHING`, so replays after crashes or rewinds are safe |
| **Atomic batches** | Raw logs, typed rows, and the checkpoint advance commit in a single transaction — the database can never be half-updated |
| **Persistent checkpoint** | Restart resumes exactly where it left off; no blocks skipped, no duplicates |

Performance:

- **Server-side topic filtering** — `eth_getLogs` is filtered by the three known `topic0` values at the RPC node; irrelevant logs never cross the wire.
- **Concurrent fetching** — block ranges are split into chunks fetched by parallel goroutines (`errgroup`, bounded by `FETCH_CONCURRENCY`); block headers for timestamps are fetched the same way.
- **Adaptive pacing** — polls back-to-back while behind (backfill), eases off to `POLL_INTERVAL` once caught up.
- **Non-blocking streaming** — WebSocket fan-out uses per-subscriber buffered channels; a slow client drops its own messages and can never stall the indexing hot path.

## Architecture

```
EVM RPC (Ethereum / Polygon / L2)
    │  eth_blockNumber / eth_getLogs (topic-filtered) / eth_getBlockByNumber[+finalized]
    ▼
┌─────────────────────────────────────────────────────┐
│ Indexer loop (internal/indexer)                     │
│  1. safe head = finalized tag OR head − CONFIRMATIONS│
│  2. verify checkpoint hash  ──reorg?──▶ rewind      │
│  3. fetch log chunks concurrently (errgroup)        │
│  4. fetch headers for timestamps (concurrent)       │
│  5. decode: ERC-20 Transfer, V2 Swap, V3 Swap       │
│  6. WriteBatch: raw + typed + checkpoint (1 txn)    │
│  7. publish to broker                               │
└──────────────┬──────────────────────┬───────────────┘
               ▼                      ▼
        PostgreSQL             PubSub broker
   (event_logs, erc20_        (per-subscriber
    transfers, swaps,          buffered channels)
    checkpoints)                      │
               ▲                      ▼
┌──────────────┴──────────────────────────────────────┐
│ HTTP server (internal/api, chi)                     │
│  REST: /transfers /swaps /stats /status /health     │
│  WS:   /ws?type=erc20_transfer|swap                 │
│  Prometheus: /metrics                               │
└─────────────────────────────────────────────────────┘
```

## Tech stack

| Component | Choice |
|---|---|
| Language | Go 1.24 |
| Chain access | go-ethereum (`ethclient`) |
| Database | PostgreSQL 16, `pgx/v5` (no ORM), embedded SQL migrations |
| HTTP | `chi` v5 |
| WebSocket | `gorilla/websocket` |
| Metrics | Prometheus client |
| Logging | `log/slog` (structured JSON) |
| Tests | `testify`, race-detector clean |

## Quick start

```bash
cp .env.example .env          # pick CHAIN; set RPC_URL to your provider for real throughput

# Option A: everything in Docker
docker compose up -d --build

# Option B: Postgres in Docker, indexer locally
make up                        # starts Postgres
set -a; source .env; set +a
make run

# Index Ethereum instead — same binary, own database:
createdb eth_indexer
CHAIN=ethereum \
DATABASE_URL=postgres://postgres:postgres@localhost:5432/eth_indexer?sslmode=disable \
make run
```

Migrations run automatically on startup.

### Verify

```bash
curl -s localhost:8080/health | jq
curl -s localhost:8080/status | jq
curl -s localhost:8080/stats  | jq

# Recent transfers, filtered
curl -s "localhost:8080/transfers?limit=5" | jq
curl -s "localhost:8080/transfers?token=0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174&limit=5" | jq

# Recent swaps
curl -s "localhost:8080/swaps?venue=v3&limit=5" | jq

# Live stream (all events, or filter by type)
websocat "ws://localhost:8080/ws"
websocat "ws://localhost:8080/ws?type=swap"

# Prometheus metrics
curl -s localhost:8080/metrics | grep indexer_
```

## API

| Endpoint | Description |
|---|---|
| `GET /transfers` | Decoded ERC-20 transfers. Filters: `token`, `from`, `to`, `beforeBlock` (keyset pagination), `limit` (max 500) |
| `GET /swaps` | Decoded swaps. Filters: `pool`, `venue` (`v2`/`v3`), `beforeBlock`, `limit` |
| `GET /stats` | Row counts and last indexed block |
| `GET /status` | Indexer state: head, lag, caught-up flag, error/reorg counters, uptime |
| `GET /health` | Liveness/readiness (checks DB + indexer loop); 503 when unhealthy |
| `GET /ws` | WebSocket stream of newly indexed events; optional `?type=` filter |
| `GET /metrics` | Prometheus metrics |

Token amounts are decimal strings end to end (`uint256` doesn't fit in JSON numbers) and stored as `NUMERIC(78,0)` — sortable and aggregatable in SQL, unlike text.

### Key metrics

`indexer_last_indexed_block`, `indexer_chain_head_block`, `indexer_blocks_behind`, `indexer_events_indexed_total{type}`, `indexer_rpc_calls_total{method,outcome}`, `indexer_rpc_duration_seconds{method}`, `indexer_reorgs_detected_total`, `indexer_cycle_duration_seconds`, `indexer_websocket_subscribers`

## Project structure

```
cmd/indexer/          Entry point: wiring + graceful shutdown (signal → context → errgroup)
internal/
  chain/              ethclient wrapper (retry, timeout, metrics) + topic-based ABI decoder
  indexer/            Poll cycle: confirmations, reorg detect/rewind, concurrent fetch, ordering
  storage/            pgx pool, embedded migrations, atomic batch writes, checkpoint, queries
  api/                chi REST handlers + WebSocket streaming
  pubsub/             Generic non-blocking broker (per-subscriber buffered channels)
  events/             Shared decoded-event model
  config/             Env-based configuration with validation
  metrics/            Prometheus instrumentation
pkg/retry/            Context-aware exponential backoff with full jitter
```

## Testing

```bash
make test        # unit tests
make test-race   # with the race detector
```

Coverage focus:

- **Decoder** — real topic hashes pinned against known signatures; ERC-20 vs ERC-721 disambiguation; V3 signed `int256` two's-complement amounts in both directions; malformed/truncated data
- **Indexer cycle** — against an in-memory fake chain + store: confirmation depth respected, checkpoint advances exactly to safe head, reorg detection triggers rewind to the right block and re-indexes, `MAX_BLOCKS_PER_CYCLE` cap, fresh-start semantics, broker publishing
- **Retry** — backoff, exhaustion preserving the original error for `errors.Is`, context cancellation
- **PubSub** — fan-out, unsubscribe, slow-consumer drop counting, idempotent close

## Configuration

All via environment; see [.env.example](.env.example). Notable knobs:

| Var | Default | Notes |
|---|---|---|
| `CHAIN` | `polygon` | Preset selector: `ethereum`, `polygon`, `arbitrum`, `optimism`, `base` |
| `USE_FINALIZED` | preset | `true` = index to the `finalized` tag; `false` = use `CONFIRMATIONS` |
| `CONFIRMATIONS` | preset | Reorg safety margin when `USE_FINALIZED=false` |
| `BATCH_SIZE` | preset | Blocks per `eth_getLogs`; lower for restrictive public RPCs |
| `MAX_BLOCKS_PER_CYCLE` | 5000 | Bounds memory + transaction size during backfill |
| `FETCH_CONCURRENCY` | 4 | Parallel RPC fetches; raise with a paid provider |
| `REORG_REWIND` | 256 | Rewind distance on deep-reorg detection |
| `START_BLOCK` | 0 | 0 = start at current safe head (no backfill) |

Presets set `CHAIN_ID`, `RPC_URL`, `CONFIRMATIONS`, `POLL_INTERVAL`, `BATCH_SIZE`, and `USE_FINALIZED` per network; each is overridable. Full list: [chains.go](internal/config/chains.go).

## Design decisions

| Decision | Rationale |
|---|---|
| Polling over `eth_subscribe` | Deterministic, resumable, no silent WebSocket drops; confirmations + checkpoints need pull semantics anyway |
| `pgx` over an ORM | Explicit SQL, `pgx.Batch` for one-round-trip bulk inserts, no reflection overhead on the hot path |
| Raw + typed dual storage | Typed tables serve fast queries; raw `event_logs` lets new event types be backfilled without re-reading the chain |
| `NUMERIC(78,0)` for amounts | Any `uint256` fits; sortable/aggregatable in SQL unlike text columns |
| Consumer-side interfaces | `chain.Client` and `indexer.Store` are defined where they're used — the whole pipeline tests without a node or database |
| Crash on 10 consecutive failed cycles | Fail fast and let the orchestrator restart clean, rather than limping with unknown state |
