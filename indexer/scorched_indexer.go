// ============================================================
// SCORCHED — X Layer Event Indexer (Go)
// ERC-8004 + APP Real-Time Ingestion Engine
// ============================================================

package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	_ "github.com/lib/pq"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/redis/go-redis/v9"
)

// ============================================================
// CONFIGURATION
// ============================================================

type Config struct {
	XLayerRPC         string // HTTP JSON-RPC — used for eth_call, eth_getLogs, backfill
	XLayerWS          string // WSS — required for eth_subscribe (real-time). HTTP endpoints do NOT support subscriptions.
	PostgresDSN       string
	Neo4jURI          string
	Neo4jUser         string
	Neo4jPassword     string
	RedisAddr         string
	RedisPassword     string
	StartBlock        uint64
	BlockConfirmations uint64
	BatchSize         uint64
	ReindexInterval   time.Duration
}

func LoadConfig() *Config {
	return &Config{
		// Verified against X Layer docs (web3.okx.com/xlayer/docs) as of this build.
		XLayerRPC:          getEnv("XLAYER_RPC", "https://rpc.xlayer.tech"),
		XLayerWS:           getEnv("XLAYER_WS", "wss://xlayerws.okx.com"),
		PostgresDSN:        getEnv("POSTGRES_DSN", "postgres://scorched:scorched@localhost:5432/scorched?sslmode=disable"),
		Neo4jURI:           getEnv("NEO4J_URI", "bolt://localhost:7687"),
		Neo4jUser:          getEnv("NEO4J_USER", "neo4j"),
		Neo4jPassword:      getEnv("NEO4J_PASSWORD", "scorched"),
		RedisAddr:          getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:      getEnv("REDIS_PASSWORD", ""),
		StartBlock:         getEnvUint64("START_BLOCK", 0),
		BlockConfirmations: 1, // NOTE: verify against current X Layer finality guarantees before relying on this in production — see AUDIT.md
		BatchSize:          100,
		ReindexInterval:    30 * time.Second,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getEnvUint64 parses a numeric env var, falling back on missing/invalid
// values. Used for START_BLOCK, which was previously hardcoded to 0 and
// silently ignored the env var entirely — see AUDIT.md.
func getEnvUint64(key string, fallback uint64) uint64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// ============================================================
// CONTRACT ABIs
// Sourced directly from the authoritative EIP text, not invented:
//   ERC-8004 events: https://eips.ethereum.org/EIPS/eip-8004
//   ERC-8183 events: https://eips.ethereum.org/EIPS/eip-8183 (AgenticCommerce.sol reference impl)
// ============================================================

// ERC-8004 IdentityRegistry + ReputationRegistry + ValidationRegistry events.
// Note: IdentityRegistry also emits the standard ERC-721 Transfer event on
// mint (agentId == tokenId) — handled separately since it's not ERC-8004-specific.
const ERC8004ABI = `[
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"agentId","type":"uint256"},
		{"indexed":false,"name":"agentURI","type":"string"},
		{"indexed":true,"name":"owner","type":"address"}
	],"name":"Registered","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"agentId","type":"uint256"},
		{"indexed":true,"name":"indexedMetadataKey","type":"string"},
		{"indexed":false,"name":"metadataKey","type":"string"},
		{"indexed":false,"name":"metadataValue","type":"bytes"}
	],"name":"MetadataSet","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"agentId","type":"uint256"},
		{"indexed":false,"name":"newURI","type":"string"},
		{"indexed":true,"name":"updatedBy","type":"address"}
	],"name":"URIUpdated","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"agentId","type":"uint256"},
		{"indexed":true,"name":"clientAddress","type":"address"},
		{"indexed":false,"name":"feedbackIndex","type":"uint64"},
		{"indexed":false,"name":"value","type":"int128"},
		{"indexed":false,"name":"valueDecimals","type":"uint8"},
		{"indexed":true,"name":"indexedTag1","type":"string"},
		{"indexed":false,"name":"tag1","type":"string"},
		{"indexed":false,"name":"tag2","type":"string"},
		{"indexed":false,"name":"endpoint","type":"string"},
		{"indexed":false,"name":"feedbackURI","type":"string"},
		{"indexed":false,"name":"feedbackHash","type":"bytes32"}
	],"name":"NewFeedback","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"agentId","type":"uint256"},
		{"indexed":true,"name":"clientAddress","type":"address"},
		{"indexed":true,"name":"feedbackIndex","type":"uint64"}
	],"name":"FeedbackRevoked","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"agentId","type":"uint256"},
		{"indexed":true,"name":"clientAddress","type":"address"},
		{"indexed":false,"name":"feedbackIndex","type":"uint64"},
		{"indexed":true,"name":"responder","type":"address"},
		{"indexed":false,"name":"responseURI","type":"string"},
		{"indexed":false,"name":"responseHash","type":"bytes32"}
	],"name":"ResponseAppended","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"validatorAddress","type":"address"},
		{"indexed":true,"name":"agentId","type":"uint256"},
		{"indexed":false,"name":"requestURI","type":"string"},
		{"indexed":true,"name":"requestHash","type":"bytes32"}
	],"name":"ValidationRequest","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"validatorAddress","type":"address"},
		{"indexed":true,"name":"agentId","type":"uint256"},
		{"indexed":true,"name":"requestHash","type":"bytes32"},
		{"indexed":false,"name":"response","type":"uint8"},
		{"indexed":false,"name":"responseURI","type":"string"},
		{"indexed":false,"name":"responseHash","type":"bytes32"},
		{"indexed":false,"name":"tag","type":"string"}
	],"name":"ValidationResponse","type":"event"}
]`

// Standard ERC-721 Transfer, emitted by IdentityRegistry on every register()
// (from=0x0 mint) and on transfer of agent ownership.
const ERC721ABI = `[
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"from","type":"address"},
		{"indexed":true,"name":"to","type":"address"},
		{"indexed":true,"name":"tokenId","type":"uint256"}
	],"name":"Transfer","type":"event"}
]`

// ERC-8183 AgenticCommerce (job escrow) events — from the reference
// AgenticCommerce.sol implementation in the EIP text.
const APPABI = `[
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":true,"name":"client","type":"address"},
		{"indexed":true,"name":"provider","type":"address"},
		{"indexed":false,"name":"evaluator","type":"address"},
		{"indexed":false,"name":"expiredAt","type":"uint256"},
		{"indexed":false,"name":"hook","type":"address"}
	],"name":"JobCreated","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":true,"name":"provider","type":"address"}
	],"name":"ProviderSet","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":false,"name":"amount","type":"uint256"}
	],"name":"BudgetSet","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":true,"name":"client","type":"address"},
		{"indexed":false,"name":"amount","type":"uint256"}
	],"name":"JobFunded","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":true,"name":"provider","type":"address"},
		{"indexed":false,"name":"deliverable","type":"bytes32"}
	],"name":"JobSubmitted","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":true,"name":"evaluator","type":"address"},
		{"indexed":false,"name":"reason","type":"bytes32"}
	],"name":"JobCompleted","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":true,"name":"rejector","type":"address"},
		{"indexed":false,"name":"reason","type":"bytes32"}
	],"name":"JobRejected","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"}
	],"name":"JobExpired","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":true,"name":"provider","type":"address"},
		{"indexed":false,"name":"amount","type":"uint256"}
	],"name":"PaymentReleased","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":true,"name":"evaluator","type":"address"},
		{"indexed":false,"name":"amount","type":"uint256"}
	],"name":"EvaluatorFeePaid","type":"event"},
	{"anonymous":false,"inputs":[
		{"indexed":true,"name":"jobId","type":"uint256"},
		{"indexed":true,"name":"client","type":"address"},
		{"indexed":false,"name":"amount","type":"uint256"}
	],"name":"Refunded","type":"event"}
]`

// ============================================================
// CONTRACT ADDRESSES (X Layer Mainnet/Testnet)
// ============================================================

// IMPORTANT — READ THIS BEFORE DEPLOYING
//
// These are NOT verified deployed contract addresses. As of this build there is
// no confirmed ERC-8004 identity registry or on-chain escrow contract deployed
// on X Layer at a fixed, known address. OKX's real Agent Payments Protocol (APP)
// settles primarily through an off-chain x402 Facilitator API
// (POST /api/v6/pay/x402/verify, /settle), and escrow is documented as
// "coming soon" in OKX's own APP whitepaper — not yet live on-chain.
//
// You MUST set ERC8004_REGISTRY and APP_PROTOCOL to real addresses (your own
// deployed registry/escrow, or OKX-published ones if/when available) via
// environment variables before this indexer will do anything meaningful.
// The defaults below are deliberately invalid so the indexer fails loudly
// instead of silently indexing nothing. See AUDIT.md for details.
var (
	ERC8004RegistryAddress = getEnv("ERC8004_REGISTRY", "")
	APPProtocolAddress     = getEnv("APP_PROTOCOL", "")
)

// isValidHexAddress rejects anything that isn't a real 20-byte hex address —
// catches placeholder values like "0x...APP0" that HexToAddress would otherwise
// silently mangle instead of erroring on.
func isValidHexAddress(addr string) bool {
	if !strings.HasPrefix(addr, "0x") || len(addr) != 42 {
		return false
	}
	_, err := hex.DecodeString(addr[2:])
	return err == nil
}

// ============================================================
// EVENT PARSERS
// ============================================================

type EventParser struct {
	erc8004ABI abi.ABI
	erc721ABI  abi.ABI
	appABI     abi.ABI
}

func NewEventParser() (*EventParser, error) {
	erc8004ABI, err := abi.JSON(strings.NewReader(ERC8004ABI))
	if err != nil {
		return nil, fmt.Errorf("parse ERC8004 ABI: %w", err)
	}
	erc721ABI, err := abi.JSON(strings.NewReader(ERC721ABI))
	if err != nil {
		return nil, fmt.Errorf("parse ERC721 ABI: %w", err)
	}
	appABI, err := abi.JSON(strings.NewReader(APPABI))
	if err != nil {
		return nil, fmt.Errorf("parse APP ABI: %w", err)
	}
	return &EventParser{erc8004ABI: erc8004ABI, erc721ABI: erc721ABI, appABI: appABI}, nil
}

// ERC-8004 Events (IdentityRegistry + ReputationRegistry + ValidationRegistry)
// Field names/types match the EIP-8004 spec exactly — see
// https://eips.ethereum.org/EIPS/eip-8004

type Registered struct {
	AgentId uint256Placeholder
	AgentURI string
	Owner    common.Address
}

type MetadataSetEvent struct {
	AgentId            uint256Placeholder
	IndexedMetadataKey string // indexed string — only its hash is in the topic; full value only recoverable if you also emit/store it off-chain
	MetadataKey        string
	MetadataValue      []byte
}

type URIUpdatedEvent struct {
	AgentId   uint256Placeholder
	NewURI    string
	UpdatedBy common.Address
}

type NewFeedback struct {
	AgentId       uint256Placeholder
	ClientAddress common.Address
	FeedbackIndex uint64
	Value         *big.Int // int128 in Solidity; go-ethereum ABI decodes into *big.Int, can be negative
	ValueDecimals uint8
	Tag1          string
	Tag2          string
	Endpoint      string
	FeedbackURI   string
	FeedbackHash  [32]byte
}

type FeedbackRevokedEvent struct {
	AgentId       uint256Placeholder
	ClientAddress common.Address
	FeedbackIndex uint64
}

type ResponseAppendedEvent struct {
	AgentId       uint256Placeholder
	ClientAddress common.Address
	FeedbackIndex uint64
	Responder     common.Address
	ResponseURI   string
	ResponseHash  [32]byte
}

type ValidationRequestEvent struct {
	ValidatorAddress common.Address
	AgentId          uint256Placeholder
	RequestURI       string
	RequestHash      [32]byte
}

type ValidationResponseEvent struct {
	ValidatorAddress common.Address
	AgentId          uint256Placeholder
	RequestHash      [32]byte
	Response         uint8
	ResponseURI      string
	ResponseHash     [32]byte
	Tag              string
}

// uint256Placeholder documents that agentId is a uint256 (ERC-721 tokenId),
// not the bytes32 the old fabricated ABI used. Decoded as *big.Int.
type uint256Placeholder = *big.Int

type Transfer struct {
	From    common.Address
	To      common.Address
	TokenId *big.Int
}

// ERC-8183 AgenticCommerce (Job escrow) events — field names/types match the
// reference AgenticCommerce.sol in the EIP-8183 spec exactly.

type JobCreated struct {
	JobId     *big.Int
	Client    common.Address
	Provider  common.Address
	Evaluator common.Address
	ExpiredAt *big.Int
	Hook      common.Address
}

type ProviderSetEvent struct {
	JobId    *big.Int
	Provider common.Address
}

type BudgetSetEvent struct {
	JobId  *big.Int
	Amount *big.Int
}

type JobFunded struct {
	JobId  *big.Int
	Client common.Address
	Amount *big.Int
}

type JobSubmitted struct {
	JobId       *big.Int
	Provider    common.Address
	Deliverable [32]byte
}

type JobCompleted struct {
	JobId     *big.Int
	Evaluator common.Address
	Reason    [32]byte
}

type JobRejected struct {
	JobId    *big.Int
	Rejector common.Address
	Reason   [32]byte
}

type JobExpiredEvent struct {
	JobId *big.Int
}

type PaymentReleased struct {
	JobId    *big.Int
	Provider common.Address
	Amount   *big.Int
}

type EvaluatorFeePaidEvent struct {
	JobId     *big.Int
	Evaluator common.Address
	Amount    *big.Int
}

type RefundedEvent struct {
	JobId  *big.Int
	Client common.Address
	Amount *big.Int
}

// ============================================================
// INDEXER CORE
// ============================================================

type Indexer struct {
	config       *Config
	client       *ethclient.Client // HTTP — eth_call, eth_getLogs, backfill
	wsClient     *ethclient.Client // WSS — eth_subscribe (real-time). HTTP endpoints reject subscriptions.
	parser       *EventParser
	db           *sql.DB
	neo4jDriver  neo4j.DriverWithContext
	redisClient  *redis.Client

	// Event signatures for filtering
	topics       [][]common.Hash

	// State
	lastBlock    uint64
	mu           sync.RWMutex

	// Context
	ctx          context.Context
	cancel       context.CancelFunc
}

func NewIndexer(cfg *Config) (*Indexer, error) {
	ctx, cancel := context.WithCancel(context.Background())

	if !isValidHexAddress(ERC8004RegistryAddress) {
		cancel()
		return nil, fmt.Errorf("ERC8004_REGISTRY is not set to a valid 20-byte hex address (got %q) — deploy or obtain a real registry contract and set it via env before running the indexer; see AUDIT.md", ERC8004RegistryAddress)
	}
	if !isValidHexAddress(APPProtocolAddress) {
		cancel()
		return nil, fmt.Errorf("APP_PROTOCOL is not set to a valid 20-byte hex address (got %q) — this must point at a real deployed contract, not a placeholder; see AUDIT.md", APPProtocolAddress)
	}

	// Connect to X Layer over HTTP (calls, backfill)
	client, err := ethclient.Dial(cfg.XLayerRPC)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dial X Layer HTTP RPC: %w", err)
	}

	// Connect to X Layer over WSS (required for eth_subscribe / real-time logs —
	// the HTTP endpoint above cannot serve subscriptions)
	wsClient, err := ethclient.DialContext(ctx, cfg.XLayerWS)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dial X Layer WSS RPC (%s): %w", cfg.XLayerWS, err)
	}

	// Connect to PostgreSQL
	db, err := sql.Open("postgres", cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	// Connect to Neo4j
	neo4jDriver, err := neo4j.NewDriverWithContext(
		cfg.Neo4jURI,
		neo4j.BasicAuth(cfg.Neo4jUser, cfg.Neo4jPassword, ""),
	)
	if err != nil {
		return nil, fmt.Errorf("connect neo4j: %w", err)
	}

	// Connect to Redis
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	// Parse ABIs
	parser, err := NewEventParser()
	if err != nil {
		return nil, err
	}

	// Build event filter topics
	registryAddr := common.HexToAddress(ERC8004RegistryAddress)
	appAddr := common.HexToAddress(APPProtocolAddress)

	// All ERC-8004 identity/reputation/validation events, the standard ERC-721
	// Transfer (emitted by IdentityRegistry on register/transfer), and all
	// ERC-8183 AgenticCommerce job lifecycle events.
	topics := [][]common.Hash{{
		parser.erc8004ABI.Events["Registered"].ID,
		parser.erc8004ABI.Events["MetadataSet"].ID,
		parser.erc8004ABI.Events["URIUpdated"].ID,
		parser.erc8004ABI.Events["NewFeedback"].ID,
		parser.erc8004ABI.Events["FeedbackRevoked"].ID,
		parser.erc8004ABI.Events["ResponseAppended"].ID,
		parser.erc8004ABI.Events["ValidationRequest"].ID,
		parser.erc8004ABI.Events["ValidationResponse"].ID,
		parser.erc721ABI.Events["Transfer"].ID,
		parser.appABI.Events["JobCreated"].ID,
		parser.appABI.Events["ProviderSet"].ID,
		parser.appABI.Events["BudgetSet"].ID,
		parser.appABI.Events["JobFunded"].ID,
		parser.appABI.Events["JobSubmitted"].ID,
		parser.appABI.Events["JobCompleted"].ID,
		parser.appABI.Events["JobRejected"].ID,
		parser.appABI.Events["JobExpired"].ID,
		parser.appABI.Events["PaymentReleased"].ID,
		parser.appABI.Events["EvaluatorFeePaid"].ID,
		parser.appABI.Events["Refunded"].ID,
	}}

	_ = registryAddr // Used in filter addresses
	_ = appAddr

	// Load checkpoint
	lastBlock, err := loadCheckpoint(ctx, redisClient)
	if err != nil {
		log.Printf("No checkpoint found, starting from block %d", cfg.StartBlock)
		lastBlock = cfg.StartBlock
	}

	return &Indexer{
		config:      cfg,
		client:      client,
		wsClient:    wsClient,
		parser:      parser,
		db:          db,
		neo4jDriver: neo4jDriver,
		redisClient: redisClient,
		topics:      topics,
		lastBlock:   lastBlock,
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

// ============================================================
// CHECKPOINT MANAGEMENT
// ============================================================

const checkpointKey = "scorched:last_block"

func loadCheckpoint(ctx context.Context, rdb *redis.Client) (uint64, error) {
	val, err := rdb.Get(ctx, checkpointKey).Result()
	if err != nil {
		return 0, err
	}
	var block uint64
	_, err = fmt.Sscanf(val, "%d", &block)
	return block, err
}

func saveCheckpoint(ctx context.Context, rdb *redis.Client, block uint64) error {
	return rdb.Set(ctx, checkpointKey, block, 0).Err()
}

// ============================================================
// MAIN INDEXING LOOP
// ============================================================

func (idx *Indexer) Start() error {
	log.Println("🚀 Scorched Indexer starting...")

	// Start WebSocket subscription for real-time events
	go idx.subscribeRealtime()

	// Start historical backfill
	go idx.backfillHistorical()

	// Start Neo4j sync
	go idx.syncNeo4j()

	// Start risk/fraud detection scans — Scorched's differentiator, see
	// AUDIT.md. Runs after Neo4j sync since circular hiring detection
	// reads from the graph, not directly from Postgres.
	go idx.riskScanLoop()

	// Wait for shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	idx.cancel()
	return idx.Close()
}

func (idx *Indexer) subscribeRealtime() {
	log := log.New(os.Stdout, "[REALTIME] ", log.LstdFlags)

	// Build filter for both contracts
	registryAddr := common.HexToAddress(ERC8004RegistryAddress)
	appAddr := common.HexToAddress(APPProtocolAddress)

	query := ethereum.FilterQuery{
		Addresses: []common.Address{registryAddr, appAddr},
		Topics:    idx.topics,
	}

	logs := make(chan types.Log)
	sub, err := idx.wsClient.SubscribeFilterLogs(idx.ctx, query, logs)
	if err != nil {
		log.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	log.Println("Subscribed to real-time events")

	for {
		select {
		case err := <-sub.Err():
			log.Printf("Subscription error: %v", err)
			return
		case vLog := <-logs:
			if err := idx.processLog(vLog); err != nil {
				log.Printf("Process log error: %v", err)
			}
			idx.updateCheckpoint(vLog.BlockNumber)
		case <-idx.ctx.Done():
			return
		}
	}
}

func (idx *Indexer) backfillHistorical() {
	log := log.New(os.Stdout, "[BACKFILL] ", log.LstdFlags)

	ticker := time.NewTicker(idx.config.ReindexInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := idx.runBackfill(log); err != nil {
				log.Printf("Backfill error: %v", err)
			}
		case <-idx.ctx.Done():
			return
		}
	}
}

func (idx *Indexer) runBackfill(log *log.Logger) error {
	headBlock, err := idx.client.BlockNumber(idx.ctx)
	if err != nil {
		return fmt.Errorf("get head block: %w", err)
	}

	idx.mu.RLock()
	fromBlock := idx.lastBlock + 1
	idx.mu.RUnlock()

	if fromBlock >= headBlock-idx.config.BlockConfirmations {
		return nil // Caught up
	}

	toBlock := fromBlock + idx.config.BatchSize
	if toBlock > headBlock-idx.config.BlockConfirmations {
		toBlock = headBlock - idx.config.BlockConfirmations
	}

	log.Printf("Backfilling blocks %d to %d", fromBlock, toBlock)

	registryAddr := common.HexToAddress(ERC8004RegistryAddress)
	appAddr := common.HexToAddress(APPProtocolAddress)

	query := ethereum.FilterQuery{
		FromBlock: big.NewInt(int64(fromBlock)),
		ToBlock:   big.NewInt(int64(toBlock)),
		Addresses: []common.Address{registryAddr, appAddr},
		Topics:    idx.topics,
	}

	logs, err := idx.client.FilterLogs(idx.ctx, query)
	if err != nil {
		return fmt.Errorf("filter logs: %w", err)
	}

	for _, vLog := range logs {
		if err := idx.processLog(vLog); err != nil {
			log.Printf("Process log %s: %v", vLog.TxHash.Hex(), err)
		}
	}

	idx.updateCheckpoint(toBlock)
	log.Printf("Indexed %d events from blocks %d-%d", len(logs), fromBlock, toBlock)

	return nil
}

func (idx *Indexer) updateCheckpoint(block uint64) {
	idx.mu.Lock()
	idx.lastBlock = block
	idx.mu.Unlock()

	if err := saveCheckpoint(idx.ctx, idx.redisClient, block); err != nil {
		log.Printf("Save checkpoint error: %v", err)
	}
}

// ============================================================
// EVENT PROCESSING
// ============================================================

func (idx *Indexer) processLog(vLog types.Log) error {
	if len(vLog.Topics) == 0 {
		return nil
	}

	eventSig := vLog.Topics[0]

	switch {
	// ERC-8004 Identity Registry events
	case eventSig == idx.parser.erc8004ABI.Events["Registered"].ID:
		return idx.handleRegistered(vLog)
	case eventSig == idx.parser.erc8004ABI.Events["MetadataSet"].ID:
		return idx.handleMetadataSet(vLog)
	case eventSig == idx.parser.erc8004ABI.Events["URIUpdated"].ID:
		return idx.handleURIUpdated(vLog)
	case eventSig == idx.parser.erc721ABI.Events["Transfer"].ID:
		return idx.handleTransfer(vLog)

	// ERC-8004 Reputation Registry events
	case eventSig == idx.parser.erc8004ABI.Events["NewFeedback"].ID:
		return idx.handleNewFeedback(vLog)
	case eventSig == idx.parser.erc8004ABI.Events["FeedbackRevoked"].ID:
		return idx.handleFeedbackRevoked(vLog)
	case eventSig == idx.parser.erc8004ABI.Events["ResponseAppended"].ID:
		return idx.handleResponseAppended(vLog)

	// ERC-8004 Validation Registry events
	case eventSig == idx.parser.erc8004ABI.Events["ValidationRequest"].ID:
		return idx.handleValidationRequest(vLog)
	case eventSig == idx.parser.erc8004ABI.Events["ValidationResponse"].ID:
		return idx.handleValidationResponse(vLog)

	// ERC-8183 AgenticCommerce (job escrow) events
	case eventSig == idx.parser.appABI.Events["JobCreated"].ID:
		return idx.handleJobCreated(vLog)
	case eventSig == idx.parser.appABI.Events["ProviderSet"].ID:
		return idx.handleProviderSet(vLog)
	case eventSig == idx.parser.appABI.Events["BudgetSet"].ID:
		return idx.handleBudgetSet(vLog)
	case eventSig == idx.parser.appABI.Events["JobFunded"].ID:
		return idx.handleJobFunded(vLog)
	case eventSig == idx.parser.appABI.Events["JobSubmitted"].ID:
		return idx.handleJobSubmitted(vLog)
	case eventSig == idx.parser.appABI.Events["JobCompleted"].ID:
		return idx.handleJobCompleted(vLog)
	case eventSig == idx.parser.appABI.Events["JobRejected"].ID:
		return idx.handleJobRejected(vLog)
	case eventSig == idx.parser.appABI.Events["JobExpired"].ID:
		return idx.handleJobExpired(vLog)
	case eventSig == idx.parser.appABI.Events["PaymentReleased"].ID:
		return idx.handlePaymentReleased(vLog)
	case eventSig == idx.parser.appABI.Events["EvaluatorFeePaid"].ID:
		return idx.handleEvaluatorFeePaid(vLog)
	case eventSig == idx.parser.appABI.Events["Refunded"].ID:
		return idx.handleRefunded(vLog)
	}

	return nil
}

// --- ERC-8004 Identity Registry Handlers ---

func (idx *Indexer) handleRegistered(vLog types.Log) error {
	var event Registered
	if err := idx.parser.erc8004ABI.UnpackIntoInterface(&event, "Registered", vLog.Data); err != nil {
		return fmt.Errorf("unpack Registered: %w", err)
	}
	// agentId and owner are indexed -> come from topics, not Data
	event.AgentId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.Owner = common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO agents (agent_id, owner_address, agent_uri, first_seen_at, updated_at)
		VALUES ($1, $2, $3, NOW(), NOW())
		ON CONFLICT (agent_id) DO UPDATE SET
			owner_address = EXCLUDED.owner_address,
			agent_uri = EXCLUDED.agent_uri,
			updated_at = NOW()
	`, event.AgentId.String(), strings.ToLower(event.Owner.Hex()), event.AgentURI)

	return err
}

func (idx *Indexer) handleMetadataSet(vLog types.Log) error {
	var event MetadataSetEvent
	if err := idx.parser.erc8004ABI.UnpackIntoInterface(&event, "MetadataSet", vLog.Data); err != nil {
		return fmt.Errorf("unpack MetadataSet: %w", err)
	}
	event.AgentId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	// Topics[2] is keccak256(metadataKey) since indexedMetadataKey is an indexed
	// string — the full key string is only available via the non-indexed
	// metadataKey field already unpacked from Data above.

	_, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO agent_metadata (agent_id, metadata_key, metadata_value, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (agent_id, metadata_key) DO UPDATE SET
			metadata_value = EXCLUDED.metadata_value,
			updated_at = NOW()
	`, event.AgentId.String(), event.MetadataKey, event.MetadataValue)

	return err
}

func (idx *Indexer) handleURIUpdated(vLog types.Log) error {
	var event URIUpdatedEvent
	if err := idx.parser.erc8004ABI.UnpackIntoInterface(&event, "URIUpdated", vLog.Data); err != nil {
		return fmt.Errorf("unpack URIUpdated: %w", err)
	}
	event.AgentId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.UpdatedBy = common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE agents SET agent_uri = $2, updated_at = NOW() WHERE agent_id = $1
	`, event.AgentId.String(), event.NewURI)

	return err
}

// handleTransfer covers the standard ERC-721 Transfer emitted by
// IdentityRegistry on register() (from = 0x0, mint) and on ownership
// transfer. Registered already inserts the agent row on mint, so this
// mainly keeps owner_address in sync on subsequent transfers.
func (idx *Indexer) handleTransfer(vLog types.Log) error {
	if len(vLog.Topics) < 4 {
		return nil // not a Transfer we can decode (from/to/tokenId all indexed)
	}
	from := common.BytesToAddress(vLog.Topics[1].Bytes())
	to := common.BytesToAddress(vLog.Topics[2].Bytes())
	tokenId := new(big.Int).SetBytes(vLog.Topics[3].Bytes())

	if from == (common.Address{}) {
		// Mint — Registered handles initial insert; nothing further to do here.
		return nil
	}

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE agents SET owner_address = $2, updated_at = NOW() WHERE agent_id = $1
	`, tokenId.String(), strings.ToLower(to.Hex()))

	return err
}

// --- ERC-8004 Reputation Registry Handlers ---

func (idx *Indexer) handleNewFeedback(vLog types.Log) error {
	var event NewFeedback
	if err := idx.parser.erc8004ABI.UnpackIntoInterface(&event, "NewFeedback", vLog.Data); err != nil {
		return fmt.Errorf("unpack NewFeedback: %w", err)
	}
	event.AgentId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.ClientAddress = common.BytesToAddress(vLog.Topics[2].Bytes())
	// Topics[3] is keccak256(tag1) (indexedTag1) — full tag1 string comes from Data.

	_, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO feedback (agent_id, client_address, feedback_index, value, value_decimals,
			tag1, tag2, endpoint, feedback_uri, feedback_hash, tx_hash, block_number, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW())
		ON CONFLICT (agent_id, client_address, feedback_index) DO NOTHING
	`, event.AgentId.String(), strings.ToLower(event.ClientAddress.Hex()), event.FeedbackIndex,
		event.Value.String(), event.ValueDecimals, event.Tag1, event.Tag2, event.Endpoint,
		event.FeedbackURI, hexutilEncode(event.FeedbackHash[:]), vLog.TxHash.Hex(), vLog.BlockNumber)
	if err != nil {
		return err
	}

	_, err = idx.db.ExecContext(idx.ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY agent_reputation`)
	return err
}

