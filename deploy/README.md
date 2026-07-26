# Deployment

Cloud-native deployment assets for the **evm-indexer** (a multi-chain EVM
blockchain indexer). The deployable is the primary binary `cmd/indexer`, which
serves the HTTP API + Prometheus metrics on `:8080` and runs the indexing loop.

```
deploy/
├── helm/evm-indexer/   # Helm chart for the indexer (targets cmd/indexer)
└── terraform/          # Illustrative AWS IaC for RDS Postgres + ElastiCache Redis
```

## Deployment model — single writer per chain

The indexer is **stateful-ish**: for a given chain it advances a per-chain
checkpoint in Postgres as it indexes blocks. Running two instances against the
same chain/database would double-index blocks and race on that checkpoint.

Because of this the Helm chart:

- defaults **`replicaCount: 1`**, and
- uses the **`Recreate`** Deployment strategy, so during a rollout the old pod
  fully stops before the new one starts — there is never a window with two
  writers advancing the same checkpoint concurrently.
- ships the **HPA disabled**. The read/API path could scale, but that would
  require splitting it from the single-writer indexing loop into its own
  deployment (not done here).

Run **one release per chain**, each with its own `CHAIN` and its own
`DATABASE_URL`.

## How to deploy

### 1. Provision managed dependencies (Terraform)

```bash
cd deploy/terraform
terraform init
terraform apply   # creates RDS Postgres 16 (+ optional ElastiCache Redis 7)

terraform output -raw database_url   # -> DATABASE_URL
terraform output -raw redis_url      # -> REDIS_URL (optional)
```

See `deploy/terraform/README.md` for inputs/outputs. It's illustrative IaC and
omits backend/state config on purpose.

### 2. Install the indexer (Helm)

Create the secret the chart expects (or let the chart create it via
`--set-string secrets.*`), then install:

```bash
kubectl create secret generic evm-indexer-secrets \
  --from-literal=DATABASE_URL="$(terraform -chdir=deploy/terraform output -raw database_url)" \
  --from-literal=RPC_URL="https://polygon.example.com" \
  --from-literal=REDIS_URL="$(terraform -chdir=deploy/terraform output -raw redis_url)"

helm install polygon deploy/helm/evm-indexer \
  --set env.CHAIN=polygon \
  --set image.tag=latest
```

Image: `ghcr.io/zeemzo/evm-indexer`. Probes hit `/health`; metrics are exposed
at `/metrics` (pod annotations + optional `serviceMonitor.enabled` for the
Prometheus Operator).

## CI

`.github/workflows/ci.yml` runs on every push and pull request:

- **lint** — `go vet` + golangci-lint (see `.golangci.yml`)
- **test** — `go build ./...` then `go test -race`, with a Postgres 16 service
- **docker** — builds the image with buildx (no push) to prove it builds
