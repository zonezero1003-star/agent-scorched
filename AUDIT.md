# Scorched — Code Audit

Audited against the live X Layer developer docs (web3.okx.com/xlayer/docs) and
current news on OKX's Agent Payments Protocol / OKX.AI launch. Dated to this
build; re-verify anything network-related before shipping, since X Layer's
own stack has changed underneath it before (Polygon CDK → OP Stack).

## Fixed in this pass

1. **Invalid placeholder contract address (critical).**
   `APP_PROTOCOL` was set to `0x000000000000000000000000000000000000APP0` —
   not valid hex (`P` isn't a hex digit). `common.HexToAddress` would have
   silently mangled it instead of erroring. Fixed: addresses are now loaded
   from env with no default, and `NewIndexer` fails loudly at startup if
   either `ERC8004_REGISTRY` or `APP_PROTOCOL` isn't a real 20-byte hex
   address, instead of silently indexing nothing.

2. **HTTP RPC used for real-time subscriptions (critical).** The indexer
   dialed `XLAYER_RPC` (HTTPS) and then called `SubscribeFilterLogs` on it —
   HTTP JSON-RPC does not support `eth_subscribe`. Real X Layer WSS endpoints
   are `wss://xlayerws.okx.com` / `wss://ws.xlayer.tech` (the README's claimed
   `wss://rpc.xlayer.tech` doesn't exist in the docs). Fixed: added a
   dedicated `XLAYER_WS` config and a second `ethclient` dialed over
   WebSocket, used only for `subscribeRealtime`.

3. **CORS wildcard + `AllowCredentials: true`.** Fixed: origins now come from
   `CORS_ORIGINS` (comma-separated), defaulting to `http://localhost:3000`
   instead of `*`. The GraphQL WebSocket transport's `CheckOrigin` now checks
   the same allowlist instead of always returning `true`.

4. **`RATE_LIMIT_RPS` was defined but never enforced.** Added a minimal
   per-IP token-bucket middleware. It's intentionally simple (no new
   dependency) — swap for `golang.org/x/time/rate` or `httprate` before
   real production traffic.

5. **Redis had no password anywhere**, and `REDIS_PASSWORD` wasn't a field
   in either config struct. Added the field, wired it into both the indexer
   and API's Redis clients, and added `--requirepass` to the compose file
   (now required, no default).

6. **Weak shared default passwords baked into `docker-compose.yml` itself**
   (not just `.env.example`) for Postgres/Neo4j. Changed to
   `${VAR:?message}` syntax — compose now refuses to start without an
   explicit value in your `.env`.

7. **Postgres / Neo4j / Redis ports bound to all interfaces.** Changed to
   `127.0.0.1:<port>:<port>` so they're not reachable from outside the host
   by default.

8. **README inaccuracies:** "Polygon CDK" (X Layer has since moved to the OP
   Stack framework per current docs), "~200ms finality" (unverified against
   current docs — OP Stack rollups typically have a 7-day L1 challenge
   window for withdrawal finality, though L2 soft-confirmations are fast),
   and the non-existent `wss://rpc.xlayer.tech` endpoint. Corrected and
   flagged for re-verification since these values have moved before.

## UPDATE — ERC-8004 IS live on X Layer (correction to the section below)

The original version of this audit said no confirmed ERC-8004 deployment
existed on X Layer. That was wrong — verified against 8004scan.io's live
networks page:

- **X Layer (chain ID 196)**
  - Identity Registry: `0x8004A169FB4a3325136EB29fA0ceB6D2e539a432`
  - Reputation Registry: `0x8004BAa17C55a88189AE136b182e5fdA19dE9b63`
  - 4,830 agents registered, 4,473 feedback entries as of this check
  - Explorer: https://www.oklink.com/x-layer/address/0x8004A169FB4a3325136EB29fA0ceB6D2e539a432
  - Same deterministic (CREATE2) address as Ethereum, Base, BSC, Polygon, etc.
  - Live example: https://8004scan.io/agents/xlayer/5551 (CoAgentic) — avatar
    hosted on OKX's own CDN, suggesting OKX-affiliated usage, not just a
    random community deployment

**Action taken:** `ERC8004_REGISTRY` in `.env.example` now documents this
real address directly (still not set as a hardcoded default in code — you
still need to opt in via env — but you no longer need to deploy your own).

**Update — a real, verifiable ERC-8183 escrow deployment on X Layer mainnet
was found** (project: CivilisAI/Civilis-public, MIT licensed, public repo):

- `ACPV2` (their ERC-8183 Agentic Commerce contract):
  `0xBEf97c569a5b4a82C1e8f53792eC41c988A4316e`
- `CivilisCommerceV2` (commerce wrapper):
  `0x7bac782C23E72462C96891537C61a4C86E9F086e`
- Verifiable proof: a funded ERC-8183 job tx —
  `0xddb14433d31fad2e24e2a5cfbb574fff8c752c85cc1274cdd7549d3f546bcdb5` —
  and an ERC-8004 identity registration tx —
  `0x49458734988bda69679429328e0444ac917467b70e86999e7dcde0c623905d53` —
  both checkable on OKX's own explorer at
  `https://web3.okx.com/explorer/x-layer/tx/<hash>`

**Caveat, in the project's own words:** this is one project's private
deployment, not a universal OKX standard contract that all agents use. Their
own docs state "arena is not presented as fully funded escrow today" and
"not every intel purchase is funded." Treat it as a real, small, working
proof-of-concept to index — not the canonical escrow for the whole X Layer
agent economy (no such canonical singleton exists yet, unlike ERC-8004
identity, which does have one).

**Recommendation:** index identity against the canonical ERC-8004 singleton
(broadest real coverage — 4,830 agents), and index escrow against Civilis's
`ACPV2` as a working example with genuine on-chain activity, while being
transparent in the demo that it's one project's contract rather than an
X-Layer-wide standard. You can also deploy your own `AgenticCommerce`
instance later for full control — the reference implementation is CC0licensed
at the ERC-8183 EIP page.



---



**There is no confirmed real contract for Scorched to index yet.** This is
the actual blocker, not a code bug:

- ERC-8004 is a real emerging standard with real reference deployments (e.g.
  `0x8004A169FB4a3325136EB29fA0ceB6D2e539a432` on other chains) — but nothing
  is confirmed deployed on X Layer at a fixed address.
- OKX's real Agent Payments Protocol (launched publicly) settles primarily
  through an **off-chain x402 Facilitator API**
  (`POST /api/v6/pay/x402/verify`, `/settle`) — not a custom on-chain
  contract emitting `QuoteIssued` / `EscrowOpened` / `DisputeResolved` events
  the way this codebase's ABI assumes.
- OKX's own APP whitepaper lists **escrow as "coming soon"** — not confirmed
  live on-chain as of this build.

**Options, in order of hackathon-friendliness:**
1. Deploy your own minimal ERC-8004-style registry + escrow contract to X
   Layer testnet, point the indexer at those addresses, and demo against
   real (if small) on-chain data.
2. Pivot Scorched's real-time indexing to track actual settlement
   transactions (ERC-20 transfers to known facilitator addresses, or
   whatever real on-chain footprint APP does leave), and present the
   escrow/dispute/supply-chain graph views as a labeled "preview / roadmap"
   feature backed by your seed data — be upfront about this in the demo
   script rather than implying it's live.
3. Contact the OKX AI Genesis hackathon organizers directly and ask if
   they've published testnet contract addresses for hackathon use — quite
   possible given how new this launched (OKX.AI went to developer beta
   June 30, 2026).

## Update — indexer ABI/schema rewritten to match real events

The Go indexer's event ABIs, structs, dispatch, and handlers were rewritten
from scratch against the authoritative event signatures in the EIPs
themselves (not a summary or a guess):

- ERC-8004: https://eips.ethereum.org/EIPS/eip-8004 (Registered, MetadataSet,
  URIUpdated, NewFeedback, FeedbackRevoked, ResponseAppended,
  ValidationRequest, ValidationResponse — plus the standard ERC-721 Transfer
  the IdentityRegistry also emits)
