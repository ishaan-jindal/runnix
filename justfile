set shell := ["bash", "-cu"]

default:
    @just --list

dev:
    go run ./cmd/gateway

dispatcher:
    go run ./cmd/dispatcher

test:
    go test -race ./...

vet:
    go vet ./...

lint:
    golangci-lint run ./...

build:
    mkdir -p dist
    go build -o dist/runnix-gateway ./cmd/gateway
    go build -o dist/runnix-dispatcher ./cmd/dispatcher

generate:
    sqlc generate

compose-up:
    docker compose -f deploy/compose.yaml up --build

compose-down:
    docker compose -f deploy/compose.yaml down -v

migrate-up:
    go run ./cmd/gateway -migrate-only

# Render-check the hosted compose files without starting anything.
deploy-check:
    IMAGE_TAG=test POSTGRES_PASSWORD=test JWT_SECRET=test WEBHOOK_SIGNING_SECRET=test docker compose -f deploy/compose.caddy.yaml config -q
    IMAGE_TAG=test POSTGRES_PASSWORD=test JWT_SECRET=test WEBHOOK_SIGNING_SECRET=test docker compose -f deploy/compose.prod.yaml config -q
    IMAGE_TAG=test POSTGRES_PASSWORD=test JWT_SECRET=test WEBHOOK_SIGNING_SECRET=test docker compose -f deploy/compose.dev.yaml config -q
