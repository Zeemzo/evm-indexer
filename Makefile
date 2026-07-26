.PHONY: build run test test-race vet lint up down docker-build clean

build:
	go build -o bin/indexer ./cmd/indexer

run: build
	./bin/indexer

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

# Full stack: Postgres + indexer
up-all:
	docker compose up -d --build

down:
	docker compose down

docker-build:
	docker build -t polygon-indexer:latest .

clean:
	rm -rf bin
