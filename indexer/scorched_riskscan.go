package main

// ============================================================
// SCORCHED — Risk & Fraud Detection Scanner
//
// This is Scorched's actual differentiator vs. generic ERC-8004 explorers
// (8004scan.io, scorched.info) — see AUDIT.md for that market-research
// context. Those tools show a reputation number. This module actively
// looks for signs that number is being gamed, or that money is stuck,
// and pushes alerts rather than waiting for someone to search.
//
// Three checks, run periodically:
//   1. Circular hiring — agents hiring each other in a closed loop, which
//      can manufacture feedback/activity that looks organic but never
//      touches a real external client (reputation-washing / collusion).
//   2. Stalled jobs — funded or submitted past their on-chain expiry
//      without settling. Money that's stuck, not just slow.
//   3. Reputation reversal — an agent with an established positive
//      history whose recent feedback trends sharply negative, which a
//      single lifetime-average score would hide.
//
// Results are written to `risk_alerts` (schema_erc8004_8183.sql) and
// published to Redis so the API's `newRiskAlert` subscription (see
// scorched_api.go / HANDOFF.md) can push them live. This is intentionally
// its own file/concern, kept separate from the event-indexing logic in
// scorched_indexer.go, even though today it runs in the same binary and
// shares its DB/Neo4j/Redis connections — see AUDIT.md's note on the
// docker-compose / Dockerfile / directory-structure gap this project
// still has before these can build as truly separate services.
// ============================================================

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/lib/pq"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// riskScanLoop runs all three checks on a fixed interval. Call from
// Indexer.Start() alongside the other background loops.
func (idx *Indexer) riskScanLoop() {
	rlog := log.New(os.Stdout, "[RISKSCAN] ", log.LstdFlags)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run once immediately on startup rather than waiting for the first tick.
	idx.runAllRiskScans(rlog)

	for {
		select {
		case <-ticker.C:
			idx.runAllRiskScans(rlog)
		case <-idx.ctx.Done():
			return
		}
	}
}

func (idx *Indexer) runAllRiskScans(rlog *log.Logger) {
	if err := idx.scanCircularHiring(rlog); err != nil {
		rlog.Printf("circular hiring scan error: %v", err)
	}
	if err := idx.scanStalledJobs(rlog); err != nil {
		rlog.Printf("stalled job scan error: %v", err)
	}
	if err := idx.scanReputationDrops(rlog); err != nil {
		rlog.Printf("reputation drop scan error: %v", err)
	}
}

// --- Check 1: Circular hiring ---
// Runs the R1 Cypher query from scorched_neo4j_schema.cypher against the
// live graph. A cycle of funded jobs back to the starting agent is a
// structural red flag regardless of any individual agent's score.
func (idx *Indexer) scanCircularHiring(rlog *log.Logger) error {
	session := idx.neo4jDriver.NewSession(idx.ctx, neo4j.SessionConfig{DatabaseName: "neo4j"})
	defer session.Close(idx.ctx)

	result, err := session.ExecuteRead(idx.ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		records, err := tx.Run(idx.ctx, `
			MATCH path = (a:Agent)-[:HIRED*2..8]->(a)
			WHERE ALL(r IN relationships(path) WHERE toFloat(r.budget) > 0)
			WITH a, path,
			     [n IN nodes(path) | n.agentId] AS cycleAgents,
			     reduce(total = 0.0, r IN relationships(path) | total + toFloat(r.budget)) AS cycleValue,
			     length(path) AS cycleLength
			RETURN DISTINCT a.agentId AS rootAgent, cycleLength, cycleAgents, cycleValue
			ORDER BY cycleValue DESC
			LIMIT 100
		`, nil)
		if err != nil {
			return nil, err
		}
		return records.Collect(idx.ctx)
	})
	if err != nil {
		return fmt.Errorf("neo4j circular hiring query: %w", err)
	}

	records := result.([]*neo4j.Record)
	for _, rec := range records {
		rootAgent, _ := rec.Get("rootAgent")
		cycleAgentsRaw, _ := rec.Get("cycleAgents")
		cycleValue, _ := rec.Get("cycleValue")
		cycleLength, _ := rec.Get("cycleLength")

		cycleAgents, _ := cycleAgentsRaw.([]any)
		var agentIDs []string
		for _, a := range cycleAgents {
			if s, ok := a.(string); ok {
				agentIDs = append(agentIDs, s)
			}
		}

		dedupKey := fmt.Sprintf("circular_hiring:%s", joinSorted(agentIDs))
		severity := "medium"
		if cv, ok := cycleValue.(float64); ok && cv > 1e18 { // > ~1 token unit at 18 decimals
			severity = "high"
		}

		details := map[string]any{
			"cycleLength": cycleLength,
			"cycleValue":  cycleValue,
			"cycleAgents": agentIDs,
		}

		if err := idx.writeRiskAlert(rlog, "circular_hiring", rootAgent.(string), nil, agentIDs, severity, details, dedupKey); err != nil {
			rlog.Printf("write circular_hiring alert: %v", err)
		}
	}

	return nil
}

