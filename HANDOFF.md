# Scorched — Handoff: Layer 2 (GraphQL / API / Frontend / Seed)

> **STATUS: completed.** All four tasks below were finished in the same
> session this handoff doc was written in — see AUDIT.md's "all four
> follow-on tasks completed" section for what changed in each file. This
> doc is kept as a record of the plan/ground-truth used, in case further
> work builds on it (e.g. the still-open Redis-publish wiring noted in
> AUDIT.md).

You are continuing work on Scorched, an X Layer agent-economy explorer.
The indexer (`scorched_indexer.go`) and its Postgres schema
(`schema_erc8004_8183.sql`) were already rewritten to match the REAL
ERC-8004 and ERC-8183 event signatures — that layer is done and correct.
Everything ABOVE the indexer (GraphQL schema, API resolvers, frontend,
seed data) still reflects the OLD fabricated model and will not work
against the new tables until updated. That is your job.

Read `AUDIT.md` in full before touching any code — it documents every
finding, including which contract addresses are real vs. placeholder, and
the exact list of what's stale. Do not re-fabricate anything it flags as
unconfirmed.

## Ground truth to work from (do not deviate without re-verifying)

- **Real ERC-8004 event signatures**: https://eips.ethereum.org/EIPS/eip-8004
  (`Registered`, `MetadataSet`, `URIUpdated`, `NewFeedback`,
  `FeedbackRevoked`, `ResponseAppended`, `ValidationRequest`,
  `ValidationResponse`, plus standard ERC-721 `Transfer`)
- **Real ERC-8183 event signatures**: https://eips.ethereum.org/EIPS/eip-8183
  (`JobCreated`, `ProviderSet`, `BudgetSet`, `JobFunded`, `JobSubmitted`,
  `JobCompleted`, `JobRejected`, `JobExpired`, `PaymentReleased`,
  `EvaluatorFeePaid`, `Refunded`)
- **The new Postgres schema**: `schema_erc8004_8183.sql` — tables `agents`,
  `agent_metadata`, `feedback`, `feedback_responses`, `validations`,
  `jobs`, `job_submissions`, `job_settlements`, `job_payments`, and the
  `agent_reputation` materialized view. This is the only schema the API
  should read from going forward — `scorched_postgresql_schema_OLD_UNUSED.sql` is
  the OLD fabricated one and should be treated as dead/reference-only
  (don't delete it without checking nothing else depends on it, but don't
  write new code against it either).
- **Real contract addresses already confirmed** (see AUDIT.md for
  citations): ERC-8004 canonical registry on X Layer at
  `0x8004A169FB4a3325136EB29fA0ceB6D2e539a432` (identity) /
  `0x8004BAa17C55a88189AE136b182e5fdA19dE9b63` (reputation); a real,
  verifiable ERC-8183 escrow deployment at
  `0xBEf97c569a5b4a82C1e8f53792eC41c988A4316e` (CivilisAI's `ACPV2`, one
  project's own deployment, not a universal standard — see caveat in
  AUDIT.md before presenting it as canonical).

## Task 1 — GraphQL schema (`schema.graphqls`)

Replace the old fabricated types (built around bytes32 agent IDs, roles,
bonds, quotes, escrows, milestones, disputes) with types matching the new
SQL tables:

- `Agent` (agentId: ID! as uint256/string, ownerAddress, agentUri,
  firstSeenAt, updatedAt, feedbackCount, avgFeedbackValue — the last two
  come from the `agent_reputation` materialized view)
- `Feedback` (agentId, clientAddress, feedbackIndex, value, valueDecimals,
  tag1, tag2, endpoint, feedbackUri, feedbackHash, isRevoked, txHash,
  blockNumber, createdAt)
- `Validation` (requestHash: ID!, validatorAddress, agentId, requestUri,
  response, responseUri, responseHash, tag, requestedAt, respondedAt)
- `Job` (jobId: ID!, client, provider, evaluator, hook, budget, expiredAt,
  status: JobStatus enum [OPEN, FUNDED, SUBMITTED, COMPLETED, REJECTED,
  EXPIRED], createdAt, updatedAt)
- `JobSettlement`, `JobPayment` as sub-types or connected fields on `Job`
- Keep subscriptions (WebSocket) working — re-point them at the new
  tables' change events instead of the old escrow/dispute ones. Check
  `scorched_api.go`'s existing subscription/pubsub wiring before assuming
  the transport layer needs changes — it likely doesn't, only the payload
  shapes do.

Do not invent fields the schema doesn't need — keep it a faithful mirror
of what's actually queryable from the new tables plus the two registries'
real on-chain read functions (e.g. `getSummary`, `readAllFeedback` from
the EIP text) if you want live-chain reads alongside the indexed cache.

## Task 2 — API resolvers (`scorched_api.go`)

The resolvers currently query old table/column names (`quotes`, `escrows`,
`role`, `bond_staked`, etc. — grep for these to find every spot). Rewrite
each resolver to query the new tables. Preserve the existing
CORS/rate-limit/Redis-auth fixes already in this file from the earlier
audit pass — don't revert those while rewriting resolvers. The dynamic
query builder pattern (parameterized `$N` placeholders, not string
interpolation of user input) is safe and worth keeping as the model for
any new filtered queries you add.

## Task 3 — Frontend (`SupplyChainVisualizer.tsx` / `.css`)

This component currently visualizes quote → escrow → milestone → dispute
chains. Re-scope it to visualize what's actually real:
- Agent identity + reputation (feedback count, avg value) as node
  attributes
- Job lifecycle (`JobCreated` → `JobFunded` → `JobSubmitted` →
  `JobCompleted`/`Rejected`/`Expired`) as the edge/flow structure between
  client and provider agents
- Don't keep "milestone" or "dispute" UI elements — neither concept exists
  in the real ERC-8183 spec (it's a single evaluator attestation, not
  staged milestones or multi-party disputes). This was one of the original
  fabrication's biggest departures from the real standard; don't
  reintroduce it in the UI even if it'd look more impressive.

## Task 4 — Seed data (`seed_test_data.sql`)

Regenerate against the new schema: a handful of `agents` rows (with
plausible `agentUri` values following the real registration-file JSON
shape from EIP-8004), some `feedback` rows referencing them, and a few
`jobs` in different `status` values (at least one of each: Open, Funded,
Submitted, Completed, Rejected, Expired) so the frontend has something in
every state to render. Keep amounts/IDs realistic but clearly fake (don't
reuse the real mainnet addresses found during the audit as fake seed
data — that would misleadingly conflate real and synthetic data if anyone
diffs the demo against the real chain).

## Constraints that apply to all four tasks

- No Go/Node toolchain is available to compile-check in this sandbox as of
  the last session — if that's still true, do careful manual review
  (brace/paren balance, consistent naming) and say so explicitly rather
  than claiming it builds.
- Keep the honesty pattern from AUDIT.md: if something can't be verified
  or a real value isn't confirmed, say so in the code comments and in your
  handoff back, rather than filling gaps with plausible-looking invented
  values. That's what went wrong the first time this project was built.
- When you finish each task, update AUDIT.md's "Still open" list to check
  off what's done, rather than leaving it stale.