func (idx *Indexer) handleFeedbackRevoked(vLog types.Log) error {
	if len(vLog.Topics) < 4 {
		return nil
	}
	agentId := new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	clientAddress := common.BytesToAddress(vLog.Topics[2].Bytes())
	feedbackIndex := new(big.Int).SetBytes(vLog.Topics[3].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE feedback SET is_revoked = TRUE
		WHERE agent_id = $1 AND client_address = $2 AND feedback_index = $3
	`, agentId.String(), strings.ToLower(clientAddress.Hex()), feedbackIndex.Uint64())

	return err
}

func (idx *Indexer) handleResponseAppended(vLog types.Log) error {
	var event ResponseAppendedEvent
	if err := idx.parser.erc8004ABI.UnpackIntoInterface(&event, "ResponseAppended", vLog.Data); err != nil {
		return fmt.Errorf("unpack ResponseAppended: %w", err)
	}
	event.AgentId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.ClientAddress = common.BytesToAddress(vLog.Topics[2].Bytes())
	event.Responder = common.BytesToAddress(vLog.Topics[3].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO feedback_responses (agent_id, client_address, feedback_index, responder, response_uri, response_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
	`, event.AgentId.String(), strings.ToLower(event.ClientAddress.Hex()), event.FeedbackIndex,
		strings.ToLower(event.Responder.Hex()), event.ResponseURI, hexutilEncode(event.ResponseHash[:]))

	return err
}

