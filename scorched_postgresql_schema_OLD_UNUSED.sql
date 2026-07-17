-- ============================================================
-- SCORCHED — Production PostgreSQL Schema for X Layer
-- ERC-8004 Identity + APP (Agent Payments Protocol) Indexer
-- ============================================================

-- Enable required extensions
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ============================================================
-- CORE TABLES
-- ============================================================

CREATE TABLE IF NOT EXISTS agents (
    agent_id BYTEA PRIMARY KEY,
    owner_address BYTEA NOT NULL,
    role SMALLINT NOT NULL CHECK (role IN (0, 1, 2, 3)),
    -- 0=User, 1=Provider, 2=Evaluator, 3=Arbitrator
    bond_staked NUMERIC(78,0) NOT NULL DEFAULT 0,
    bond_available NUMERIC(78,0) NOT NULL DEFAULT 0,
    uri TEXT NOT NULL,
    manifest_hash BYTEA,
    is_active BOOLEAN DEFAULT true,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_heartbeat_at TIMESTAMPTZ,
    last_heartbeat_block BIGINT,
    reputation_score DECIMAL(4,2) DEFAULT 0.00,
    total_jobs_completed BIGINT DEFAULT 0,
    total_jobs_disputed BIGINT DEFAULT 0,
    total_value_locked NUMERIC(78,0) DEFAULT 0,
    total_value_earned NUMERIC(78,0) DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS services (
    service_id BYTEA PRIMARY KEY,
    agent_id BYTEA NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT,
    uri TEXT NOT NULL,
    base_price NUMERIC(78,0) NOT NULL,
    currency SMALLINT NOT NULL CHECK (currency IN (0, 1, 2)),
    -- 0=USDC, 1=OKB, 2=USDT
    pricing_model SMALLINT NOT NULL CHECK (pricing_model IN (0, 1, 2, 3)),
    -- 0=Fixed, 1=Streaming, 2=Milestone, 3=PerCall
    category_tags TEXT[] DEFAULT '{}',
    avg_delivery_time_ms BIGINT,
    success_rate DECIMAL(5,2),
    is_available BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS quotes (
    quote_id BYTEA PRIMARY KEY,
    client_agent_id BYTEA NOT NULL REFERENCES agents(agent_id),
    provider_agent_id BYTEA NOT NULL REFERENCES agents(agent_id),
    service_id BYTEA NOT NULL REFERENCES services(service_id),
    parent_quote_id BYTEA REFERENCES quotes(quote_id),
    total_amount NUMERIC(78,0) NOT NULL,
    currency SMALLINT NOT NULL,
    status SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0, 1, 2, 3)),
    -- 0=Open, 1=Accepted, 2=Expired, 3=Rejected
    expiry_block BIGINT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    accepted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS escrows (
    escrow_id BYTEA PRIMARY KEY,
    quote_id BYTEA NOT NULL REFERENCES quotes(quote_id),
    status SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0, 1, 2, 3, 4)),
    -- 0=Open, 1=Active, 2=Completed, 3=Disputed, 4=Resolved
    total_locked NUMERIC(78,0) NOT NULL,
    total_released NUMERIC(78,0) DEFAULT 0,
    currency SMALLINT NOT NULL,
    milestone_count SMALLINT NOT NULL DEFAULT 1,
    current_milestone SMALLINT DEFAULT 0,
    dispute_window_blocks BIGINT,
    opened_at TIMESTAMPTZ DEFAULT NOW(),
    closed_at TIMESTAMPTZ,
    dispute_id BYTEA,
    gas_cost_okb NUMERIC(78,0) DEFAULT 0
);

CREATE TABLE IF NOT EXISTS milestones (
    escrow_id BYTEA NOT NULL REFERENCES escrows(escrow_id) ON DELETE CASCADE,
    milestone_index SMALLINT NOT NULL,
    amount NUMERIC(78,0) NOT NULL,
    status SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0, 1, 2, 3)),
    -- 0=Pending, 1=Submitted, 2=Approved, 3=Rejected
    submission_hash BYTEA,
    submitted_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    approved_by BYTEA REFERENCES agents(agent_id),
    PRIMARY KEY (escrow_id, milestone_index)
);

CREATE TABLE IF NOT EXISTS disputes (
    dispute_id BYTEA PRIMARY KEY,
    escrow_id BYTEA NOT NULL REFERENCES escrows(escrow_id),
    complainant_id BYTEA NOT NULL REFERENCES agents(agent_id),
    respondent_id BYTEA NOT NULL REFERENCES agents(agent_id),
    status SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0, 1, 2, 3)),
    -- 0=Open, 1=Evidence, 2=Voting, 3=Resolved
    ruling SMALLINT CHECK (ruling IN (0, 1, 2)),
    -- 0=ClientWins, 1=ProviderWins, 2=Split
    client_award NUMERIC(78,0),
    provider_award NUMERIC(78,0),
    bond_required NUMERIC(78,0),
    lead_arbitrator BYTEA REFERENCES agents(agent_id),
    opened_at TIMESTAMPTZ DEFAULT NOW(),
    resolved_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS dispute_arbitrators (
    dispute_id BYTEA NOT NULL REFERENCES disputes(dispute_id) ON DELETE CASCADE,
    arbitrator_id BYTEA NOT NULL REFERENCES agents(agent_id),
    has_voted BOOLEAN DEFAULT false,
    vote_ruling SMALLINT,
    vote_hash BYTEA,
    PRIMARY KEY (dispute_id, arbitrator_id)
);

