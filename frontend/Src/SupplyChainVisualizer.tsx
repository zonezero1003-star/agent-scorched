// ============================================================
// SCORCHED — React Supply Chain Visualizer
// Interactive D3.js + React graph for agent commerce tracing
// Rewritten to match the real ERC-8004 (identity/reputation) + ERC-8183
// (job escrow) data model — see AUDIT.md / HANDOFF.md. There is no
// "milestone" or "dispute" concept in either real standard, so those UI
// elements from the original fabricated version are gone: a Job has a
// single lifecycle (Open -> Funded -> Submitted -> Completed/Rejected/
// Expired), not staged milestones or multi-party disputes.
// ============================================================

import React, { useEffect, useRef, useState, useCallback } from 'react';
import * as d3 from 'd3';
import { useQuery, useSubscription } from '@apollo/client';
import { gql } from 'graphql-tag';
import './SupplyChainVisualizer.css';

// ============================================================
// GRAPHQL QUERIES
// ============================================================

const GET_SUPPLY_CHAIN = gql`
  query AgentSupplyChain($agentId: ID!, $depth: Int) {
    agentSupplyChain(agentId: $agentId, depth: $depth) {
      rootAgent {
        agentId
        ownerAddress
        agentUri
        feedbackCount
        avgFeedbackValue
      }
      depth
      edges {
        client {
          agentId
          ownerAddress
          feedbackCount
          avgFeedbackValue
        }
        provider {
          agentId
          ownerAddress
          feedbackCount
          avgFeedbackValue
        }
        job {
          jobId
          status
        }
        budget
        status
        at
      }
    }
  }
`;

const JOB_STATUS_SUBSCRIPTION = gql`
  subscription JobStatusChanged($jobId: ID!) {
    jobStatusChanged(jobId: $jobId) {
      jobId
      status
      budget
      updatedAt
    }
  }
`;

// ============================================================
// TYPES
// ============================================================

interface AgentNode {
  agentId: string;
  ownerAddress: string;
  agentUri?: string;
  feedbackCount: number;
  avgFeedbackValue?: number | null;
}

interface SupplyChainEdge {
  client: AgentNode;
  provider: AgentNode;
  job: { jobId: string; status: string };
  budget: string;
  status: string; // Open | Funded | Submitted | Completed | Rejected | Expired
  at: string;
}

interface SupplyChainData {
  agentSupplyChain: {
    rootAgent: AgentNode;
    depth: number;
    edges: SupplyChainEdge[];
  };
}

// ============================================================
// D3 GRAPH CONFIG
// ============================================================

const GRAPH_CONFIG = {
  width: 1200,
  height: 600,
  nodeRadius: 28,
  linkDistance: 180,
  chargeStrength: -800,
  colors: {
    // Colored by job status on the edge into/out of a node rather than an
    // agent "role" — ERC-8004 identity has no on-chain role field.
    Open: '#94a3b8',
    Funded: '#3b82f6',
    Submitted: '#8b5cf6',
    Completed: '#22c55e',
    Rejected: '#ef4444',
    Expired: '#64748b',
    default: '#64748b',
    link: '#475569',
    linkActive: '#3b82f6',
    text: '#f1f5f9',
    background: '#0f172a',
    panel: '#1e293b',
  },
};

// ============================================================
// COMPONENT
// ============================================================

