// ============================================================
// SCORCHED — GraphQL API Server (Go)
// Serves humans and agents (A2MCP compatible)
// ============================================================

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/gorilla/websocket"
	"github.com/lib/pq"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/redis/go-redis/v9"
)

// ============================================================
// CONFIGURATION
// ============================================================

type APIConfig struct {
	Port              string
	PostgresDSN       string
	Neo4jURI          string
	Neo4jUser         string
	Neo4jPassword     string
	RedisAddr         string
	RedisPassword     string
	MaxComplexity     int
	EnablePlayground  bool
	CORSOrigins       []string
	RateLimitRPS      int
}

func LoadAPIConfig() *APIConfig {
	// CORS_ORIGINS: comma-separated list, e.g. "https://app.example.com,https://staging.example.com"
	// Defaults to localhost-only rather than "*" — a wildcard combined with
	// AllowCredentials:true is a real CSRF/credential-leak risk. Set explicitly
	// for your deployed frontend origin(s) before going to production.
	origins := strings.Split(getEnv("CORS_ORIGINS", "http://localhost:3000"), ",")

	rateLimit := 100
	if v, err := strconv.Atoi(getEnv("RATE_LIMIT_RPS", "100")); err == nil {
		rateLimit = v
	}

	return &APIConfig{
		Port:             getEnv("PORT", "8080"),
		PostgresDSN:      getEnv("POSTGRES_DSN", "postgres://scorched:scorched@localhost:5432/scorched?sslmode=disable"),
		Neo4jURI:         getEnv("NEO4J_URI", "bolt://localhost:7687"),
		Neo4jUser:        getEnv("NEO4J_USER", "neo4j"),
		Neo4jPassword:    getEnv("NEO4J_PASSWORD", "scorched"),
		RedisAddr:        getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:    getEnv("REDIS_PASSWORD", ""),
		MaxComplexity:    100,
		EnablePlayground: getEnv("ENABLE_PLAYGROUND", "true") == "true",
		CORSOrigins:      origins,
		RateLimitRPS:     rateLimit,
	}
}

// originAllowed checks a request Origin header against the configured allowlist.
func originAllowed(allowed []string, origin string) bool {
	for _, a := range allowed {
		if a == "*" || a == origin {
			return true
		}
	}
	return false
}

// simpleRateLimiter is a minimal per-IP token bucket. It avoids adding a new
// dependency for the hackathon build — swap for golang.org/x/time/rate or
// httprate in a real production deploy.
type simpleRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rps     int
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

func newSimpleRateLimiter(rps int) *simpleRateLimiter {
	return &simpleRateLimiter{buckets: make(map[string]*bucket), rps: rps}
}

func (rl *simpleRateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(rl.rps), lastSeen: now}
		rl.buckets[key] = b
	}
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens += elapsed * float64(rl.rps)
	if b.tokens > float64(rl.rps) {
		b.tokens = float64(rl.rps)
	}
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (rl *simpleRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.RemoteAddr
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			key = fwd
		}
		if !rl.allow(key) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ============================================================
// DATA ACCESS LAYER
// ============================================================

type DataSource struct {
	db          *sql.DB
	neo4jDriver neo4j.DriverWithContext
	redisClient *redis.Client
}

func NewDataSource(cfg *APIConfig) (*DataSource, error) {
	db, err := sql.Open("postgres", cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	neo4jDriver, err := neo4j.NewDriverWithContext(
		cfg.Neo4jURI,
		neo4j.BasicAuth(cfg.Neo4jUser, cfg.Neo4jPassword, ""),
	)
	if err != nil {
		return nil, fmt.Errorf("neo4j: %w", err)
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	})

	return &DataSource{
		db:          db,
		neo4jDriver: neo4jDriver,
		redisClient: redisClient,
	}, nil
}

// ============================================================
// GRAPHQL RESOLVERS
// ============================================================

