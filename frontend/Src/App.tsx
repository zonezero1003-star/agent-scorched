import React, { useEffect, useState } from 'react';
import {
  ApolloClient,
  ApolloProvider,
  InMemoryCache,
  HttpLink,
  split,
} from '@apollo/client';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { getMainDefinition } from '@apollo/client/utilities';
import { createClient } from 'graphql-ws';
import { SupplyChainVisualizer } from './SupplyChainVisualizer';
import './App.css';

// ============================================================
// API endpoint config
// docker-compose sets REACT_APP_WS_URL (see .env.example) — vite.config.ts
// re-exposes REACT_APP_* alongside VITE_* so that name doesn't need
// renaming. There's no separate REACT_APP_API_URL in .env.example today;
// add one (and read it here) if the API isn't on the same host as this
// frontend's dev/proxy setup. Defaulting to localhost:8080 for local dev.
// ============================================================

// docker-compose (see .env.example / docker-compose.yml) sets
// REACT_APP_API_URL to a bare origin like "http://localhost:8080" (no
// path) and REACT_APP_WS_URL similarly as "ws://localhost:8080" — the
// GraphQL path is appended here, not baked into the env var, so both the
// REST base and the GraphQL endpoint can be derived from the same origin.
const API_ORIGIN = (import.meta as any).env?.REACT_APP_API_URL || 'http://localhost:8080';
const WS_ORIGIN = (import.meta as any).env?.REACT_APP_WS_URL || 'ws://localhost:8080';

const HTTP_URL = `${API_ORIGIN}/graphql`;
const WS_URL = `${WS_ORIGIN}/graphql`;
const REST_BASE = API_ORIGIN;

const httpLink = new HttpLink({ uri: HTTP_URL });

const wsLink = new GraphQLWsLink(
  createClient({
    url: WS_URL,
  })
);

const splitLink = split(
  ({ query }) => {
    const definition = getMainDefinition(query);
    return (
      definition.kind === 'OperationDefinition' &&
      definition.operation === 'subscription'
    );
  },
  wsLink,
  httpLink
);

const client = new ApolloClient({
  link: splitLink,
  cache: new InMemoryCache(),
});

// ============================================================
// Risk alerts panel — surfaces Scorched's differentiator (see AUDIT.md)
// directly on the landing page rather than burying it behind a query a
// user has to know to ask for. Uses the REST endpoint for simplicity;
// the GraphQL `riskAlerts` query / `newRiskAlert` subscription exist too
// if you want to wire this through Apollo instead.
// ============================================================

interface RiskAlert {
  id: number;
  kind: string;
  agentId?: string;
  jobId?: string;
  severity: string;
  details: string;
  detectedAt: string;
}

const RiskAlertsPanel: React.FC = () => {
  const [alerts, setAlerts] = useState<RiskAlert[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    const fetchAlerts = async () => {
      try {
        const res = await fetch(`${REST_BASE}/api/v1/risk-alerts?first=10`);
        if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
        const data = await res.json();
        if (!cancelled) {
          setAlerts(data.alerts || []);
          setError(null);
        }
      } catch (err: any) {
        if (!cancelled) setError(err.message);
      } finally {
        if (!cancelled) setLoading(false);
      }
    };

    fetchAlerts();
    const interval = setInterval(fetchAlerts, 30000);
    return () => {
      cancelled = true;
      clearInterval(interval);
    };
  }, []);

  return (
    <div className="risk-alerts-panel">
      <h2>Live Risk Alerts</h2>
      <p className="risk-alerts-subtitle">
        Circular hiring, stalled jobs, and reputation reversals — detected automatically, not something you have to go looking for.
      </p>
      {loading && <div className="risk-alerts-status">Loading...</div>}
      {error && <div className="risk-alerts-status risk-alerts-error">Could not reach API: {error}</div>}
      {!loading && !error && alerts.length === 0 && (
        <div className="risk-alerts-status">No alerts yet — the scanner runs every 5 minutes.</div>
      )}
      <ul className="risk-alerts-list">
        {alerts.map((a) => (
          <li key={a.id} className={`risk-alert-item risk-alert-${a.severity}`}>
            <span className="risk-alert-kind">{a.kind.replace('_', ' ')}</span>
            <span className="risk-alert-severity">{a.severity}</span>
            <span className="risk-alert-target">
              {a.agentId ? `Agent #${a.agentId}` : a.jobId ? `Job #${a.jobId}` : ''}
            </span>
            <span className="risk-alert-time">{new Date(a.detectedAt).toLocaleString()}</span>
          </li>
        ))}
      </ul>
    </div>
  );
};

// ============================================================
// App shell — agent search + supply chain view + risk alerts
// ============================================================

const App: React.FC = () => {
  const [agentIdInput, setAgentIdInput] = useState('');
  const [activeAgentId, setActiveAgentId] = useState<string | null>(null);

  const handleSearch = (e: React.FormEvent) => {
    e.preventDefault();
    if (agentIdInput.trim()) {
      setActiveAgentId(agentIdInput.trim());
    }
  };

  return (
    <ApolloProvider client={client}>
      <div className="app-shell">
        <header className="app-header">
          <h1>Scorched</h1>
          <p className="app-tagline">X Layer's agent commerce watchdog — not just another agent lookup.</p>
        </header>

        <form className="agent-search" onSubmit={handleSearch}>
          <input
            type="text"
            placeholder="Enter an agent ID (e.g. 8841)"
            value={agentIdInput}
            onChange={(e) => setAgentIdInput(e.target.value)}
          />
          <button type="submit">View Supply Chain</button>
        </form>

        <main className="app-main">
          {activeAgentId ? (
            <SupplyChainVisualizer agentId={activeAgentId} />
          ) : (
            <div className="app-empty-state">
              <p>Search an agent ID above to trace its supply chain, or browse live risk alerts below.</p>
            </div>
          )}

          <RiskAlertsPanel />
        </main>
      </div>
    </ApolloProvider>
  );
};

export default App;