// --- ERC-8004 Validation Registry Handlers ---

func (idx *Indexer) handleValidationRequest(vLog types.Log) error {
	var event ValidationRequestEvent
	if err := idx.parser.erc8004ABI.UnpackIntoInterface(&event, "ValidationRequest", vLog.Data); err != nil {
		return fmt.Errorf("unpack ValidationRequest: %w", err)
	}
	event.ValidatorAddress = common.BytesToAddress(vLog.Topics[1].Bytes())
	event.AgentId = new(big.Int).SetBytes(vLog.Topics[2].Bytes())
	event.RequestHash = [32]byte(vLog.Topics[3])

	_, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO validations (request_hash, validator_address, agent_id, request_uri, requested_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (request_hash) DO NOTHING
	`, hexutilEncode(event.RequestHash[:]), strings.ToLower(event.ValidatorAddress.Hex()),
		event.AgentId.String(), event.RequestURI)

	return err
}

func (idx *Indexer) handleValidationResponse(vLog types.Log) error {
	var event ValidationResponseEvent
	if err := idx.parser.erc8004ABI.UnpackIntoInterface(&event, "ValidationResponse", vLog.Data); err != nil {
		return fmt.Errorf("unpack ValidationResponse: %w", err)
	}
	event.ValidatorAddress = common.BytesToAddress(vLog.Topics[1].Bytes())
	event.AgentId = new(big.Int).SetBytes(vLog.Topics[2].Bytes())
	event.RequestHash = [32]byte(vLog.Topics[3])

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE validations SET response = $2, response_uri = $3, response_hash = $4, tag = $5, responded_at = NOW()
		WHERE request_hash = $1
	`, hexutilEncode(event.RequestHash[:]), event.Response, event.ResponseURI,
		hexutilEncode(event.ResponseHash[:]), event.Tag)

	return err
}