// --- Types ---
// These map directly to schema_erc8004_8183.sql — see AUDIT.md/HANDOFF.md.

type Agent struct {
	AgentID          string             `json:"agentId"`
	OwnerAddress     string             `json:"ownerAddress"`
	AgentURI         string             `json:"agentUri"`
	Metadata         []*AgentMetadataEntry `json:"metadata"`
	FirstSeenAt      time.Time          `json:"firstSeenAt"`
	UpdatedAt        time.Time          `json:"updatedAt"`
	FeedbackCount    int64              `json:"feedbackCount"`
	AvgFeedbackValue *float64           `json:"avgFeedbackValue"`
}

type AgentMetadataEntry struct {
	MetadataKey   string    `json:"metadataKey"`
	MetadataValue []byte    `json:"metadataValue"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type Feedback struct {
	AgentID       string     `json:"agentId"`
	ClientAddress string     `json:"clientAddress"`
	FeedbackIndex int64      `json:"feedbackIndex"`
	Value         string     `json:"value"` // NUMERIC — kept as string to preserve precision/sign over JSON
	ValueDecimals int        `json:"valueDecimals"`
	Tag1          *string    `json:"tag1"`
	Tag2          *string    `json:"tag2"`
	Endpoint      *string    `json:"endpoint"`
	FeedbackURI   *string    `json:"feedbackUri"`
	FeedbackHash  *string    `json:"feedbackHash"`
	IsRevoked     bool       `json:"isRevoked"`
	TxHash        string     `json:"txHash"`
	BlockNumber   int64      `json:"blockNumber"`
	CreatedAt     time.Time  `json:"createdAt"`
}

type FeedbackResponse struct {
	Responder    string    `json:"responder"`
	ResponseURI  *string   `json:"responseUri"`
	ResponseHash *string   `json:"responseHash"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Validation struct {
	RequestHash      string     `json:"requestHash"`
	ValidatorAddress string     `json:"validatorAddress"`
	AgentID          string     `json:"agentId"`
	RequestURI       *string    `json:"requestUri"`
	Response         *int       `json:"response"`
	ResponseURI      *string    `json:"responseUri"`
	ResponseHash     *string    `json:"responseHash"`
	Tag              *string    `json:"tag"`
	RequestedAt      time.Time  `json:"requestedAt"`
	RespondedAt      *time.Time `json:"respondedAt"`
}

type Job struct {
	JobID            string     `json:"jobId"`
	ClientAddress    string     `json:"clientAddress"`
	ProviderAddress  *string    `json:"providerAddress"`
	EvaluatorAddress string     `json:"evaluatorAddress"`
	HookAddress      *string    `json:"hookAddress"`
	Budget           string     `json:"budget"`
	Status           string     `json:"status"` // Open|Funded|Submitted|Completed|Rejected|Expired
	ExpiredAt        time.Time  `json:"expiredAt"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type JobSubmission struct {
	Deliverable string    `json:"deliverable"`
	SubmittedAt time.Time `json:"submittedAt"`
}

type JobSettlement struct {
	Kind        string    `json:"kind"` // completed|rejected|expired
	Actor       *string   `json:"actor"`
	Reason      *string   `json:"reason"`
	TxHash      string    `json:"txHash"`
	BlockNumber int64     `json:"blockNumber"`
	CreatedAt   time.Time `json:"createdAt"`
}

type JobPayment struct {
	Kind        string    `json:"kind"` // payment_released|evaluator_fee|refund
	Recipient   string    `json:"recipient"`
	Amount      string    `json:"amount"`
	TxHash      string    `json:"txHash"`
	BlockNumber int64     `json:"blockNumber"`
	CreatedAt   time.Time `json:"createdAt"`
}

type SupplyChain struct {
	RootAgent *Agent             `json:"rootAgent"`
	Edges     []*SupplyChainEdge `json:"edges"`
	Depth     int                `json:"depth"`
}

type SupplyChainEdge struct {
	Client   *Agent    `json:"client"`
	Provider *Agent    `json:"provider"`
	Job      *Job      `json:"job"`
	Budget   string    `json:"budget"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
}

type LeaderboardEntry struct {
	Rank        int    `json:"rank"`
	Agent       *Agent `json:"agent"`
	MetricValue string `json:"metricValue"`
	MetricLabel string `json:"metricLabel"`
}

type RiskAlert struct {
	ID              int64     `json:"id"`
	Kind            string    `json:"kind"` // circular_hiring | stalled_job | reputation_drop
	AgentID         *string   `json:"agentId"`
	JobID           *string   `json:"jobId"`
	RelatedAgentIDs []string  `json:"relatedAgentIds"`
	Severity        string    `json:"severity"` // low | medium | high
	Details         string    `json:"details"`  // raw JSON
	DetectedAt      time.Time `json:"detectedAt"`
}

// Query: riskAlerts(agentId, kind, severity, pagination) — see AUDIT.md for
// why this exists: it's the thing generic ERC-8004 explorers don't do.
func (r *Resolver) RiskAlerts(ctx context.Context, args struct {
	AgentID  *string
	Kind     *string
	Severity *string
	First    *int
}) ([]*RiskAlert, error) {
	limit := 50
	if args.First != nil && *args.First > 0 && *args.First <= 200 {
		limit = *args.First
	}

	query := `
		SELECT id, kind, agent_id, job_id, related_agent_ids, severity, details, detected_at
		FROM risk_alerts WHERE 1=1
	`
	var params []interface{}
	paramIdx := 1

	if args.AgentID != nil {
		query += fmt.Sprintf(" AND agent_id = $%d", paramIdx)
		params = append(params, *args.AgentID)
		paramIdx++
	}
	if args.Kind != nil {
		query += fmt.Sprintf(" AND kind = $%d", paramIdx)
		params = append(params, *args.Kind)
		paramIdx++
	}
	if args.Severity != nil {
		query += fmt.Sprintf(" AND severity = $%d", paramIdx)
		params = append(params, *args.Severity)
		paramIdx++
	}

	query += fmt.Sprintf(" ORDER BY detected_at DESC LIMIT $%d", paramIdx)
	params = append(params, limit)

	rows, err := r.ds.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var alerts []*RiskAlert
	for rows.Next() {
		var a RiskAlert
		var agentID, jobID sql.NullString
		var relatedIDs pq.StringArray
		var detailsBytes []byte

		if err := rows.Scan(&a.ID, &a.Kind, &agentID, &jobID, &relatedIDs, &a.Severity, &detailsBytes, &a.DetectedAt); err != nil {
			continue
		}
		if agentID.Valid {
			a.AgentID = &agentID.String
		}
		if jobID.Valid {
			a.JobID = &jobID.String
		}
		a.RelatedAgentIDs = []string(relatedIDs)
		a.Details = string(detailsBytes)
		alerts = append(alerts, &a)
	}
	return alerts, nil
}

// Query: circularHiringDetection — convenience wrapper for RiskAlerts
// filtered to just this one kind, since it's the headline feature.
func (r *Resolver) CircularHiringDetection(ctx context.Context) ([]*RiskAlert, error) {
	kind := "circular_hiring"
	return r.RiskAlerts(ctx, struct {
		AgentID  *string
		Kind     *string
		Severity *string
		First    *int
	}{Kind: &kind})
}

// --- Resolver Implementation ---

type Resolver struct {
	ds *DataSource
}

func NewResolver(ds *DataSource) *Resolver {
	return &Resolver{ds: ds}
}

// Query: agent(agentId: ID!)
func (r *Resolver) Agent(ctx context.Context, args struct{ AgentID string }) (*Agent, error) {
	agentID, ok := new(big.Int).SetString(strings.TrimPrefix(args.AgentID, "0x"), 0)
	if !ok {
		// fall back to base-10 parse if it wasn't hex-prefixed
		var ok2 bool
		agentID, ok2 = new(big.Int).SetString(args.AgentID, 10)
		if !ok2 {
			return nil, fmt.Errorf("invalid agentId: %q", args.AgentID)
		}
	}

	var a Agent
	var avgValue sql.NullFloat64
	err := r.ds.db.QueryRowContext(ctx, `
		SELECT a.agent_id, a.owner_address, a.agent_uri, a.first_seen_at, a.updated_at,
		       COALESCE(rep.feedback_count, 0), rep.avg_value
		FROM agents a
		LEFT JOIN agent_reputation rep ON rep.agent_id = a.agent_id
		WHERE a.agent_id = $1
	`, agentID.String()).Scan(
		&a.AgentID, &a.OwnerAddress, &a.AgentURI, &a.FirstSeenAt, &a.UpdatedAt,
		&a.FeedbackCount, &avgValue,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if avgValue.Valid {
		a.AvgFeedbackValue = &avgValue.Float64
	}

	metadata, err := r.loadAgentMetadata(ctx, agentID.String())
	if err != nil {
		return nil, err
	}
	a.Metadata = metadata

	return &a, nil
}

func (r *Resolver) loadAgentMetadata(ctx context.Context, agentID string) ([]*AgentMetadataEntry, error) {
	rows, err := r.ds.db.QueryContext(ctx, `
		SELECT metadata_key, metadata_value, updated_at
		FROM agent_metadata WHERE agent_id = $1
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AgentMetadataEntry
	for rows.Next() {
		var m AgentMetadataEntry
		if err := rows.Scan(&m.MetadataKey, &m.MetadataValue, &m.UpdatedAt); err != nil {
			continue
		}
		out = append(out, &m)
	}
	return out, nil
}

func (r *Resolver) loadAgentFeedback(ctx context.Context, agentID string, limit int) ([]*Feedback, error) {
	rows, err := r.ds.db.QueryContext(ctx, `
		SELECT agent_id, client_address, feedback_index, value, value_decimals,
		       tag1, tag2, endpoint, feedback_uri, feedback_hash, is_revoked,
		       tx_hash, block_number, created_at
		FROM feedback WHERE agent_id = $1
		ORDER BY created_at DESC LIMIT $2
	`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Feedback
	for rows.Next() {
		var f Feedback
		if err := rows.Scan(&f.AgentID, &f.ClientAddress, &f.FeedbackIndex, &f.Value, &f.ValueDecimals,
			&f.Tag1, &f.Tag2, &f.Endpoint, &f.FeedbackURI, &f.FeedbackHash, &f.IsRevoked,
			&f.TxHash, &f.BlockNumber, &f.CreatedAt); err != nil {
			continue
		}
		out = append(out, &f)
	}
	return out, nil
}

// Query: agents(filter, pagination)
func (r *Resolver) Agents(ctx context.Context, args struct {
	Tags                []string
	MinAvgFeedbackValue *float64
	MinFeedbackCount    *int
	First               *int
}) ([]*Agent, error) {
	limit := 20
	if args.First != nil && *args.First > 0 && *args.First <= 100 {
		limit = *args.First
	}

	query := `
		SELECT a.agent_id, a.owner_address, a.agent_uri, a.first_seen_at, a.updated_at,
		       COALESCE(rep.feedback_count, 0), rep.avg_value
		FROM agents a
		LEFT JOIN agent_reputation rep ON rep.agent_id = a.agent_id
		WHERE 1=1
	`
	var params []interface{}
	paramIdx := 1

	if args.MinAvgFeedbackValue != nil {
		query += fmt.Sprintf(" AND rep.avg_value >= $%d", paramIdx)
		params = append(params, *args.MinAvgFeedbackValue)
		paramIdx++
	}
	if args.MinFeedbackCount != nil {
		query += fmt.Sprintf(" AND COALESCE(rep.feedback_count, 0) >= $%d", paramIdx)
		params = append(params, *args.MinFeedbackCount)
		paramIdx++
	}
	if len(args.Tags) > 0 {
		query += fmt.Sprintf(" AND EXISTS (SELECT 1 FROM feedback f WHERE f.agent_id = a.agent_id AND (f.tag1 = ANY($%d) OR f.tag2 = ANY($%d)))", paramIdx, paramIdx)
		params = append(params, pq.Array(args.Tags))
		paramIdx++
	}

	query += fmt.Sprintf(" ORDER BY COALESCE(rep.feedback_count, 0) DESC LIMIT $%d", paramIdx)
	params = append(params, limit)

	rows, err := r.ds.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		var a Agent
		var avgValue sql.NullFloat64
		if err := rows.Scan(&a.AgentID, &a.OwnerAddress, &a.AgentURI, &a.FirstSeenAt, &a.UpdatedAt,
			&a.FeedbackCount, &avgValue); err != nil {
			continue
		}
		if avgValue.Valid {
			a.AvgFeedbackValue = &avgValue.Float64
		}
		agents = append(agents, &a)
	}
	return agents, nil
}

// Query: job(jobId: ID!)
func (r *Resolver) Job(ctx context.Context, args struct{ JobID string }) (*Job, error) {
	jobID, ok := new(big.Int).SetString(strings.TrimPrefix(args.JobID, "0x"), 0)
	if !ok {
		var ok2 bool
		jobID, ok2 = new(big.Int).SetString(args.JobID, 10)
		if !ok2 {
			return nil, fmt.Errorf("invalid jobId: %q", args.JobID)
		}
	}

	var j Job
	var provider sql.NullString
	var hook sql.NullString
	err := r.ds.db.QueryRowContext(ctx, `
		SELECT job_id, client, provider, evaluator, hook, budget, status, expired_at, created_at, updated_at
		FROM jobs WHERE job_id = $1
	`, jobID.String()).Scan(
		&j.JobID, &j.ClientAddress, &provider, &j.EvaluatorAddress, &hook,
		&j.Budget, &j.Status, &j.ExpiredAt, &j.CreatedAt, &j.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if provider.Valid {
		j.ProviderAddress = &provider.String
	}
	if hook.Valid {
		j.HookAddress = &hook.String
	}

	return &j, nil
}

// Query: jobs(filter, pagination)
func (r *Resolver) Jobs(ctx context.Context, args struct {
	Status          *string
	ClientAddress   *string
	ProviderAddress *string
	First           *int
}) ([]*Job, error) {
	limit := 20
	if args.First != nil && *args.First > 0 && *args.First <= 100 {
		limit = *args.First
	}

	query := `
		SELECT job_id, client, provider, evaluator, hook, budget, status, expired_at, created_at, updated_at
		FROM jobs WHERE 1=1
	`
	var params []interface{}
	paramIdx := 1

	if args.Status != nil {
		query += fmt.Sprintf(" AND status = $%d", paramIdx)
		params = append(params, *args.Status)
		paramIdx++
	}
	if args.ClientAddress != nil {
		query += fmt.Sprintf(" AND client = $%d", paramIdx)
		params = append(params, strings.ToLower(*args.ClientAddress))
		paramIdx++
	}
	if args.ProviderAddress != nil {
		query += fmt.Sprintf(" AND provider = $%d", paramIdx)
		params = append(params, strings.ToLower(*args.ProviderAddress))
		paramIdx++
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", paramIdx)
	params = append(params, limit)

	rows, err := r.ds.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		var j Job
		var provider, hook sql.NullString
		if err := rows.Scan(&j.JobID, &j.ClientAddress, &provider, &j.EvaluatorAddress, &hook,
			&j.Budget, &j.Status, &j.ExpiredAt, &j.CreatedAt, &j.UpdatedAt); err != nil {
			continue
		}
		if provider.Valid {
			j.ProviderAddress = &provider.String
		}
		if hook.Valid {
			j.HookAddress = &hook.String
		}
		jobs = append(jobs, &j)
	}
	return jobs, nil
}

// Query: agentSupplyChain(agentId, depth) — who has this agent hired, and
// who hired them, via ERC-8183 jobs. Simplification: joins jobs.client /
// jobs.provider directly against agents.owner_address, which misses agents
// that pay from a separate `agentWallet` metadata key. See HANDOFF.md.
func (r *Resolver) AgentSupplyChain(ctx context.Context, args struct {
	AgentID string
	Depth   *int
}) (*SupplyChain, error) {
	root, err := r.Agent(ctx, struct{ AgentID string }{AgentID: args.AgentID})
	if err != nil || root == nil {
		return nil, err
	}

	rows, err := r.ds.db.QueryContext(ctx, `
		SELECT j.job_id, j.client, j.provider, j.budget, j.status, j.created_at,
		       ca.agent_id, pa.agent_id
		FROM jobs j
		LEFT JOIN agents ca ON ca.owner_address = j.client
		LEFT JOIN agents pa ON pa.owner_address = j.provider
		WHERE j.client = $1 OR j.provider = $1
		ORDER BY j.created_at DESC
		LIMIT 200
	`, root.OwnerAddress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sc := &SupplyChain{RootAgent: root, Depth: 1}
	for rows.Next() {
		var jobID, client, provider, budget, status string
		var createdAt time.Time
		var clientAgentID, providerAgentID sql.NullString

		if err := rows.Scan(&jobID, &client, &provider, &budget, &status, &createdAt,
			&clientAgentID, &providerAgentID); err != nil {
			continue
		}

		edge := &SupplyChainEdge{
			Job:      &Job{JobID: jobID, ClientAddress: client, ProviderAddress: &provider, Budget: budget, Status: status},
			Budget:   budget,
			Status:   status,
			At:       createdAt,
			Client:   &Agent{OwnerAddress: client},
			Provider: &Agent{OwnerAddress: provider},
		}
		if clientAgentID.Valid {
			edge.Client.AgentID = clientAgentID.String
		}
		if providerAgentID.Valid {
			edge.Provider.AgentID = providerAgentID.String
		}
		sc.Edges = append(sc.Edges, edge)
	}

	return sc, nil
}

// Query: topAgents / leaderboard — ranked by feedback count (the only
// on-chain reputation signal that actually exists in ERC-8004; there is no
// "revenue" or "job completion" figure attached to identity, only to jobs).
func (r *Resolver) TopAgents(ctx context.Context, args struct {
	ByMetric *string
	First    *int
}) ([]*Agent, error) {
	limit := 20
	if args.First != nil && *args.First > 0 {
		limit = *args.First
	}

	rows, err := r.ds.db.QueryContext(ctx, `
		SELECT a.agent_id, a.owner_address, a.agent_uri, a.first_seen_at, a.updated_at,
		       COALESCE(rep.feedback_count, 0), rep.avg_value
		FROM agents a
		LEFT JOIN agent_reputation rep ON rep.agent_id = a.agent_id
		ORDER BY COALESCE(rep.feedback_count, 0) DESC, rep.avg_value DESC NULLS LAST
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		var a Agent
		var avgValue sql.NullFloat64
		if err := rows.Scan(&a.AgentID, &a.OwnerAddress, &a.AgentURI, &a.FirstSeenAt, &a.UpdatedAt,
			&a.FeedbackCount, &avgValue); err != nil {
			continue
		}
		if avgValue.Valid {
			a.AvgFeedbackValue = &avgValue.Float64
		}
		agents = append(agents, &a)
	}
	return agents, nil
}

// ============================================================
// SUBSCRIPTIONS (WebSocket)
// ============================================================

type SubscriptionResolver struct {
	ds *DataSource
}

// JobStatusChanged streams job status transitions over Redis pub/sub.
// The indexer (scorched_indexer.go) is responsible for publishing to
// "job:<jobId>" whenever it writes a status change — that publish call
// is NOT yet added to the indexer; see HANDOFF.md follow-up list.
func (sr *SubscriptionResolver) JobStatusChanged(ctx context.Context, args struct{ JobID string }) (<-chan *Job, error) {
	ch := make(chan *Job, 1)
	pubsub := sr.ds.redisClient.Subscribe(ctx, "job:"+args.JobID)

	go func() {
		defer close(ch)
		defer pubsub.Close()

		for msg := range pubsub.Channel() {
			var job Job
			if err := json.Unmarshal([]byte(msg.Payload), &job); err == nil {
				select {
				case ch <- &job:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// NewFeedback streams new ReputationRegistry feedback for a given agent
// (or all agents if agentId is empty). Same caveat as above — the indexer
// needs a matching publish call on "feedback:<agentId>" / "feedback:*".
func (sr *SubscriptionResolver) NewFeedback(ctx context.Context, args struct{ AgentID *string }) (<-chan *Feedback, error) {
	ch := make(chan *Feedback, 1)
	channel := "feedback:*"
	if args.AgentID != nil && *args.AgentID != "" {
		channel = "feedback:" + *args.AgentID
	}
	pubsub := sr.ds.redisClient.Subscribe(ctx, channel)

	go func() {
		defer close(ch)
		defer pubsub.Close()

		for msg := range pubsub.Channel() {
			var fb Feedback
			if err := json.Unmarshal([]byte(msg.Payload), &fb); err == nil {
				select {
				case ch <- &fb:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// NewRiskAlert streams risk/fraud findings as scorched_riskscan.go detects
// them. This one, unlike JobStatusChanged/NewFeedback above, is fully wired
// end-to-end — writeRiskAlert() in scorched_riskscan.go already publishes
// to exactly the "alerts:<agentId>" / "alerts:*" channels this subscribes to.
func (sr *SubscriptionResolver) NewRiskAlert(ctx context.Context, args struct{ AgentID *string }) (<-chan *RiskAlert, error) {
	ch := make(chan *RiskAlert, 1)
	channel := "alerts:*"
	if args.AgentID != nil && *args.AgentID != "" {
		channel = "alerts:" + *args.AgentID
	}
	pubsub := sr.ds.redisClient.Subscribe(ctx, channel)

	go func() {
		defer close(ch)
		defer pubsub.Close()

		for msg := range pubsub.Channel() {
			var alert RiskAlert
			if err := json.Unmarshal([]byte(msg.Payload), &alert); err == nil {
				select {
				case ch <- &alert:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// ============================================================
// HTTP SERVER & ROUTING
// ============================================================

func main() {
	cfg := LoadAPIConfig()

	ds, err := NewDataSource(cfg)
	if err != nil {
		log.Fatalf("Failed to create data source: %v", err)
	}

	resolver := NewResolver(ds)

	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins, // from CORS_ORIGINS env — no longer hardcoded "*"
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	}))
	limiter := newSimpleRateLimiter(cfg.RateLimitRPS)
	r.Use(limiter.middleware)

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// GraphQL Playground
	if cfg.EnablePlayground {
		r.Handle("/", playground.Handler("Scorched", "/graphql"))
	}

	// GraphQL Endpoint with WebSocket support
	srv := handler.NewDefaultServer(NewExecutableSchema(Config{
		Resolvers: resolver,
	}))
	srv.AddTransport(&transport.Websocket{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return originAllowed(cfg.CORSOrigins, r.Header.Get("Origin"))
			},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		KeepAlivePingInterval: 10 * time.Second,
	})
	srv.AddTransport(transport.Options{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})
	srv.AddTransport(transport.MultipartForm{})
	srv.Use(extension.Introspection{})

	r.Handle("/graphql", srv)

	// REST API endpoints for A2MCP compatibility
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/agents", restAgentsHandler(resolver))
		r.Get("/agents/{id}", restAgentHandler(resolver))
		r.Get("/jobs/{id}", restJobHandler(resolver))
		r.Get("/supply-chain/{agentId}", restSupplyChainHandler(resolver))
		r.Get("/leaderboard", restLeaderboardHandler(resolver))
		r.Get("/risk-alerts", restRiskAlertsHandler(resolver))
	})

	addr := ":" + cfg.Port
	log.Printf("Scorched API server starting on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// REST Handlers for A2MCP (Agent-to-MCP) compatibility
func restAgentsHandler(r *Resolver) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		tags := req.URL.Query()["tag"]
		minRepStr := req.URL.Query().Get("minAvgFeedbackValue")
		minCountStr := req.URL.Query().Get("minFeedbackCount")
		firstStr := req.URL.Query().Get("first")

		var minRep *float64
		if minRepStr != "" {
			if v, err := strconv.ParseFloat(minRepStr, 64); err == nil {
				minRep = &v
			}
		}
		var minCount *int
		if minCountStr != "" {
			if v, err := strconv.Atoi(minCountStr); err == nil {
				minCount = &v
			}
		}
		var first *int
		if firstStr != "" {
			if v, err := strconv.Atoi(firstStr); err == nil {
				first = &v
			}
		}

		agents, err := r.Agents(req.Context(), struct {
			Tags                []string
			MinAvgFeedbackValue *float64
			MinFeedbackCount    *int
			First               *int
		}{Tags: tags, MinAvgFeedbackValue: minRep, MinFeedbackCount: minCount, First: first})

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"agents": agents,
			"count":  len(agents),
		})
	}
}

func restAgentHandler(r *Resolver) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "id")
		agent, err := r.Agent(req.Context(), struct{ AgentID string }{AgentID: id})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if agent == nil {
			http.Error(w, "Agent not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(agent)
	}
}

func restJobHandler(r *Resolver) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "id")
		job, err := r.Job(req.Context(), struct{ JobID string }{JobID: id})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if job == nil {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(job)
	}
}

func restSupplyChainHandler(r *Resolver) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		agentId := chi.URLParam(req, "agentId")
		sc, err := r.AgentSupplyChain(req.Context(), struct {
			AgentID string
			Depth   *int
		}{AgentID: agentId})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sc)
	}
}

func restLeaderboardHandler(r *Resolver) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		firstStr := req.URL.Query().Get("first")

		var first *int
		if firstStr != "" {
			if v, err := strconv.Atoi(firstStr); err == nil {
				first = &v
			}
		}

		agents, err := r.TopAgents(req.Context(), struct {
			ByMetric *string
			First    *int
		}{First: first})

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"leaderboard": agents,
		})
	}
}

func restRiskAlertsHandler(r *Resolver) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		agentID := req.URL.Query().Get("agentId")
		kind := req.URL.Query().Get("kind")
		severity := req.URL.Query().Get("severity")
		firstStr := req.URL.Query().Get("first")

		var agentIDPtr, kindPtr, severityPtr *string
		if agentID != "" {
			agentIDPtr = &agentID
		}
		if kind != "" {
			kindPtr = &kind
		}
		if severity != "" {
			severityPtr = &severity
		}
		var first *int
		if firstStr != "" {
			if v, err := strconv.Atoi(firstStr); err == nil {
				first = &v
			}
		}

		alerts, err := r.RiskAlerts(req.Context(), struct {
			AgentID  *string
			Kind     *string
			Severity *string
			First    *int
		}{AgentID: agentIDPtr, Kind: kindPtr, Severity: severityPtr, First: first})

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"alerts": alerts,
			"count":  len(alerts),
		})
	}
}

// Placeholder for gqlgen schema compilation
// In production, this would be generated by gqlgen from schema.graphqls
func NewExecutableSchema(cfg Config) graphql.ExecutableSchema {
	// This is a placeholder - actual implementation requires gqlgen code generation
	return nil
}

type Config struct {
	Resolvers *Resolver
}