// --- Check 2: Stalled jobs ---
// A job that reached Funded or Submitted status but whose on-chain
// expiredAt has passed without a settlement event is money stuck in
// limbo — either the provider went dark, or the evaluator never acted.
func (idx *Indexer) scanStalledJobs(rlog *log.Logger) error {
	rows, err := idx.db.QueryContext(idx.ctx, `
		SELECT job_id, client, provider, budget, status, expired_at
		FROM jobs
		WHERE status IN ('Funded', 'Submitted')
		  AND expired_at < NOW()
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var jobID, client, budget, status string
		var provider sql.NullString
		var expiredAt time.Time

		if err := rows.Scan(&jobID, &client, &provider, &budget, &status, &expiredAt); err != nil {
			continue
		}

		daysStalled := int(time.Since(expiredAt).Hours() / 24)
		severity := "low"
		if daysStalled >= 7 {
			severity = "high"
		} else if daysStalled >= 2 {
			severity = "medium"
		}

		details := map[string]any{
			"status":      status,
			"budget":      budget,
			"expiredAt":   expiredAt,
			"daysStalled": daysStalled,
		}

		dedupKey := fmt.Sprintf("stalled_job:%s", jobID)
		var providerID *string
		if provider.Valid {
			providerID = &provider.String
		}

		if err := idx.writeRiskAlert(rlog, "stalled_job", "", &jobID, nil, severity, details, dedupKey); err != nil {
			rlog.Printf("write stalled_job alert: %v", err)
		}
		_ = providerID // reserved for a future join against agents.owner_address, see HANDOFF.md-style note below
	}

	return nil
}

// --- Check 3: Reputation reversal ---
// Compares each agent's average feedback value over the last 10 pieces
// of feedback against their all-time average. A sharp, recent decline
// is exactly the pattern a single lifetime-average score hides — an
// agent that was great for 200 jobs and has gone bad in the last 5 still
// shows a high overall average.
func (idx *Indexer) scanReputationDrops(rlog *log.Logger) error {
	rows, err := idx.db.QueryContext(idx.ctx, `
		WITH recent AS (
			SELECT agent_id,
			       AVG(value::float / POWER(10, value_decimals)) AS recent_avg,
			       COUNT(*) AS recent_count
			FROM (
				SELECT agent_id, value, value_decimals,
				       ROW_NUMBER() OVER (PARTITION BY agent_id ORDER BY created_at DESC) AS rn
				FROM feedback
				WHERE NOT is_revoked
			) ranked
			WHERE rn <= 10
			GROUP BY agent_id
		)
		SELECT r.agent_id, r.recent_avg, r.recent_count, a.avg_value, a.feedback_count
		FROM recent r
		JOIN agent_reputation a ON a.agent_id = r.agent_id
		WHERE a.feedback_count >= 15  -- established history required, or "recent" IS the whole history
		  AND r.recent_avg < a.avg_value * 0.7 -- recent trend at least 30% below lifetime average
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var agentID string
		var recentAvg, lifetimeAvg float64
		var recentCount, feedbackCount int64

		if err := rows.Scan(&agentID, &recentAvg, &recentCount, &lifetimeAvg, &feedbackCount); err != nil {
			continue
		}

		severity := "medium"
		dropRatio := 1 - (recentAvg / lifetimeAvg)
		if dropRatio >= 0.5 {
			severity = "high"
		}

		details := map[string]any{
			"recentAvg":     recentAvg,
			"lifetimeAvg":   lifetimeAvg,
			"recentCount":   recentCount,
			"feedbackCount": feedbackCount,
		}

		dedupKey := fmt.Sprintf("reputation_drop:%s:%d", agentID, feedbackCount)

		if err := idx.writeRiskAlert(rlog, "reputation_drop", agentID, nil, nil, severity, details, dedupKey); err != nil {
			rlog.Printf("write reputation_drop alert: %v", err)
		}
	}

	return nil
}

// writeRiskAlert inserts a new alert (idempotent on dedupKey — a scan
// re-detecting the same issue does not create duplicate rows) and
// publishes it to Redis for live subscriptions. agentID may be empty
// string (stalled_job alerts are keyed by job, not agent).
func (idx *Indexer) writeRiskAlert(rlog *log.Logger, kind string, agentID string, jobID *string, relatedAgentIDs []string, severity string, details map[string]any, dedupKey string) error {
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return err
	}

	var agentIDParam interface{}
	if agentID != "" {
		agentIDParam = agentID
	}
	var jobIDParam interface{}
	if jobID != nil {
		jobIDParam = *jobID
	}

	var relatedIDsParam interface{}
	if len(relatedAgentIDs) > 0 {
		relatedIDsParam = pq.Array(relatedAgentIDs)
	}

	res, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO risk_alerts (kind, agent_id, job_id, related_agent_ids, severity, details, detected_at, dedup_key)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), $7)
		ON CONFLICT (dedup_key) DO NOTHING
	`, kind, agentIDParam, jobIDParam, relatedIDsParam, severity, detailsJSON, dedupKey)
	if err != nil {
		return err
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		// Already alerted on this exact finding — don't re-publish noise.
		return nil
	}

	rlog.Printf("new %s alert (severity=%s): %s", kind, severity, dedupKey)

	payload, _ := json.Marshal(map[string]any{
		"kind":       kind,
		"agentId":    agentID,
		"jobId":      jobID,
		"severity":   severity,
		"details":    string(detailsJSON), // encoded as a string to match RiskAlert.Details (string) on the receiving end
		"detectedAt": time.Now(),
	})

	channel := "alerts:*"
	if agentID != "" {
		channel = "alerts:" + agentID
	}
	if err := idx.redisClient.Publish(idx.ctx, channel, payload).Err(); err != nil {
		rlog.Printf("publish alert to redis: %v", err)
	}
	// Always also publish to the wildcard channel so a "watch everything"
	// subscriber doesn't need to know agent IDs in advance.
	if channel != "alerts:*" {
		idx.redisClient.Publish(idx.ctx, "alerts:*", payload)
	}

	return nil
}

// joinSorted produces a stable, order-independent key from a cycle's agent
// IDs so the same cycle detected starting from a different node in the
// loop still dedups to one alert.
func joinSorted(ids []string) string {
	sorted := append([]string(nil), ids...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	out := ""
	for i, s := range sorted {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