// --- ERC-8183 AgenticCommerce (job escrow) Handlers ---

func (idx *Indexer) handleJobCreated(vLog types.Log) error {
	var event JobCreated
	if err := idx.parser.appABI.UnpackIntoInterface(&event, "JobCreated", vLog.Data); err != nil {
		return fmt.Errorf("unpack JobCreated: %w", err)
	}
	event.JobId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.Client = common.BytesToAddress(vLog.Topics[2].Bytes())
	event.Provider = common.BytesToAddress(vLog.Topics[3].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO jobs (job_id, client, provider, evaluator, hook, budget, expired_at, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 0, to_timestamp($6), 'Open', NOW(), NOW())
		ON CONFLICT (job_id) DO NOTHING
	`, event.JobId.String(), strings.ToLower(event.Client.Hex()),
		nullableAddress(event.Provider), strings.ToLower(event.Evaluator.Hex()),
		nullableAddress(event.Hook), event.ExpiredAt.Int64())

	return err
}

func (idx *Indexer) handleProviderSet(vLog types.Log) error {
	if len(vLog.Topics) < 3 {
		return nil
	}
	jobId := new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	provider := common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE jobs SET provider = $2, updated_at = NOW() WHERE job_id = $1
	`, jobId.String(), strings.ToLower(provider.Hex()))

	return err
}

