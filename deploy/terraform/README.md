# Terraform — evm-indexer managed dependencies

Illustrative Infrastructure-as-Code that provisions the **managed dependencies**
the evm-indexer needs on AWS:

- **RDS Postgres 16** — the indexer's primary datastore (transfers, swaps, the
  per-chain checkpoint).
- **ElastiCache Redis 7** (single node) — optional; only needed if you use the
  event stream / `cmd/consumer`.
- **Security groups** allowing Postgres `5432` and Redis `6379` from your app's
  security groups (or CIDRs).

This module deliberately does **not** provision compute (EKS/ECS) or the app
itself — the indexer is deployed with the Helm chart in `../helm/evm-indexer`.

> This is illustrative IaC. Storage is encrypted and nothing is publicly
> accessible, but review sizing, backups, Multi-AZ, and network layout before
> using it for anything real.

## Inputs

| Variable | Description | Default |
| --- | --- | --- |
| `region` | AWS region | `us-east-1` |
| `environment` | Environment name (used in names/tags) | `dev` |
| `vpc_id` | VPC for the resources | _required_ |
| `subnet_ids` | Subnets for DB/cache subnet groups (use private) | _required_ |
| `db_instance_class` | RDS instance class | `db.t3.micro` |
| `db_allocated_storage` | RDS storage (GiB) | `20` |
| `db_name` | Initial database name | `evm_indexer` |
| `db_username` | Master username | `indexer` |
| `db_password` | Master password (sensitive) | _required_ |
| `redis_node_type` | ElastiCache node type | `cache.t3.micro` |
| `allowed_security_group_ids` | App SGs allowed to connect | `[]` |
| `allowed_cidr_blocks` | CIDRs allowed to connect | `[]` |
| `tags` | Extra tags | `{}` |

## Outputs

- `database_url` — full `postgres://…` connection string (sensitive)
- `redis_url` — full `redis://…` connection string
- `db_endpoint`, `redis_endpoint` — raw `host:port`

## Usage

```hcl
module "indexer_infra" {
  source = "./deploy/terraform"

  region      = "us-east-1"
  environment = "prod"
  vpc_id      = "vpc-0abc123"
  subnet_ids  = ["subnet-0aaa", "subnet-0bbb"]

  db_password                = var.db_password
  allowed_security_group_ids = ["sg-0appnodes"]
}
```

```bash
terraform init
terraform plan
terraform apply

# Feed the outputs into the Helm release:
terraform output -raw database_url
terraform output -raw redis_url
```

## State / backend

Backend and remote-state configuration are **intentionally omitted** so this
module stays drop-in. Configure your own backend (e.g. S3 + DynamoDB lock)
before applying in a shared environment.
