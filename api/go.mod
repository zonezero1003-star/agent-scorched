module scorched/api

go 1.22

// NOTE: versions below are best-effort, not verified with `go mod tidy` —
// no Go toolchain was available in this project's sandbox. Run
// `go mod tidy` yourself before building; it will correct/fill these in.
require (
	github.com/99designs/gqlgen v0.17.45
	github.com/go-chi/chi/v5 v5.0.12
	github.com/go-chi/cors v1.2.1
	github.com/gorilla/websocket v1.5.1
	github.com/lib/pq v1.10.9
	github.com/neo4j/neo4j-go-driver/v5 v5.19.0
	github.com/redis/go-redis/v9 v9.5.1
)