func (idx *Indexer) handleBudgetSet(vLog types.Log) error {
	var event BudgetSetEvent
	if err := idx.parser.appABI.UnpackIntoInterface(&event, "BudgetSet", vLog.Data); err != nil {
		return fmt.Errorf("unpack BudgetSet: %w", err)
	}
	event.JobId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE jobs SET budget = $2, updated_at = NOW() WHERE job_id = $1
	`, event.JobId.String(), event.Amount.String())

	return err
}

func (idx *Indexer) handleJobFunded(vLog types.Log) error {
	var event JobFunded
	if err := idx.parser.appABI.UnpackIntoInterface(&event, "JobFunded", vLog.Data); err != nil {
		return fmt.Errorf("unpack JobFunded: %w", err)
	}
	event.JobId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.Client = common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE jobs SET status = 'Funded', budget = $2, updated_at = NOW() WHERE job_id = $1
	`, event.JobId.String(), event.Amount.String())

	return err
}

func (idx *Indexer) handleJobSubmitted(vLog types.Log) error {
	var event JobSubmitted
	if err := idx.parser.appABI.UnpackIntoInterface(&event, "JobSubmitted", vLog.Data); err != nil {
		return fmt.Errorf("unpack JobSubmitted: %w", err)
	}
	event.JobId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.Provider = common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE jobs SET status = 'Submitted', updated_at = NOW() WHERE job_id = $1
	`, event.JobId.String())
	if err != nil {
		return err
	}

	_, err = idx.db.ExecContext(idx.ctx, `
		INSERT INTO job_submissions (job_id, deliverable, submitted_at)
		VALUES ($1, $2, NOW())
	`, event.JobId.String(), hexutilEncode(event.Deliverable[:]))

	return err
}

func (idx *Indexer) handleJobCompleted(vLog types.Log) error {
	var event JobCompleted
	if err := idx.parser.appABI.UnpackIntoInterface(&event, "JobCompleted", vLog.Data); err != nil {
		return fmt.Errorf("unpack JobCompleted: %w", err)
	}
	event.JobId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.Evaluator = common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE jobs SET status = 'Completed', updated_at = NOW() WHERE job_id = $1
	`, event.JobId.String())
	if err != nil {
		return err
	}

	_, err = idx.db.ExecContext(idx.ctx, `
		INSERT INTO job_settlements (job_id, kind, actor, reason, tx_hash, block_number, created_at)
		VALUES ($1, 'completed', $2, $3, $4, $5, NOW())
	`, event.JobId.String(), strings.ToLower(event.Evaluator.Hex()), hexutilEncode(event.Reason[:]),
		vLog.TxHash.Hex(), vLog.BlockNumber)

	return err
}