- ERC-8183: https://eips.ethereum.org/EIPS/eip-8183 (JobCreated, ProviderSet,
  BudgetSet, JobFunded, JobSubmitted, JobCompleted, JobRejected, JobExpired,
  PaymentReleased, EvaluatorFeePaid, Refunded — copied from the reference
  `AgenticCommerce.sol` in the EIP text)

**A new schema file, `schema_erc8004_8183.sql`, replaces the tables the
indexer writes to** (agents / agent_metadata / feedback / feedback_responses
/ validations / jobs / job_submissions / job_settlements / job_payments).
The original `scorched_postgresql_schema_OLD_UNUSED.sql` was built around the
fabricated ABI (bytes32 agent IDs, roles, bonds, quotes, escrows,
milestones, disputes) and doesn't match what these contracts actually emit
— agent IDs are `uint256` (ERC-721 tokenIds), not `bytes32`, and there's no
on-chain concept of "role" or "bond" in either real standard.

**Update — all four follow-on tasks completed in this pass:**
- `schema.graphqls` — rewritten: Agent/Feedback/Validation/Job/JobSubmission/
  JobSettlement/JobPayment/SupplyChain types matching the new SQL tables.
  No more role/bond/escrow/milestone/dispute types.
- `scorched_api.go` — all resolvers and REST handlers rewritten against
  `schema_erc8004_8183.sql`. CORS/rate-limit/Redis-auth fixes from the
  earlier audit pass were preserved. One known gap: the indexer doesn't
  yet publish to Redis pub/sub on writes (`job:<jobId>`, `feedback:<agentId>`
  channels the new subscriptions listen on) — subscriptions will connect
  but stay silent until that publish call is added to
  `scorched_indexer.go`'s handlers.
