-- Checkpoint: one row per chain. The last durably indexed block and its
-- hash, which the poller compares against the chain to detect reorgs.
CREATE TABLE IF NOT EXISTS checkpoints (
    chain_id        BIGINT PRIMARY KEY,
    last_block      BIGINT      NOT NULL,
    last_block_hash CHAR(66)    NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Raw audit trail of every matched log. Lets us backfill new typed
-- tables later without re-reading the chain.
CREATE TABLE IF NOT EXISTS event_logs (
    id           BIGSERIAL PRIMARY KEY,
    block_number BIGINT      NOT NULL,
    block_time   TIMESTAMPTZ NOT NULL,
    tx_hash      CHAR(66)    NOT NULL,
    log_index    INT         NOT NULL,
    address      CHAR(42)    NOT NULL,
    topics       TEXT[]      NOT NULL,
    data         BYTEA       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tx_hash, log_index)
);
CREATE INDEX IF NOT EXISTS idx_event_logs_block   ON event_logs (block_number);
CREATE INDEX IF NOT EXISTS idx_event_logs_address ON event_logs (address);

-- Decoded ERC-20 transfers. value is NUMERIC(78,0): wide enough for any
-- uint256, and sortable/aggregatable unlike TEXT.
CREATE TABLE IF NOT EXISTS erc20_transfers (
    id           BIGSERIAL PRIMARY KEY,
    block_number BIGINT        NOT NULL,
    block_time   TIMESTAMPTZ   NOT NULL,
    tx_hash      CHAR(66)      NOT NULL,
    log_index    INT           NOT NULL,
    token        CHAR(42)      NOT NULL,
    from_addr    CHAR(42)      NOT NULL,
    to_addr      CHAR(42)      NOT NULL,
    value        NUMERIC(78,0) NOT NULL,
    created_at   TIMESTAMPTZ   NOT NULL DEFAULT now(),
    UNIQUE (tx_hash, log_index)
);
CREATE INDEX IF NOT EXISTS idx_transfers_block ON erc20_transfers (block_number DESC);
CREATE INDEX IF NOT EXISTS idx_transfers_token ON erc20_transfers (token);
CREATE INDEX IF NOT EXISTS idx_transfers_from  ON erc20_transfers (from_addr);
CREATE INDEX IF NOT EXISTS idx_transfers_to    ON erc20_transfers (to_addr);

-- Decoded swaps, V2 and V3 normalized to one in/out shape.
CREATE TABLE IF NOT EXISTS swaps (
    id           BIGSERIAL PRIMARY KEY,
    block_number BIGINT        NOT NULL,
    block_time   TIMESTAMPTZ   NOT NULL,
    tx_hash      CHAR(66)      NOT NULL,
    log_index    INT           NOT NULL,
    pool         CHAR(42)      NOT NULL,
    sender       CHAR(42)      NOT NULL,
    recipient    CHAR(42)      NOT NULL,
    amount0_in   NUMERIC(78,0) NOT NULL,
    amount1_in   NUMERIC(78,0) NOT NULL,
    amount0_out  NUMERIC(78,0) NOT NULL,
    amount1_out  NUMERIC(78,0) NOT NULL,
    venue        TEXT          NOT NULL,
    created_at   TIMESTAMPTZ   NOT NULL DEFAULT now(),
    UNIQUE (tx_hash, log_index)
);
CREATE INDEX IF NOT EXISTS idx_swaps_block ON swaps (block_number DESC);
CREATE INDEX IF NOT EXISTS idx_swaps_pool  ON swaps (pool);
