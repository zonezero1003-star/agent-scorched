// ============================================================
// SCORCHED — Neo4j Graph Schema for X Layer Agent Commerce
// Rewritten to match the real ERC-8004 (identity) + ERC-8183 (job escrow)
// data model — see AUDIT.md/HANDOFF.md. No Service/Escrow/Milestone/
// Dispute/Quote/BondSlashEvent nodes: none of those concepts exist
// on-chain in either real standard. Only (:Agent) nodes and
// (:Agent)-[:HIRED]->(:Agent) edges (synced from the `jobs` table by
// scorched_indexer.go's runNeo4jSync), which is exactly what the risk
// detection queries below need.
// ============================================================

// ============================================================
// CONSTRAINTS & INDEXES
// ============================================================

CREATE CONSTRAINT agent_id_unique IF NOT EXISTS
FOR (a:Agent) REQUIRE a.agentId IS UNIQUE;

CREATE INDEX agent_feedback_count_idx IF NOT EXISTS
FOR (a:Agent) ON (a.feedbackCount);

CREATE INDEX agent_avg_feedback_idx IF NOT EXISTS
FOR (a:Agent) ON (a.avgFeedbackValue);

// ============================================================
// NODE TYPES
// ============================================================

// (:Agent) — ERC-8004 identity, synced from the `agents` + `agent_reputation`
// tables. Properties:
//   agentId: string (uint256 ERC-721 tokenId, as decimal string)
//   ownerAddress: string (0x-hex, lowercase)
//   agentURI: string
//   feedbackCount: int
//   avgFeedbackValue: float (nullable — no feedback yet)
//   firstSeenAt: datetime

// ============================================================
// RELATIONSHIP TYPES
// ============================================================

// (:Agent)-[:HIRED {jobId: string, budget: string, status: string, at: datetime}]->(:Agent)
// Synced from the `jobs` table (ERC-8183). One edge per job, client -> provider.
// status is one of: Open | Funded | Submitted | Completed | Rejected | Expired

// ============================================================
// RISK / FRAUD DETECTION QUERIES
// These back the riskReport / circularHiringDetection GraphQL resolvers
// in scorched_api.go. This is the actual differentiator over generic
// ERC-8004 explorers (8004scan.io, scorched.info) — they show a
// reputation number; these queries look for signs that number is being
// gamed, or that money is stuck.
// ============================================================

// R1 — Circular hiring detection (potential reputation-washing / collusion).
// A cluster of agents hiring each other in a closed loop can manufacture
// activity and feedback that looks organic but never touches a real
// external client. Flags any cycle of 2-8 HIRED edges back to the
// starting agent, restricted to jobs that were actually funded (budget > 0)
// since an unfunded Open job proves nothing.
// Params: none
MATCH path = (a:Agent)-[:HIRED*2..8]->(a)
WHERE ALL(r IN relationships(path) WHERE toFloat(r.budget) > 0)
WITH a, path,
     [n IN nodes(path) | n.agentId] AS cycleAgents,
     reduce(total = 0.0, r IN relationships(path) | total + toFloat(r.budget)) AS cycleValue,
     length(path) AS cycleLength
RETURN DISTINCT a.agentId AS rootAgent, cycleLength, cycleAgents, cycleValue
ORDER BY cycleValue DESC
LIMIT 100;

// R2 — Stalled jobs: funded or submitted, but the job's real-world
// deadline (expiredAt, synced onto the edge as `expiredAt` if you extend
// the sync — see note below) has passed without settlement. This graph
// alone doesn't carry expiredAt today; the practical version of this
// check runs in SQL against `jobs.expired_at` directly (see
// scorched_riskscan.go) rather than here, since it's a simple filter
// with no graph traversal need. Kept here as a reference for anyone who
// wants to extend the Neo4j sync to carry expiry data onto edges instead.

// R3 — Sudden reputation reversal: an agent with an established history
// of positive feedback whose most recent feedback trends sharply negative.
// This requires per-feedback time-series data that isn't summarized onto
// the Agent node (only the aggregate avgFeedbackValue is) — the practical
// version of this check also runs in SQL (see scorched_riskscan.go),
// comparing a recent window's average against the all-time average.

// R4 — Agent's "Trusted Network" — who they hire most frequently
// Params: $agentId
MATCH (a:Agent {agentId: $agentId})-[h:HIRED]->(provider:Agent)
WITH provider, count(h) AS hireCount, sum(toFloat(h.budget)) AS totalPaid, avg(toFloat(h.budget)) AS avgPayment
RETURN provider.agentId, provider.avgFeedbackValue, hireCount, totalPaid, avgPayment
ORDER BY hireCount DESC, totalPaid DESC
LIMIT 20;

// R5 — Network centrality: most-connected agents in the commerce graph
// (useful both for "who matters most" leaderboards and as a prioritization
// signal for risk scanning — high-centrality agents are worth watching
// closely since fraud there has outsized blast radius)
MATCH (a:Agent)
OPTIONAL MATCH (a)-[:HIRED]->(hired:Agent)
OPTIONAL MATCH (a)<-[:HIRED]-(hiredBy:Agent)
WITH a, count(DISTINCT hired) AS outDegree, count(DISTINCT hiredBy) AS inDegree
RETURN a.agentId, a.avgFeedbackValue, outDegree, inDegree, (outDegree + inDegree) AS networkCentrality
ORDER BY networkCentrality DESC
LIMIT 50;

// R6 — Value flow between agent clusters (large-value relationships worth
// a closer look, regardless of whether they're cyclical)
MATCH (a1:Agent)-[h:HIRED]->(a2:Agent)
WITH a1, a2, sum(toFloat(h.budget)) AS totalValue, count(h) AS jobCount
WHERE totalValue > 0
RETURN a1.agentId AS fromAgent, a2.agentId AS toAgent, totalValue, jobCount
ORDER BY totalValue DESC
LIMIT 100;