- `SupplyChainVisualizer.tsx` — rewritten to visualize the real Job
  lifecycle (Open → Funded → Submitted → Completed/Rejected/Expired)
  instead of milestones/disputes, and agent reputation via feedback
  count + average value instead of a fabricated 0-5 "reputationScore".
  Budget amounts are labeled "raw units" rather than assumed-USDC-decimals,
  since ERC-8183 doesn't fix a payment token/decimals in its events.
- `seed_test_data.sql` — regenerated against the new tables: 6 agents,
  metadata, feedback (including one negative-value example, since ERC-8004
  feedback values are signed), 2 validations, and 6 jobs covering every
  status (Open/Funded/Submitted/Completed/Rejected/Expired) with matching
  submissions/settlements/payments. All addresses/hashes are clearly
  synthetic (`0xaaaa...`, `0xbbbb...` patterns) and deliberately do NOT
  reuse the real mainnet addresses found during the audit.

**Still not done — flagging honestly:** the Redis-publish wiring mentioned
above (subscriptions are structurally correct but won't fire live events
until the indexer publishes), and no Go/Node toolchain was available in
this sandbox to compile-check any of this — manual review only (grep for
stale references, brace/paren balance). Compile and run this yourself
before a live demo.



## New — risk/fraud detection layer (Scorched's actual differentiator)

Market check before building this (see chat): generic ERC-8004 explorers
already exist and are well-established — 8004scan.io (500k+ agents, 21+
chains, built by AltLayer, already nicknamed "Etherscan for AI Agents" in
the community), scorched.info (Alias.AI), and RNWY (4.5M+ commerce jobs
indexed with trust scoring). Re-building "search an agent, see a score"
would be building something that already exists, better, at bigger scale.

The sharpened angle: those tools show a reputation number. None of them
appear to actively watch the commerce graph for signs that number is being
gamed, or that money is stuck, and push alerts rather than waiting to be
searched. That's what `scorched_riskscan.go` (new file) does — three
periodic checks:

1. **Circular hiring detection** — agents hiring each other in a closed
   loop (2-8 hops) with real funded budgets, which can manufacture
   feedback/activity that looks organic. Runs as a Cypher query against
   the Neo4j `HIRED` graph (see the R1-R6 queries now in
   `scorched_neo4j_schema.cypher`, itself rewritten in this pass — it was
   still on the old fabricated Escrow/Milestone/Dispute node model before).
2. **Stalled job detection** — jobs stuck in `Funded`/`Submitted` past
   their on-chain `expiredAt` without settling. Straight SQL against
   `jobs`, no graph traversal needed.
3. **Reputation reversal detection** — an agent's last-10-feedback average
   dropping 30%+ below their lifetime average, which a single aggregate
   score hides (an agent great for 200 jobs that's gone bad in the last 5
   still shows a high lifetime average).

Results land in a new `risk_alerts` table (`schema_erc8004_8183.sql`) and
publish to Redis (`alerts:<agentId>` / `alerts:*`) for the `newRiskAlert`
GraphQL subscription — this one, unlike `jobStatusChanged`/`newFeedback`,
is fully wired end-to-end (publish side included), not just structurally
present.

**Caveats to be upfront about:**
- Circular hiring detection is a *signal*, not proof of fraud — legitimate
  agents can occasionally hire each other back and forth for real reasons.
  Treat high-severity alerts as "worth a human look," not an automatic
  fraud verdict.
