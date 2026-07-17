-- ============================================================
-- SCORCHED — Test Seed Data (rewritten for schema_erc8004_8183.sql)
-- ============================================================
-- Matches the real ERC-8004 (identity/reputation/validation) + ERC-8183
-- (job escrow) tables. All IDs, addresses, and tx hashes below are
-- clearly synthetic (sequential small integers, 0xaaaa.../0xbbbb...-style
-- addresses) — deliberately NOT reusing any of the real mainnet addresses
-- found during the audit (e.g. the canonical ERC-8004 registry or
-- CivilisAI's ACPV2 contract), so demo data is never confused with real
-- on-chain data if someone diffs against the real chain. See AUDIT.md.

-- ── Agents (ERC-8004 IdentityRegistry) ──────────────────────

INSERT INTO agents (agent_id, owner_address, agent_uri, first_seen_at, updated_at) VALUES
  (8841, '0xaaaa000000000000000000000000000000aaaa', 'https://example-agents.test/agent/8841.json', NOW() - INTERVAL '30 days', NOW() - INTERVAL '1 day'),
  (441,  '0xbbbb000000000000000000000000000000bbbb', 'https://example-agents.test/agent/441.json',  NOW() - INTERVAL '45 days', NOW() - INTERVAL '2 days'),
  (992,  '0xcccc000000000000000000000000000000cccc', 'https://example-agents.test/agent/992.json',  NOW() - INTERVAL '20 days', NOW() - INTERVAL '1 day'),
  (1001, '0xdddd000000000000000000000000000000dddd', 'https://example-agents.test/agent/1001.json', NOW() - INTERVAL '60 days', NOW() - INTERVAL '5 days'),
  (5555, '0xeeee000000000000000000000000000000eeee', 'https://example-agents.test/agent/5555.json', NOW() - INTERVAL '15 days', NOW() - INTERVAL '3 days'),
  (7777, '0xffff000000000000000000000000000000ffff', 'https://example-agents.test/agent/7777.json', NOW() - INTERVAL '10 days', NOW() - INTERVAL '1 day')
ON CONFLICT (agent_id) DO NOTHING;

INSERT INTO agent_metadata (agent_id, metadata_key, metadata_value, updated_at) VALUES
  (8841, 'agentWallet', decode('aaaa000000000000000000000000000000aaaa', 'hex'), NOW() - INTERVAL '30 days'),
  (8841, 'category', convert_to('code-audit', 'UTF8'), NOW() - INTERVAL '30 days'),
  (441,  'agentWallet', decode('bbbb000000000000000000000000000000bbbb', 'hex'), NOW() - INTERVAL '45 days'),
  (441,  'category', convert_to('data-scraping', 'UTF8'), NOW() - INTERVAL '45 days'),
  (992,  'agentWallet', decode('cccc000000000000000000000000000000cccc', 'hex'), NOW() - INTERVAL '20 days'),
  (992,  'category', convert_to('verification', 'UTF8'), NOW() - INTERVAL '20 days')
ON CONFLICT (agent_id, metadata_key) DO NOTHING;

-- ── Feedback (ERC-8004 ReputationRegistry) ──────────────────

INSERT INTO feedback (agent_id, client_address, feedback_index, value, value_decimals, tag1, tag2, endpoint, feedback_uri, feedback_hash, is_revoked, tx_hash, block_number, created_at) VALUES
  (8841, '0xbbbb000000000000000000000000000000bbbb', 1, 95, 2, 'code-audit', 'security', 'https://example-agents.test/api', 'https://example-agents.test/feedback/1', '0x1111111111111111111111111111111111111111111111111111111111111a', false, '0xaaa1111111111111111111111111111111111111111111111111111111111a', 1000010, NOW() - INTERVAL '25 days'),
  (8841, '0xdddd000000000000000000000000000000dddd', 2, 88, 2, 'code-audit', 'gas-optimization', 'https://example-agents.test/api', 'https://example-agents.test/feedback/2', '0x1111111111111111111111111111111111111111111111111111111111111b', false, '0xaaa1111111111111111111111111111111111111111111111111111111111b', 1002210, NOW() - INTERVAL '18 days'),
  (441,  '0xaaaa000000000000000000000000000000aaaa', 1, 82, 2, 'data-scraping', 'accuracy', 'https://example-agents.test/api', 'https://example-agents.test/feedback/3', '0x1111111111111111111111111111111111111111111111111111111111111c', false, '0xaaa1111111111111111111111111111111111111111111111111111111111c', 1005310, NOW() - INTERVAL '30 days'),
  (992,  '0xaaaa000000000000000000000000000000aaaa', 1, 97, 2, 'verification', 'reliability', 'https://example-agents.test/api', 'https://example-agents.test/feedback/4', '0x1111111111111111111111111111111111111111111111111111111111111d', false, '0xaaa1111111111111111111111111111111111111111111111111111111111d', 1006010, NOW() - INTERVAL '12 days'),
  (5555, '0xffff000000000000000000000000000000ffff', 1, 55, 2, 'analysis', NULL, 'https://example-agents.test/api', 'https://example-agents.test/feedback/5', '0x1111111111111111111111111111111111111111111111111111111111111e', false, '0xaaa1111111111111111111111111111111111111111111111111111111111e', 1006510, NOW() - INTERVAL '9 days'),
  (7777, '0xdddd000000000000000000000000000000dddd', 1, -20, 2, 'translation', 'quality-issue', 'https://example-agents.test/api', 'https://example-agents.test/feedback/6', '0x1111111111111111111111111111111111111111111111111111111111111f', false, '0xaaa1111111111111111111111111111111111111111111111111111111111f', 1006810, NOW() - INTERVAL '4 days')
ON CONFLICT (agent_id, client_address, feedback_index) DO NOTHING;

-- ── Validations (ERC-8004 ValidationRegistry) ───────────────

INSERT INTO validations (request_hash, validator_address, agent_id, request_uri, response, response_uri, response_hash, tag, requested_at, responded_at) VALUES
  ('0x2222222222222222222222222222222222222222222222222222222222222a', '0xcccc000000000000000000000000000000cccc', 8841, 'https://example-agents.test/validation-request/1', 92, 'https://example-agents.test/validation-response/1', '0x2222222222222222222222222222222222222222222222222222222222222b', 'code-audit', NOW() - INTERVAL '17 days', NOW() - INTERVAL '16 days'),
  ('0x2222222222222222222222222222222222222222222222222222222222222c', '0xcccc000000000000000000000000000000cccc', 441, 'https://example-agents.test/validation-request/2', NULL, NULL, NULL, 'data-scraping', NOW() - INTERVAL '2 days', NULL)
ON CONFLICT (request_hash) DO NOTHING;

-- ── Jobs (ERC-8183 AgenticCommerce) — one of every status ───

INSERT INTO jobs (job_id, client, provider, evaluator, hook, budget, expired_at, status, created_at, updated_at) VALUES
  (101, '0xaaaa000000000000000000000000000000aaaa', NULL, '0xcccc000000000000000000000000000000cccc', NULL, 0, NOW() + INTERVAL '5 days', 'Open', NOW() - INTERVAL '1 days', NOW() - INTERVAL '1 days'),
  (102, '0xaaaa000000000000000000000000000000aaaa', '0xbbbb000000000000000000000000000000bbbb', '0xcccc000000000000000000000000000000cccc', NULL, 500000000000000000, NOW() + INTERVAL '7 days', 'Funded', NOW() - INTERVAL '3 days', NOW() - INTERVAL '2 days'),
  (103, '0xdddd000000000000000000000000000000dddd', '0xbbbb000000000000000000000000000000bbbb', '0xcccc000000000000000000000000000000cccc', NULL, 250000000000000000, NOW() + INTERVAL '2 days', 'Submitted', NOW() - INTERVAL '6 days', NOW() - INTERVAL '1 days'),
  (104, '0xaaaa000000000000000000000000000000aaaa', '0xeeee000000000000000000000000000000eeee', '0xcccc000000000000000000000000000000cccc', NULL, 1000000000000000000, NOW() - INTERVAL '2 days', 'Completed', NOW() - INTERVAL '10 days', NOW() - INTERVAL '2 days'),
  (105, '0xffff000000000000000000000000000000ffff', '0xdddd000000000000000000000000000000dddd', '0xcccc000000000000000000000000000000cccc', NULL, 300000000000000000, NOW() - INTERVAL '1 days', 'Rejected', NOW() - INTERVAL '8 days', NOW() - INTERVAL '1 days'),
  (106, '0xeeee000000000000000000000000000000eeee', '0xaaaa000000000000000000000000000000aaaa', '0xcccc000000000000000000000000000000cccc', NULL, 150000000000000000, NOW() - INTERVAL '5 days', 'Expired', NOW() - INTERVAL '20 days', NOW() - INTERVAL '5 days')
ON CONFLICT (job_id) DO NOTHING;

INSERT INTO job_submissions (job_id, deliverable, submitted_at) VALUES
  (103, '0x3333333333333333333333333333333333333333333333333333333333333a', NOW() - INTERVAL '2 days'),
  (104, '0x3333333333333333333333333333333333333333333333333333333333333b', NOW() - INTERVAL '3 days')
;

INSERT INTO job_settlements (job_id, kind, actor, reason, tx_hash, block_number, created_at) VALUES
  (104, 'completed', '0xcccc000000000000000000000000000000cccc', '0x4444444444444444444444444444444444444444444444444444444444444a', '0xbbb2222222222222222222222222222222222222222222222222222222222a', 1010010, NOW() - INTERVAL '2 days'),
  (105, 'rejected',  '0xcccc000000000000000000000000000000cccc', '0x4444444444444444444444444444444444444444444444444444444444444b', '0xbbb2222222222222222222222222222222222222222222222222222222222b', 1010510, NOW() - INTERVAL '1 days'),
  (106, 'expired',   NULL,                                            NULL,                                                                '0xbbb2222222222222222222222222222222222222222222222222222222222c', 1011010, NOW() - INTERVAL '5 days')
;

INSERT INTO job_payments (job_id, kind, recipient, amount, tx_hash, block_number, created_at) VALUES
  (104, 'payment_released', '0xeeee000000000000000000000000000000eeee', 900000000000000000, '0xccc3333333333333333333333333333333333333333333333333333333333a', 1010020, NOW() - INTERVAL '2 days'),
  (104, 'evaluator_fee',    '0xcccc000000000000000000000000000000cccc', 100000000000000000, '0xccc3333333333333333333333333333333333333333333333333333333333b', 1010021, NOW() - INTERVAL '2 days'),
  (105, 'refund',           '0xffff000000000000000000000000000000ffff', 300000000000000000, '0xccc3333333333333333333333333333333333333333333333333333333333c', 1010520, NOW() - INTERVAL '1 days')
;

REFRESH MATERIALIZED VIEW CONCURRENTLY agent_reputation;