func (idx *Indexer) handleJobRejected(vLog types.Log) error {
	var event JobRejected
	if err := idx.parser.appABI.UnpackIntoInterface(&event, "JobRejected", vLog.Data); err != nil {
		return fmt.Errorf("unpack JobRejected: %w", err)
	}
	event.JobId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.Rejector = common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE jobs SET status = 'Rejected', updated_at = NOW() WHERE job_id = $1
	`, event.JobId.String())
	if err != nil {
		return err
	}

	_, err = idx.db.ExecContext(idx.ctx, `
		INSERT INTO job_settlements (job_id, kind, actor, reason, tx_hash, block_number, created_at)
		VALUES ($1, 'rejected', $2, $3, $4, $5, NOW())
	`, event.JobId.String(), strings.ToLower(event.Rejector.Hex()), hexutilEncode(event.Reason[:]),
		vLog.TxHash.Hex(), vLog.BlockNumber)

	return err
}

func (idx *Indexer) handleJobExpired(vLog types.Log) error {
	if len(vLog.Topics) < 2 {
		return nil
	}
	jobId := new(big.Int).SetBytes(vLog.Topics[1].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		UPDATE jobs SET status = 'Expired', updated_at = NOW() WHERE job_id = $1
	`, jobId.String())
	if err != nil {
		return err
	}

	_, err = idx.db.ExecContext(idx.ctx, `
		INSERT INTO job_settlements (job_id, kind, actor, reason, tx_hash, block_number, created_at)
		VALUES ($1, 'expired', NULL, NULL, $2, $3, NOW())
	`, jobId.String(), vLog.TxHash.Hex(), vLog.BlockNumber)

	return err
}

