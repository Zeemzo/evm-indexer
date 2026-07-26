.PHONY: build build-all run mcp consumer test test-race vet lint up up-redis down docker-build clean

build:
	go build -o bin/indexer ./cmd/indexer

# Build all three binaries (indexer, mcp, consumer)
build-all:
	go build -o bin/indexer ./cmd/indexer
	go build -o bin/mcp ./cmd/mcp
	go build -o bin/consumer ./cmd/consumer

run: build
	./bin/indexer

# Build the MCP stdio server (point Claude Desktop / Cursor at bin/mcp)
mcp:
	go build -o bin/mcp ./cmd/mcp

# Build the example Redis Stream consumer
consumer:
	go build -o bin/consumer ./cmd/consumer

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# Start Postgres only (run the indexer locally against it)
up:
	docker compose up -d postgres

# Start Postgres + Redis (for the event-streaming path)
up-redis:
	docker compose up -d postgres redis

# Full stack: Postgres + Redis + indexer + consumer
up-all:
	docker compose up -d --build

down:
	docker compose down

docker-build:
	docker build -t evm-indexer:latest .

clean:
	rm -rf bin