export const SupplyChainVisualizer: React.FC<{ agentId: string }> = ({ agentId }) => {
  const svgRef = useRef<SVGSVGElement>(null);
  const [selectedNode, setSelectedNode] = useState<AgentNode | null>(null);
  const [selectedEdge, setSelectedEdge] = useState<SupplyChainEdge | null>(null);
  const [zoomTransform, setZoomTransform] = useState<d3.ZoomTransform>(d3.zoomIdentity);

  const { data, loading, error } = useQuery<SupplyChainData>(GET_SUPPLY_CHAIN, {
    variables: { agentId, depth: 3 },
    pollInterval: 5000,
  });

  // Subscribe to status changes for every job currently shown in the graph.
  // NOTE: the indexer does not yet publish to Redis on job status writes —
  // this subscription will connect but stay silent until that publish call
  // is added (see AUDIT.md/HANDOFF.md). Polling above keeps the view fresh
  // in the meantime.
  const firstJobId = data?.agentSupplyChain?.edges?.[0]?.job?.jobId;
  useSubscription(JOB_STATUS_SUBSCRIPTION, {
    variables: { jobId: firstJobId },
    skip: !firstJobId,
  });

  const renderGraph = useCallback(() => {
    if (!data?.agentSupplyChain || !svgRef.current) return;

    const { edges, rootAgent } = data.agentSupplyChain;
    const svg = d3.select(svgRef.current);
    svg.selectAll('*').remove();

    const width = GRAPH_CONFIG.width;
    const height = GRAPH_CONFIG.height;

    // Create zoom behavior
    const zoom = d3.zoom<SVGSVGElement, unknown>()
      .scaleExtent([0.1, 4])
      .on('zoom', (event) => {
        setZoomTransform(event.transform);
        g.attr('transform', event.transform.toString());
      });

    svg.call(zoom);

    const g = svg.append('g');

    // Build unique node list from every client/provider seen across edges
    const nodeById = new Map<string, AgentNode>();
    nodeById.set(rootAgent.agentId, rootAgent);
    edges.forEach((e) => {
      if (e.client?.agentId) nodeById.set(e.client.agentId, e.client);
      if (e.provider?.agentId) nodeById.set(e.provider.agentId, e.provider);
    });

    const simulationNodes: d3.SimulationNodeDatum[] = Array.from(nodeById.values()).map((n, i) => ({
      id: n.agentId,
      index: i,
      x: width / 2 + (Math.random() - 0.5) * 200,
      y: height / 2 + (Math.random() - 0.5) * 200,
      agent: n,
    }));

    const simulationLinks = edges
      .filter((e) => e.client?.agentId && e.provider?.agentId)
      .map((e) => ({
        source: e.client.agentId,
        target: e.provider.agentId,
        ...e,
      }));

    // Force simulation
    const simulation = d3.forceSimulation(simulationNodes)
      .force('link', d3.forceLink(simulationLinks).id((d: any) => d.id).distance(GRAPH_CONFIG.linkDistance))
      .force('charge', d3.forceManyBody().strength(GRAPH_CONFIG.chargeStrength))
      .force('center', d3.forceCenter(width / 2, height / 2))
      .force('collision', d3.forceCollide().radius(GRAPH_CONFIG.nodeRadius + 10));

    // Draw links
    const linkGroup = g.append('g').attr('class', 'links');

    const link = linkGroup.selectAll('line')
      .data(simulationLinks)
      .enter()
      .append('line')
      .attr('stroke', (d: any) => GRAPH_CONFIG.colors[d.status as keyof typeof GRAPH_CONFIG.colors] || GRAPH_CONFIG.colors.link)
      .attr('stroke-width', 2)
      .attr('stroke-opacity', 0.6)
      .attr('class', 'supply-chain-link')
      .on('click', (event, d) => {
        event.stopPropagation();
        setSelectedEdge(d as unknown as SupplyChainEdge);
        setSelectedNode(null);
      })
      .on('mouseover', function() {
        d3.select(this).attr('stroke', GRAPH_CONFIG.colors.linkActive).attr('stroke-width', 4);
      })
      .on('mouseout', function(_event, d: any) {
        d3.select(this).attr('stroke', GRAPH_CONFIG.colors[d.status as keyof typeof GRAPH_CONFIG.colors] || GRAPH_CONFIG.colors.link).attr('stroke-width', 2);
      });

    // Link labels (budget + status)
    const linkLabel = linkGroup.selectAll('.link-label')
      .data(simulationLinks)
      .enter()
      .append('g')
      .attr('class', 'link-label');

    linkLabel.append('rect')
      .attr('rx', 4)
      .attr('ry', 4)
      .attr('fill', GRAPH_CONFIG.colors.panel)
      .attr('stroke', GRAPH_CONFIG.colors.link)
      .attr('stroke-width', 1);

    linkLabel.append('text')
      .text((d: any) => `${formatAmount(d.budget)} · ${d.status}`)
      .attr('fill', GRAPH_CONFIG.colors.text)
      .attr('font-size', '11px')
      .attr('font-family', 'monospace')
      .attr('text-anchor', 'middle')
      .attr('dy', '0.35em');

    // Draw nodes
    const nodeGroup = g.append('g').attr('class', 'nodes');

    const node = nodeGroup.selectAll('g')
      .data(simulationNodes)
      .enter()
      .append('g')
      .attr('class', 'supply-chain-node')
      .style('cursor', 'pointer')
      .call(d3.drag<SVGGElement, any>()
        .on('start', (event, d) => {
          if (!event.active) simulation.alphaTarget(0.3).restart();
          d.fx = d.x;
          d.fy = d.y;
        })
        .on('drag', (event, d) => {
          d.fx = event.x;
          d.fy = event.y;
        })
        .on('end', (event, d) => {
          if (!event.active) simulation.alphaTarget(0);
          d.fx = null;
          d.fy = null;
        })
      )
      .on('click', (event, d: any) => {
        event.stopPropagation();
        setSelectedNode(d.agent as AgentNode);
        setSelectedEdge(null);
      });

    // Node circle — color/intensity driven by feedback count + avg value,
    // the only real on-chain reputation signal (ERC-8004 ReputationRegistry)
    node.append('circle')
      .attr('r', GRAPH_CONFIG.nodeRadius)
      .attr('fill', (d: any) => {
        const avg = d.agent.avgFeedbackValue ?? 0;
        return d3.interpolateRgb('#64748b', '#3b82f6')(Math.min(Math.max(avg, 0), 1));
      })
      .attr('stroke', (d: any) => {
        const count = d.agent.feedbackCount ?? 0;
        return count >= 50 ? '#22c55e' : count >= 5 ? '#eab308' : '#ef4444';
      })
      .attr('stroke-width', 3)
      .attr('class', 'node-circle');

    // Agent ID label (truncated)
    node.append('text')
      .text((d: any) => `#${d.agent.agentId}`)
      .attr('dy', -GRAPH_CONFIG.nodeRadius - 8)
      .attr('text-anchor', 'middle')
      .attr('fill', GRAPH_CONFIG.colors.text)
      .attr('font-size', '12px')
      .attr('font-weight', '600');

    // Feedback count badge (replaces the old "role" label — no role exists
    // on-chain in ERC-8004)
    node.append('text')
      .text((d: any) => `${d.agent.feedbackCount ?? 0} reviews`)
      .attr('dy', GRAPH_CONFIG.nodeRadius + 16)
      .attr('text-anchor', 'middle')
      .attr('fill', GRAPH_CONFIG.colors.text)
      .attr('font-size', '10px')
      .attr('opacity', 0.8);

    // Avg feedback value badge (replaces the old 0-5 "reputationScore" —
    // ERC-8004 feedback values are open-ended int128s, not a fixed 0-5
    // scale, so this only renders when a value exists)
    node.append('circle')
      .attr('r', 10)
      .attr('cx', GRAPH_CONFIG.nodeRadius - 5)
      .attr('cy', -GRAPH_CONFIG.nodeRadius + 5)
      .attr('fill', (d: any) => {
        const count = d.agent.feedbackCount ?? 0;
        return count >= 50 ? '#22c55e' : count >= 5 ? '#eab308' : '#ef4444';
      });

    node.append('text')
      .text((d: any) => (d.agent.avgFeedbackValue != null ? d.agent.avgFeedbackValue.toFixed(1) : '—'))
      .attr('x', GRAPH_CONFIG.nodeRadius - 5)
      .attr('y', -GRAPH_CONFIG.nodeRadius + 5)
      .attr('text-anchor', 'middle')
      .attr('dy', '0.35em')
      .attr('fill', '#fff')
      .attr('font-size', '9px')
      .attr('font-weight', '700');

    // Update positions on tick
    simulation.on('tick', () => {
      link
        .attr('x1', (d: any) => d.source.x)
        .attr('y1', (d: any) => d.source.y)
        .attr('x2', (d: any) => d.target.x)
        .attr('y2', (d: any) => d.target.y);

      linkLabel.attr('transform', (d: any) => {
        const x = (d.source.x + d.target.x) / 2;
        const y = (d.source.y + d.target.y) / 2;
        return `translate(${x}, ${y})`;
      });

      linkLabel.selectAll('rect')
        .attr('x', function(this: SVGRectElement, d: any) {
          const text = d3.select(this.parentNode).select('text').node() as SVGTextElement;
          return -(text?.getBBox().width || 60) / 2 - 4;
        })
        .attr('y', -8)
        .attr('width', function(this: SVGRectElement) {
          const text = d3.select(this.parentNode).select('text').node() as SVGTextElement;
          return (text?.getBBox().width || 60) + 8;
        })
        .attr('height', 16);

      node.attr('transform', (d: any) => `translate(${d.x}, ${d.y})`);
    });

    // Cleanup
    return () => {
      simulation.stop();
    };
  }, [data]);

  useEffect(() => {
    const cleanup = renderGraph();
    return () => cleanup?.();
  }, [renderGraph]);

  // Helpers
  const truncateAddress = (addr?: string) => {
    if (!addr) return 'Unknown';
    return `${addr.slice(0, 6)}...${addr.slice(-4)}`;
  };

  // Budget is a raw on-chain uint256 amount (token's smallest unit); the
  // exact decimals depend on the payment token used for a given job, which
  // isn't captured by ERC-8183 events themselves — display raw with a
  // "(raw units)" caveat rather than assuming 6-decimal USDC as the old
  // fabricated version did.
  const formatAmount = (amount: string) => {
    const num = parseFloat(amount);
    if (num >= 1e9) return `${(num / 1e9).toFixed(2)}B raw`;
    if (num >= 1e6) return `${(num / 1e6).toFixed(2)}M raw`;
    if (num >= 1e3) return `${(num / 1e3).toFixed(2)}K raw`;
    return `${num.toFixed(0)} raw`;
  };

  if (loading) return <div className="visualizer-loading">Loading supply chain...</div>;
  if (error) return <div className="visualizer-error">Error: {error.message}</div>;
  if (!data?.agentSupplyChain) return <div className="visualizer-empty">No supply chain data</div>;

  const { rootAgent, depth, edges } = data.agentSupplyChain;

  return (
    <div className="supply-chain-visualizer">
      {/* Header Stats */}
      <div className="sc-header">
        <div className="sc-stat">
          <span className="sc-stat-label">Jobs Shown</span>
          <span className="sc-stat-value">{edges.length}</span>
        </div>
        <div className="sc-stat">
          <span className="sc-stat-label">Chain Depth</span>
          <span className="sc-stat-value">{depth} levels</span>
        </div>
        <div className="sc-stat">
          <span className="sc-stat-label">Root Agent</span>
          <span className="sc-stat-value">#{rootAgent.agentId}</span>
        </div>
        <div className="sc-stat">
          <span className="sc-stat-label">Root Reviews</span>
          <span className="sc-stat-value">{rootAgent.feedbackCount}</span>
        </div>
      </div>

      {/* Graph Canvas */}
      <div className="sc-canvas-container">
        <svg
          ref={svgRef}
          width={GRAPH_CONFIG.width}
          height={GRAPH_CONFIG.height}
          className="sc-svg"
        />

        {/* Zoom Controls */}
        <div className="sc-zoom-controls">
          <button onClick={() => {
            if (svgRef.current) {
              d3.select(svgRef.current).transition().call(
                d3.zoom<SVGSVGElement, unknown>().transform,
                zoomTransform.scale(zoomTransform.k * 1.2)
              );
            }
          }}>+</button>
          <button onClick={() => {
            if (svgRef.current) {
              d3.select(svgRef.current).transition().call(
                d3.zoom<SVGSVGElement, unknown>().transform,
                d3.zoomIdentity
              );
            }
          }}>⟲</button>
          <button onClick={() => {
            if (svgRef.current) {
              d3.select(svgRef.current).transition().call(
                d3.zoom<SVGSVGElement, unknown>().transform,
                zoomTransform.scale(zoomTransform.k / 1.2)
              );
            }
          }}>-</button>
        </div>
      </div>

      {/* Detail Panel */}
      <div className="sc-detail-panel">
        {selectedNode && (
          <div className="sc-node-detail">
            <h3>Agent Details</h3>
            <div className="sc-detail-row">
              <span>Agent ID:</span>
              <code>#{selectedNode.agentId}</code>
            </div>
            <div className="sc-detail-row">
              <span>Owner:</span>
              <code>{truncateAddress(selectedNode.ownerAddress)}</code>
            </div>
            <div className="sc-detail-row">
              <span>Feedback Count:</span>
              <span>{selectedNode.feedbackCount}</span>
            </div>
            {selectedNode.avgFeedbackValue != null && (
              <div className="sc-detail-row">
                <span>Avg Feedback Value:</span>
                <span className={`sc-reputation sc-reputation-${selectedNode.avgFeedbackValue >= 0.7 ? 'high' : selectedNode.avgFeedbackValue >= 0.4 ? 'medium' : 'low'}`}>
                  {selectedNode.avgFeedbackValue.toFixed(2)}
                </span>
              </div>
            )}
            {selectedNode.agentUri && (
              <div className="sc-detail-row">
                <span>Agent Card:</span>
                <a href={selectedNode.agentUri} target="_blank" rel="noopener noreferrer">
                  View registration ↗
                </a>
              </div>
            )}
            <button
              className="sc-hire-button"
              onClick={() => window.open(`https://8004scan.io/agents/xlayer/${selectedNode.agentId}`, '_blank')}
            >
              View on 8004scan ↗
            </button>
          </div>
        )}

        {selectedEdge && (
          <div className="sc-edge-detail">
            <h3>Job Details</h3>
            <div className="sc-detail-row">
              <span>Client:</span>
              <code>{truncateAddress(selectedEdge.client.ownerAddress)}</code>
            </div>
            <div className="sc-detail-row">
              <span>Provider:</span>
              <code>{truncateAddress(selectedEdge.provider.ownerAddress)}</code>
            </div>
            <div className="sc-detail-row">
              <span>Budget:</span>
              <span className="sc-amount">{formatAmount(selectedEdge.budget)}</span>
            </div>
            <div className="sc-detail-row">
              <span>Status:</span>
              <span className={`sc-status sc-status-${selectedEdge.status.toLowerCase()}`}>
                {selectedEdge.status}
              </span>
            </div>
            <div className="sc-detail-row">
              <span>Job:</span>
              <a href={`/jobs/${selectedEdge.job.jobId}`}>#{selectedEdge.job.jobId}</a>
            </div>
            <div className="sc-detail-row">
              <span>Created:</span>
              <span>{new Date(selectedEdge.at).toLocaleString()}</span>
            </div>
          </div>
        )}

        {!selectedNode && !selectedEdge && (
          <div className="sc-empty-state">
            <p>Click a node or edge to view details</p>
            <p className="sc-hint">Drag to pan • Scroll to zoom • Click to inspect</p>
          </div>
        )}
      </div>
    </div>
  );
};

export default SupplyChainVisualizer;