func (idx *Indexer) handlePaymentReleased(vLog types.Log) error {
	var event PaymentReleased
	if err := idx.parser.appABI.UnpackIntoInterface(&event, "PaymentReleased", vLog.Data); err != nil {
		return fmt.Errorf("unpack PaymentReleased: %w", err)
	}
	event.JobId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.Provider = common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO job_payments (job_id, kind, recipient, amount, tx_hash, block_number, created_at)
		VALUES ($1, 'payment_released', $2, $3, $4, $5, NOW())
	`, event.JobId.String(), strings.ToLower(event.Provider.Hex()), event.Amount.String(),
		vLog.TxHash.Hex(), vLog.BlockNumber)

	return err
}

func (idx *Indexer) handleEvaluatorFeePaid(vLog types.Log) error {
	var event EvaluatorFeePaidEvent
	if err := idx.parser.appABI.UnpackIntoInterface(&event, "EvaluatorFeePaid", vLog.Data); err != nil {
		return fmt.Errorf("unpack EvaluatorFeePaid: %w", err)
	}
	event.JobId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.Evaluator = common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO job_payments (job_id, kind, recipient, amount, tx_hash, block_number, created_at)
		VALUES ($1, 'evaluator_fee', $2, $3, $4, $5, NOW())
	`, event.JobId.String(), strings.ToLower(event.Evaluator.Hex()), event.Amount.String(),
		vLog.TxHash.Hex(), vLog.BlockNumber)

	return err
}

