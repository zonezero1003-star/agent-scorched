# Scorched — The Commerce Explorer for the X Layer Agent Economy

> **Etherscan for AI Agents.** A visual, AI-native block explorer re-architected around ERC-8004 identities and the Agent Payments Protocol (APP). Instead of tracking token transfers, Scorched tracks the full agent commerce lifecycle: **quoting → escrow → metering → settlement → dispute → reputation update**.

[![X Layer](https://img.shields.io/badge/Powered%20by-X%20Layer-3b82f6)](https://web3.okx.com/xlayer)
[![OKX.AI](https://img.shields.io/badge/OKX.AI-ASP%20Listed-f59e0b)](https://okx.ai)

---

## What Problem Does This Solve?

| Traditional Explorer | Scorched |
|---|---|
| Shows wallet addresses | Shows agent identities with roles, skills, reputation |
| Tracks token transfers | Tracks hiring, subcontracting, dispute resolution |
| Static transaction list | Real-time supply chain visualization |
| No trust mechanism | Crypto-economic reputation with slashable bonds |
| Human-only consumption | Dual API: humans browse, agents query before hiring |

**The killer insight:** In an agent-to-agent economy, "who should I hire?" is the most important question. Scorched answers it with on-chain evidence, not marketing copy.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        FRONTEND                              │
│  React + D3.js Supply Chain Visualizer + Real-time WS       │
└─────────────────────────────────────────────────────────────┘
                              │
┌─────────────────────────────────────────────────────────────┐
│                      API SERVER (Go)                         │
│  GraphQL + REST + WebSocket • Rate Limited • CORS Ready     │
└─────────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┼─────────────────────┐
        ▼                     ▼                     ▼
┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│  PostgreSQL  │    │    Neo4j     │    │    Redis     │
│  (Events +   │    │  (Graph:     │    │  (Cache +    │
│   State)     │    │  Supply      │    │   Pub/Sub +  │
│              │    │  Chains,     │    │   Checkpoint)│
│              │    │  Reputation) │    │              │
└──────────────┘    └──────────────┘    └──────────────┘
        ▲                     ▲                     ▲
        └─────────────────────┼─────────────────────┘
                              │
┌─────────────────────────────────────────────────────────────┐
│                     INDEXER (Go)                             │
│  WebSocket + Backfill • ABI Decode • Dual Write (SQL+Graph) │
│  X Layer RPC: https://rpc.xlayer.tech                       │
└─────────────────────────────────────────────────────────────┘
```

---

## Core Features

### 1. Agent Identity & Verification
- Search by ERC-8004 ID or wallet address
- On-chain identity NFT view with role badges
- Service catalog with OpenAPI specs
- Bond/stake display (OKB)
- Verification badges (code audited, KYC'd)

### 2. Reputation & Trust Dashboard
- Dynamic score: **X/5 (N completed jobs, N disputes, N slashed)**
- Derived from escrow history, dispute outcomes, slash events
- Historical performance trends
- Peer review network
- Composite "Agent Health Score"

### 3. Agent Performance Dashboard
- Total transactions, success rate %, average response time
- Revenue generated (lifetime / monthly / daily)
- Active subscriptions, uptime %, error rates by service

### 4. Agent-to-Agent Interaction Graph
- Visual "social graph" of the agent economy
- Value flow between agents (OKB/USDC via APP)
- Dependency mapping and cluster analysis
- Full network graph with D3.js force simulation

### 5. Agent Activity Feed
- Real-time on-chain activity: hires, payments, completions
- Verdicts from dispute oracles
- Cryptoeconomic staking / TEE attestations
- Full A2A call context: input/output, gas cost, execution time

### 6. Revenue Analytics
- Real-time revenue tracking per agent
- Breakdown by service type, pricing model
- Leaderboard: top-earning agents by category
- Revenue trends and projections

### 7. Agent Discovery & Search
- Search by capability, filter by price/success rate/response time
- "Trending agents" and "New agents" sections
- Category browsing (DeFi, Oracle, Security, etc.)

### 8. Supply Chain Visualizer ⭐ THE KILLER FEATURE
Trace any agent's output back through its **entire** value chain:
- Which sub-agents were hired
- What each was paid
- How long each step took
- Where disputes occurred

**Positioning: Etherscan + LinkedIn + Upwork + Dune, in one view.**

---

## Agent-to-Agent Integration

Scorched is itself an ASP on OKX.AI:

```
Recruiter Agent → "Find me a code audit agent with >4.5 reputation"
                ↓
         Scorched API
                ↓
    Returns ranked list + signed attestation
                ↓
    Recruiter hires via x402 / APP escrow
```

Scorched backs its attestations with its own OKB bond. If it lies, its bond gets slashed.

---

## Tech Stack

| Layer | Technology |
|---|---|
| Blockchain | X Layer (OP Stack optimistic rollup, Chain ID: 196) |
| Indexer | Go + go-ethereum |
| API | Go + gqlgen + chi |
| Frontend | React 18 + TypeScript + D3.js + Apollo Client |
| Relational DB | PostgreSQL 16 |
| Graph DB | Neo4j 5.15 + APOC + GDS |
| Cache/PubSub | Redis 7 |
| Monitoring | Prometheus + Grafana |
| Proxy | Nginx |
| Deployment | Docker Compose |

---

## Quick Start

### Prerequisites
- Docker & Docker Compose
- 8GB RAM minimum (Neo4j is memory-hungry)
- X Layer RPC access (public: `https://rpc.xlayer.tech`)

### 1. Clone & Configure
```bash
git clone https://github.com/your-org/scorched.git
cd scorched
cp .env.example .env
# Edit .env with your settings
```

### 2. Launch Infrastructure
```bash
docker-compose up -d postgres neo4j redis
# Wait for health checks
```

### 3. Initialize Schema
```bash
# PostgreSQL (auto-runs via docker-entrypoint)
# Neo4j seeding
docker exec -it scorched-neo4j cypher-shell -u neo4j -p scorched_graph_2024 < neo4j_schema.cypher
```

### 4. Start Indexer
```bash
docker-compose up -d indexer
# Tails X Layer events in real-time
```

### 5. Start API + Frontend
```bash
docker-compose up -d api frontend nginx
```

### 6. Access
- **Frontend:** http://localhost
- **GraphQL Playground:** http://localhost/graphql
- **Neo4j Browser:** http://localhost:7474
- **Grafana:** http://localhost:3001

---

## X Layer Network Information

| Parameter | Value |
|---|---|
| **Chain ID** | 196 (mainnet), 1952 (testnet) |
| **RPC (HTTPS)** | `https://rpc.xlayer.tech` or `https://xlayerrpc.okx.com` |
| **RPC (WSS)** | `wss://xlayerws.okx.com` or `wss://ws.xlayer.tech` |
| **Rate limit** | 100 req/s per IP on public RPC |
| **Explorer** | [OKLink X Layer](https://www.oklink.com/xlayer) |
| **Bridge** | [X Layer Bridge](https://www.okx.com/xlayer/bridge) |
| **Gas Token** | OKB |
| **EVM Compatible** | Yes — full EVM equivalence |
| **Architecture** | Optimism (OP) Stack, optimistic rollup with a 7-day L1 challenge window for withdrawals |

> **Verify before relying on this in production.** X Layer's stack and finality
> characteristics have changed over time (it previously ran on Polygon CDK).
> Check https://web3.okx.com/xlayer/docs and https://status.xlayer.tech for the
> current architecture and any stated finality/confirmation guidance rather than
> assuming a fixed number — see `AUDIT.md`.

### Key Contracts

| Contract | Address |
|---|---|
| ERC-8004 Registry | `0x0000...00008004` |
| APP Protocol | `0x0000...0000APP0` |
| OKB Token | `0x7523...2a86c` |
| USDC | `0x74b7...26d22` |

---

## API Reference

### GraphQL Endpoint
```graphql
POST /graphql
Content-Type: application/json

{
  agent(id: "0x71c...") {
    id
    reputationScore
    totalJobsCompleted
    services { name basePrice }
    supplyChain(escrowId: "0x...") {
      nodes { agent { id } service { name } }
      edges { from { id } to { id } amount currency }
    }
  }
}
```

### REST Endpoints (A2MCP Compatible)
```
GET /api/v1/agents?role=Provider&minReputation=4.0&tag=security
GET /api/v1/agents/{id}
GET /api/v1/escrows/{id}
GET /api/v1/supply-chain/{escrowId}
GET /api/v1/leaderboard?category=defi&first=20
```

### WebSocket Subscriptions
```graphql
subscription {
  escrowUpdated(id: "0x...") { status totalReleased }
  agentHeartbeat(id: "0x...") { lastHeartbeatAt }
}
```

---

## Hackathon Submission

### Prize Category
**Best Product** or **Software Utility**

### Why This Wins
1. **Solves a real problem:** Agent trust and discovery is the #1 blocker for A2A commerce
2. **Native to X Layer:** Exploits sub-cent fees for dense reputation graphs impossible on Ethereum
3. **Real usage:** Every agent query generates on-chain attestation fees → proven demand
4. **Social traction:** Supply chain visualizations are highly shareable
5. **Infrastructure layer:** Other hackathon projects can build ON TOP of Scorched

### Demo Script
```
1. Show Agent Profile: "Agent #8841, 4.7/5, 312 jobs, 0 slashes"
2. Show Supply Chain: "This market report was built by 3 sub-agents, $240 total"
3. Show Real-time: "Watch this escrow resolve live via WebSocket"
4. Show A2A: "My agent just queried Scorched before hiring"
5. Post on X: "Just traced an agent supply chain on X Layer. Every sub-agent, every payment, every dispute. This is Scorched. #okxai"
```

---

## License

MIT License — Built for the OKX AI Genesis Hackathon 2026

---

## Acknowledgments

- **X Layer** — Sub-cent fees make dense agent commerce graphs economically viable
- **OKX.AI** — The platform that makes agents discoverable and hireable
- **OP Stack** — The optimistic-rollup framework X Layer currently runs on (verify at build time — this has changed before)
- **ERC-8004** — The on-chain identity standard for autonomous agents
