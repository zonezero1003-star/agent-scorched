-- ============================================================
-- Scorched — corrected schema for REAL ERC-8004 + ERC-8183 events
-- ============================================================
-- The original scorched_postgresql_schema_OLD_UNUSED.sql (agents/quotes/escrows/
-- milestones/disputes/bond_slash_events) was built around a fabricated ABI
-- and does not match the real event shapes. This file replaces the tables
-- the indexer actually writes to, matching scorched_indexer.go after the
-- ABI rewrite documented in AUDIT.md.
--
-- NOT YET DONE (see AUDIT.md "Still open" section): the GraphQL schema
-- (schema.graphqls), the API resolvers (scorched_api.go), the React
-- frontend (SupplyChainVisualizer.tsx), and seed_test_data.sql all still
-- reference the OLD fabricated tables/fields. They need a follow-up pass
-- to read from these corrected tables instead.

-- ── ERC-8004 Identity Registry ──────────────────────────────

CREATE TABLE IF NOT EXISTS agents (
    agent_id        NUMERIC PRIMARY KEY,      -- ERC-721 tokenId (uint256)
    owner_address   TEXT NOT NULL,            -- current owner (lowercase 0x-hex)
    agent_uri       TEXT,                     -- current agentURI (registration file location)
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_agents_owner ON agents(owner_address);

-- setMetadata() calls — includes the reserved `agentWallet` key set on register()
CREATE TABLE IF NOT EXISTS agent_metadata (
    agent_id        NUMERIC NOT NULL REFERENCES agents(agent_id),
    metadata_key    TEXT NOT NULL,
    metadata_value  BYTEA NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (agent_id, metadata_key)
);

-- ── ERC-8004 Reputation Registry ─────────────────────────────

CREATE TABLE IF NOT EXISTS feedback (
    agent_id        NUMERIC NOT NULL REFERENCES agents(agent_id),
    client_address  TEXT NOT NULL,
    feedback_index  BIGINT NOT NULL,          -- 1-indexed per (agent_id, client_address)
    value           NUMERIC NOT NULL,         -- int128, signed — can be negative
    value_decimals  SMALLINT NOT NULL,
    tag1            TEXT,
    tag2            TEXT,
    endpoint        TEXT,                     -- emitted, not stored on-chain — kept here for indexing only
    feedback_uri    TEXT,
    feedback_hash   TEXT,
    is_revoked      BOOLEAN NOT NULL DEFAULT FALSE,
    tx_hash         TEXT NOT NULL,
    block_number    BIGINT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (agent_id, client_address, feedback_index)
);
CREATE INDEX IF NOT EXISTS idx_feedback_agent ON feedback(agent_id);
CREATE INDEX IF NOT EXISTS idx_feedback_tag1 ON feedback(tag1);

CREATE TABLE IF NOT EXISTS feedback_responses (
    agent_id        NUMERIC NOT NULL,
    client_address  TEXT NOT NULL,
    feedback_index  BIGINT NOT NULL,
    responder       TEXT NOT NULL,
    response_uri    TEXT,
    response_hash   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (agent_id, client_address, feedback_index)
        REFERENCES feedback(agent_id, client_address, feedback_index)
);

-- ── ERC-8004 Validation Registry ─────────────────────────────

CREATE TABLE IF NOT EXISTS validations (
    request_hash      TEXT PRIMARY KEY,
    validator_address TEXT NOT NULL,
    agent_id          NUMERIC NOT NULL REFERENCES agents(agent_id),
    request_uri       TEXT,
    response          SMALLINT,               -- 0-100, NULL until responded
    response_uri      TEXT,
    response_hash     TEXT,
    tag               TEXT,
    requested_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    responded_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_validations_agent ON validations(agent_id);

-- ── ERC-8183 AgenticCommerce (job escrow) ────────────────────

CREATE TABLE IF NOT EXISTS jobs (
    job_id          NUMERIC PRIMARY KEY,
    client          TEXT NOT NULL,
    provider        TEXT,                     -- may be NULL until setProvider() if created with provider=0x0
    evaluator       TEXT NOT NULL,
    hook            TEXT,                     -- address(0) if no hook
    budget          NUMERIC NOT NULL DEFAULT 0,
    expired_at      TIMESTAMPTZ NOT NULL,
    -- Open | Funded | Submitted | Completed | Rejected | Expired
    status          TEXT NOT NULL DEFAULT 'Open',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_jobs_client ON jobs(client);
CREATE INDEX IF NOT EXISTS idx_jobs_provider ON jobs(provider);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);

CREATE TABLE IF NOT EXISTS job_submissions (
    job_id        NUMERIC NOT NULL REFERENCES jobs(job_id),
    deliverable   TEXT NOT NULL,   -- bytes32 hash/CID commitment
    submitted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS job_settlements (
    job_id         NUMERIC NOT NULL REFERENCES jobs(job_id),
    kind           TEXT NOT NULL CHECK (kind IN ('completed','rejected','expired')),
    actor          TEXT,            -- evaluator (complete/reject) or NULL (expired, permissionless)
    reason         TEXT,            -- optional attestation hash from complete()/reject()
    tx_hash        TEXT NOT NULL,
    block_number   BIGINT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS job_payments (
    job_id        NUMERIC NOT NULL REFERENCES jobs(job_id),
    kind          TEXT NOT NULL CHECK (kind IN ('payment_released','evaluator_fee','refund')),
    recipient     TEXT NOT NULL,
    amount        NUMERIC NOT NULL,
    tx_hash       TEXT NOT NULL,
    block_number  BIGINT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_job_payments_job ON job_payments(job_id);

-- ── Risk / fraud detection (Scorched's differentiator — see AUDIT.md) ──
-- Populated by scorched_riskscan.go's periodic scans, not by indexing
-- on-chain events directly. This is Scorched's own derived analysis,
-- clearly separated from raw indexed data.

CREATE TABLE IF NOT EXISTS risk_alerts (
    id              BIGSERIAL PRIMARY KEY,
    kind            TEXT NOT NULL CHECK (kind IN ('circular_hiring', 'stalled_job', 'reputation_drop')),
    -- Which agent(s)/job this alert concerns — only the relevant column(s)
    -- are set depending on kind.
    agent_id        NUMERIC REFERENCES agents(agent_id),
    job_id          NUMERIC REFERENCES jobs(job_id),
    related_agent_ids NUMERIC[],   -- e.g. the full cycle for circular_hiring
    severity        TEXT NOT NULL CHECK (severity IN ('low', 'medium', 'high')),
    details         JSONB NOT NULL DEFAULT '{}',  -- kind-specific structured detail (cycle value, days stalled, etc.)
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Dedup key so repeated scans don't re-alert on the same finding every
    -- run — e.g. "circular_hiring:8841,441,992" or "stalled_job:103"
    dedup_key       TEXT NOT NULL UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_risk_alerts_agent ON risk_alerts(agent_id);
CREATE INDEX IF NOT EXISTS idx_risk_alerts_job ON risk_alerts(job_id);
CREATE INDEX IF NOT EXISTS idx_risk_alerts_kind ON risk_alerts(kind);
CREATE INDEX IF NOT EXISTS idx_risk_alerts_detected ON risk_alerts(detected_at DESC);

-- Convenience view: current on-chain-derived reputation summary per agent.
-- (Not a substitute for calling the real ReputationRegistry.getSummary() —
-- this is a local read-model for the API to serve quickly.)
CREATE MATERIALIZED VIEW IF NOT EXISTS agent_reputation AS
SELECT
    agent_id,
    COUNT(*) FILTER (WHERE NOT is_revoked)                                   AS feedback_count,
    AVG(value::float / POWER(10, value_decimals)) FILTER (WHERE NOT is_revoked) AS avg_value
FROM feedback
GROUP BY agent_id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_reputation_agent ON agent_reputation(agent_id);