func (idx *Indexer) handleRefunded(vLog types.Log) error {
	var event RefundedEvent
	if err := idx.parser.appABI.UnpackIntoInterface(&event, "Refunded", vLog.Data); err != nil {
		return fmt.Errorf("unpack Refunded: %w", err)
	}
	event.JobId = new(big.Int).SetBytes(vLog.Topics[1].Bytes())
	event.Client = common.BytesToAddress(vLog.Topics[2].Bytes())

	_, err := idx.db.ExecContext(idx.ctx, `
		INSERT INTO job_payments (job_id, kind, recipient, amount, tx_hash, block_number, created_at)
		VALUES ($1, 'refund', $2, $3, $4, $5, NOW())
	`, event.JobId.String(), strings.ToLower(event.Client.Hex()), event.Amount.String(),
		vLog.TxHash.Hex(), vLog.BlockNumber)

	return err
}

// --- helpers ---

// hexutilEncode formats raw bytes as a lowercase "0x..." hex string for
// storage as TEXT — easier to read/debug/query than BYTEA in Postgres.
func hexutilEncode(b []byte) string {
	return "0x" + hex.EncodeToString(b)
}

// nullableAddress returns nil for the zero address (used where a Go
// sql.DB column should store SQL NULL rather than "0x000...000" — e.g.
// jobs.provider when a job is created with provider = address(0)).
func nullableAddress(addr common.Address) interface{} {
	if addr == (common.Address{}) {
		return nil
	}
	return strings.ToLower(addr.Hex())
}

// ============================================================
// NEO4J SYNC
// ============================================================

func (idx *Indexer) syncNeo4j() {
	log := log.New(os.Stdout, "[NEO4J] ", log.LstdFlags)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := idx.runNeo4jSync(log); err != nil {
				log.Printf("Neo4j sync error: %v", err)
			}
		case <-idx.ctx.Done():
			return
		}
	}
}

func (idx *Indexer) runNeo4jSync(log *log.Logger) error {
	session := idx.neo4jDriver.NewSession(idx.ctx, neo4j.SessionConfig{
		DatabaseName: "neo4j",
	})
	defer session.Close(idx.ctx)

	// Sync agents (ERC-8004 identity) + reputation summary
	rows, err := idx.db.QueryContext(idx.ctx, `
		SELECT a.agent_id, a.owner_address, a.agent_uri, a.first_seen_at,
		       COALESCE(r.feedback_count, 0), COALESCE(r.avg_value, 0)
		FROM agents a
		LEFT JOIN agent_reputation r ON r.agent_id = a.agent_id
		WHERE a.updated_at > NOW() - INTERVAL '5 minutes'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var agentId, owner, agentURI string
		var firstSeen time.Time
		var feedbackCount int64
		var avgValue float64

		if err := rows.Scan(&agentId, &owner, &agentURI, &firstSeen, &feedbackCount, &avgValue); err != nil {
			continue
		}

		_, err := session.ExecuteWrite(idx.ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			result, err := tx.Run(idx.ctx, `
				MERGE (a:Agent {agentId: $agentId})
				SET a.ownerAddress = $owner,
				    a.agentURI = $agentURI,
				    a.feedbackCount = $feedbackCount,
				    a.avgFeedbackValue = $avgValue,
				    a.firstSeenAt = datetime($firstSeen)
			`, map[string]any{
				"agentId":       agentId,
				"owner":         owner,
				"agentURI":      agentURI,
				"feedbackCount": feedbackCount,
				"avgValue":      avgValue,
				"firstSeen":     firstSeen.Format(time.RFC3339),
			})
			if err != nil {
				return nil, err
			}
			return result.Consume(idx.ctx)
		})
		if err != nil {
			log.Printf("Sync agent error: %v", err)
		}
	}

	// Sync HIRED relationships from ERC-8183 jobs. Agent-graph edges only make
	// sense when client/provider addresses match a registered agent's
	// owner_address or agentWallet metadata — this join is a simplification
	// (matches on owner_address only) and will miss agents that pay from a
	// separate agentWallet. See AUDIT.md for the follow-up needed to join
	// against agent_metadata's 'agentWallet' key instead.
	rows2, err := idx.db.QueryContext(idx.ctx, `
		SELECT j.job_id, j.client, j.provider, j.budget, j.status, j.created_at
		FROM jobs j
		WHERE j.provider IS NOT NULL AND j.created_at > NOW() - INTERVAL '5 minutes'
	`)
	if err != nil {
		return err
	}
	defer rows2.Close()

	for rows2.Next() {
		var jobId, client, provider, status string
		var budget string
		var createdAt time.Time

		if err := rows2.Scan(&jobId, &client, &provider, &budget, &status, &createdAt); err != nil {
			continue
		}

		_, err := session.ExecuteWrite(idx.ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			result, err := tx.Run(idx.ctx, `
				MATCH (client:Agent {ownerAddress: $client})
				MATCH (provider:Agent {ownerAddress: $provider})
				MERGE (client)-[h:HIRED {jobId: $jobId}]->(provider)
				SET h.budget = $budget,
				    h.status = $status,
				    h.at = datetime($createdAt)
			`, map[string]any{
				"client":    client,
				"provider":  provider,
				"jobId":     jobId,
				"budget":    budget,
				"status":    status,
				"createdAt": createdAt.Format(time.RFC3339),
			})
			if err != nil {
				return nil, err
			}
			return result.Consume(idx.ctx)
		})
		if err != nil {
			log.Printf("Sync hire error: %v", err)
		}
	}

	log.Println("Neo4j sync completed")
	return nil
}

// ============================================================
// CLEANUP
// ============================================================

func (idx *Indexer) Close() error {
	idx.cancel()
	idx.db.Close()
	idx.neo4jDriver.Close(idx.ctx)
	idx.redisClient.Close()
	idx.client.Close()
	idx.wsClient.Close()
	return nil
}

// ============================================================
// MAIN
// ============================================================

func main() {
	cfg := LoadConfig()

	idx, err := NewIndexer(cfg)
	if err != nil {
		log.Fatalf("Failed to create indexer: %v", err)
	}

	if err := idx.Start(); err != nil {
		log.Fatalf("Indexer error: %v", err)
	}
}