- The reputation-drop threshold (30% below lifetime average, 15+ feedback
  minimum) is a reasonable starting heuristic, not a tuned/validated one —
  expect false positives/negatives until it's run against real data and
  adjusted.
- `related_agent_ids` is stored as `NUMERIC[]` via `pq.Array` on a
  `[]string` — Postgres handles the implicit text→numeric cast on insert,
  but this wasn't tested against a live Postgres instance (no DB available
  in this sandbox); double check on first real run.

## Found while building this — a structural gap, now fixed

`docker-compose.yml` has always expected `./indexer/`, `./api/`, and
`./frontend/` build contexts, each with its own `Dockerfile` — but none of
those subdirectories, Dockerfiles, or `go.mod` files ever existed; every
file was flat in one directory since the original upload, and no
Dockerfile was ever among the original 15 files despite the original pitch
text claiming one existed. `docker-compose up` would have failed at the
build step regardless of anything else in this audit.

**Fixed:** files moved into `indexer/` (`scorched_indexer.go` +
`scorched_riskscan.go` — one binary, since `scorched_riskscan.go` needs
the indexer's live DB/Neo4j/Redis connections), `api/`
(`scorched_api.go` + `schema.graphqls`), and `frontend/src/`
(`SupplyChainVisualizer.tsx`/`.css`). Added a `Dockerfile` and best-effort
`go.mod` to both `indexer/` and `api/`.

**Caveat:** the `go.mod` files list plausible dependency versions but were
never run through `go mod tidy` — no Go toolchain was available in this
sandbox. Run `go mod tidy` in each directory before building; it'll
correct anything wrong. While adding the missing base import, I also
caught one real compile bug this way: `scorched_api.go` used
`graphql.ExecutableSchema` without importing the base
`github.com/99designs/gqlgen/graphql` package (only its `handler`/
`transport`/`playground` subpackages were imported) — fixed.

**Also fixed:** `docker-compose.yml`'s Postgres/Neo4j volume mounts were
pointing at `./schema.sql` and `./neo4j_schema.cypher` — filenames that
never matched any actual file in this project (the real files are
`schema_erc8004_8183.sql`/`scorched_postgresql_schema_OLD_UNUSED.sql` and
`scorched_neo4j_schema.cypher`). This was broken from the very first
version of this project, before any of the earlier audit passes — now
pointed at the real, current filenames, and `seed_test_data.sql` is now
also auto-loaded on Postgres init.

**Frontend scaffold — completed.** Added `package.json` (Vite + React 18 +
Apollo Client + graphql-ws + d3), `vite.config.ts`, `tsconfig.json`,
`index.html`, `src/main.tsx`, `src/App.tsx`, and `src/App.css`. `App.tsx`
wires up Apollo Client with a split HTTP/WebSocket link (subscriptions
over `graphql-ws`, everything else over HTTP), a simple agent-ID search
box driving `SupplyChainVisualizer`, and a `RiskAlertsPanel` that polls
`/api/v1/risk-alerts` every 30s — putting Scorched's actual differentiator
on the landing page instead of behind a query someone has to know to ask
for. Added a matching `Dockerfile` (Vite build → `serve` on port 3000,
matching what `docker-compose.yml`/`nginx.conf` already expected).

**Caveats:**
- `npm install` was never run — no network access in this sandbox to fetch
  packages — so, same as the Go `go.mod` files, dependency versions are
  best-effort and unverified. Run `npm install` yourself before building;
  it'll surface anything wrong.
- The Dockerfile bakes `REACT_APP_*`/`VITE_*` env vars in at Vite's build
  time, but `docker-compose.yml` passes them as container runtime env
  vars — Vite's static build won't see runtime changes to them. Fine for
  a hackathon demo (rebuild the image if you change API URLs), but a real
  deployment should switch to a small runtime-config-injection pattern
  instead.
- `.env.example`'s `REACT_APP_API_URL`/`REACT_APP_WS_URL` are bare origins
  (`http://localhost:8080`, no path) — `App.tsx` appends `/graphql` itself
  for the GraphQL endpoint and uses the bare origin directly for REST
  calls. Don't add `/graphql` to those env vars or you'll double it up.



## Verified as correct
- Chain ID 196 (mainnet) / 1952 (testnet)
- HTTP RPC `https://rpc.xlayer.tech`
- SQL query builder in `Agents` resolver — properly parameterized, no
  injection risk despite the dynamic `fmt.Sprintf` (only placeholder
  numbers are interpolated, never user data)
- Schema design and event-handling logic are otherwise solid