CREATE TABLE IF NOT EXISTS payment_streams (
    stream_id SERIAL PRIMARY KEY,
    escrow_id BYTEA NOT NULL REFERENCES escrows(escrow_id),
    amount NUMERIC(78,0) NOT NULL,
    unit_count NUMERIC(78,0),
    unit_type TEXT,
    metered_by BYTEA REFERENCES agents(agent_id),
    tx_hash BYTEA NOT NULL,
    block_number BIGINT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS bond_slash_events (
    slash_id SERIAL PRIMARY KEY,
    agent_id BYTEA NOT NULL REFERENCES agents(agent_id),
    amount NUMERIC(78,0) NOT NULL,
    reason_escrow_id BYTEA REFERENCES escrows(escrow_id),
    slashed_by BYTEA NOT NULL REFERENCES agents(agent_id),
    tx_hash BYTEA NOT NULL,
    block_number BIGINT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS heartbeats (
    heartbeat_id SERIAL PRIMARY KEY,
    agent_id BYTEA NOT NULL REFERENCES agents(agent_id),
    block_number BIGINT NOT NULL,
    attestation BYTEA,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS transactions (
    tx_hash BYTEA PRIMARY KEY,
    block_number BIGINT NOT NULL,
    block_hash BYTEA NOT NULL,
    from_address BYTEA,
    to_address BYTEA,
    gas_used BIGINT,
    gas_price NUMERIC(78,0),
    okb_cost NUMERIC(78,0),
    status SMALLINT DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- INDEXES
-- ============================================================

CREATE INDEX IF NOT EXISTS idx_agents_owner ON agents(owner_address);
CREATE INDEX IF NOT EXISTS idx_agents_reputation ON agents(reputation_score DESC) WHERE is_active = true;
CREATE INDEX IF NOT EXISTS idx_agents_heartbeat ON agents(last_heartbeat_at DESC);

CREATE INDEX IF NOT EXISTS idx_services_agent ON services(agent_id);
CREATE INDEX IF NOT EXISTS idx_services_tags ON services USING GIN(category_tags);
CREATE INDEX IF NOT EXISTS idx_services_available ON services(is_available, reputation_score DESC);

CREATE INDEX IF NOT EXISTS idx_quotes_client ON quotes(client_agent_id, status);
CREATE INDEX IF NOT EXISTS idx_quotes_provider ON quotes(provider_agent_id, status);
CREATE INDEX IF NOT EXISTS idx_quotes_parent ON quotes(parent_quote_id);

CREATE INDEX IF NOT EXISTS idx_escrows_status ON escrows(status, opened_at);
CREATE INDEX IF NOT EXISTS idx_escrows_quote ON escrows(quote_id);
CREATE INDEX IF NOT EXISTS idx_escrows_dispute ON escrows(dispute_id);

CREATE INDEX IF NOT EXISTS idx_milestones_escrow ON milestones(escrow_id, status);

CREATE INDEX IF NOT EXISTS idx_disputes_status ON disputes(status, opened_at);
CREATE INDEX IF NOT EXISTS idx_disputes_escrow ON disputes(escrow_id);

CREATE INDEX IF NOT EXISTS idx_slash_agent ON bond_slash_events(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_slash_escrow ON bond_slash_events(reason_escrow_id);

CREATE INDEX IF NOT EXISTS idx_heartbeats_agent ON heartbeats(agent_id, block_number DESC);
CREATE INDEX IF NOT EXISTS idx_heartbeats_block ON heartbeats(block_number);

CREATE INDEX IF NOT EXISTS idx_streams_escrow ON payment_streams(escrow_id, created_at);
CREATE INDEX IF NOT EXISTS idx_transactions_block ON transactions(block_number);

-- ============================================================
-- MATERIALIZED VIEW: Reputation Snapshot
-- ============================================================

CREATE MATERIALIZED VIEW IF NOT EXISTS agent_reputation AS
WITH 
  job_stats AS (
    SELECT 
      q.provider_agent_id AS agent_id,
      COUNT(*) AS total_jobs,
      COUNT(*) FILTER (WHERE e.status = 2) AS completed,
      COUNT(*) FILTER (WHERE e.status = 4) AS disputed,
      AVG(EXTRACT(EPOCH FROM (m.approved_at - e.opened_at))) AS avg_resolution_time
    FROM escrows e
    JOIN quotes q ON e.quote_id = q.quote_id
    LEFT JOIN milestones m ON e.escrow_id = m.escrow_id AND m.status = 2
    GROUP BY q.provider_agent_id
  ),
  slash_stats AS (
    SELECT 
      agent_id,
      COUNT(*) AS slash_count,
      SUM(amount) AS total_slashed
    FROM bond_slash_events
    WHERE created_at > NOW() - INTERVAL '90 days'
    GROUP BY agent_id
  ),
  stream_stats AS (
    SELECT 
      q.provider_agent_id AS agent_id,
      AVG(ps.amount / NULLIF(ps.unit_count, 0)) AS avg_unit_price
    FROM payment_streams ps
    JOIN escrows e ON ps.escrow_id = e.escrow_id
    JOIN quotes q ON e.quote_id = q.quote_id
    WHERE ps.created_at > NOW() - INTERVAL '30 days'
    GROUP BY q.provider_agent_id
  )
SELECT 
  a.agent_id,
  LEAST(5.0, GREATEST(0.0,
    (COALESCE(js.completed, 0) * 0.5) +
    (CASE WHEN COALESCE(js.disputed, 0) = 0 THEN 1.0 ELSE 0.0 END) +
    (CASE WHEN COALESCE(ss.slash_count, 0) = 0 THEN 1.5 ELSE -1.0 * ss.slash_count END) +
    (CASE WHEN a.bond_staked > 100000000000000000000 THEN 1.0 ELSE 0.5 END) +
    (CASE WHEN js.avg_resolution_time < 3600 THEN 0.5 ELSE 0.0 END)
  )) AS reputation_score,
  COALESCE(js.total_jobs, 0) AS total_jobs,
  COALESCE(js.completed, 0) AS completed_jobs,
  COALESCE(js.disputed, 0) AS disputed_jobs,
  COALESCE(ss.slash_count, 0) AS slash_count_90d,
  COALESCE(ss.total_slashed, 0) AS total_slashed,
  a.bond_staked,
  a.bond_available,
  a.last_heartbeat_at,
  a.is_active
FROM agents a
LEFT JOIN job_stats js ON a.agent_id = js.agent_id
LEFT JOIN slash_stats ss ON a.agent_id = ss.agent_id
LEFT JOIN stream_stats st ON a.agent_id = st.agent_id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_reputation_agent ON agent_reputation(agent_id);

-- ============================================================
-- FUNCTIONS & TRIGGERS
-- ============================================================

CREATE OR REPLACE FUNCTION refresh_agent_reputation()
RETURNS TRIGGER AS $$
BEGIN
  REFRESH MATERIALIZED VIEW CONCURRENTLY agent_reputation;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- Trigger to refresh reputation on key events
CREATE OR REPLACE TRIGGER trg_refresh_reputation_milestone
AFTER INSERT OR UPDATE ON milestones
FOR EACH STATEMENT
EXECUTE FUNCTION refresh_agent_reputation();

CREATE OR REPLACE TRIGGER trg_refresh_reputation_slash
AFTER INSERT ON bond_slash_events
FOR EACH STATEMENT
EXECUTE FUNCTION refresh_agent_reputation();

CREATE OR REPLACE FUNCTION update_agent_timestamp()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_agents_updated
BEFORE UPDATE ON agents
FOR EACH ROW
EXECUTE FUNCTION update_agent_timestamp();

-- ============================================================
-- VIEWS FOR COMMON QUERIES
-- ============================================================

CREATE OR REPLACE VIEW agent_dashboard AS
SELECT 
  a.agent_id,
  a.owner_address,
  a.role,
  a.bond_staked,
  a.bond_available,
  a.reputation_score,
  a.total_jobs_completed,
  a.total_jobs_disputed,
  a.total_value_locked,
  a.total_value_earned,
  a.is_active,
  a.last_heartbeat_at,
  a.first_seen_at,
  COUNT(DISTINCT s.service_id) AS service_count,
  COUNT(DISTINCT e.escrow_id) FILTER (WHERE e.status = 1) AS active_escrows
FROM agents a
LEFT JOIN services s ON a.agent_id = s.agent_id
LEFT JOIN quotes q ON a.agent_id = q.provider_agent_id
LEFT JOIN escrows e ON q.quote_id = e.quote_id
GROUP BY a.agent_id;

CREATE OR REPLACE VIEW escrow_supply_chain AS
SELECT 
  e.escrow_id,
  e.status AS escrow_status,
  e.total_locked,
  e.total_released,
  e.currency,
  e.opened_at,
  e.closed_at,
  q.client_agent_id,
  q.provider_agent_id,
  q.service_id,
  q.parent_quote_id,
  q.total_amount,
  m.milestone_index,
  m.status AS milestone_status,
  m.amount AS milestone_amount,
  m.submitted_at,
  m.approved_at,
  m.approved_by
FROM escrows e
JOIN quotes q ON e.quote_id = q.quote_id
LEFT JOIN milestones m ON e.escrow_id = m.escrow_id;
