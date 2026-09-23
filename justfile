set shell := ["bash", "-cu"]

# List all available recipes.
default:
    @just --list

# Run the gateway API locally (expects Postgres + NATS reachable).
dev:
    go run ./cmd/gateway

# Run the dispatcher worker locally (needs Docker socket for sandboxes).
dispatcher:
    go run ./cmd/dispatcher

# Run the full test suite with the race detector.
test:
    go test -race ./...

# Static analysis via go vet.
vet:
    go vet ./...

# Lint via golangci-lint.
lint:
    golangci-lint run ./...

# Build gateway + dispatcher binaries into dist/.
build:
    mkdir -p dist
    go build -o dist/runnix-gateway ./cmd/gateway
    go build -o dist/runnix-dispatcher ./cmd/dispatcher

# Regenerate sqlc database code from SQL migrations/queries.
generate:
    sqlc generate

# Start the full local stack (postgres + nats + gateway/dispatcher) with rebuild.
compose-up:
    docker compose -f deploy/compose.yaml up --build

# Stop the local stack and drop volumes (deletes local DB data).
compose-down:
    docker compose -f deploy/compose.yaml down -v

# Apply Postgres migrations only without starting the server.
migrate-up:
    go run ./cmd/gateway -migrate-only

# Render-check the hosted compose files without starting anything.
deploy-check:
    IMAGE_TAG=test POSTGRES_PASSWORD=test JWT_SECRET=test WEBHOOK_SIGNING_SECRET=test docker compose -f deploy/compose.caddy.yaml config -q
    IMAGE_TAG=test POSTGRES_PASSWORD=test JWT_SECRET=test WEBHOOK_SIGNING_SECRET=test docker compose -f deploy/compose.prod.yaml config -q
    IMAGE_TAG=test POSTGRES_PASSWORD=test JWT_SECRET=test WEBHOOK_SIGNING_SECRET=test docker compose -f deploy/compose.dev.yaml config -q
