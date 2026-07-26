# Illustrative IaC for the evm-indexer's managed dependencies:
#   - RDS Postgres 16 (the indexer's primary datastore)
#   - ElastiCache Redis 7 (optional event stream)
# plus security groups gating access from the indexer's app security groups.

locals {
  name_prefix = "evm-indexer-${var.environment}"

  common_tags = merge(
    {
      Project     = "evm-indexer"
      Environment = var.environment
      ManagedBy   = "terraform"
    },
    var.tags,
  )
}

# ---------------------------------------------------------------------------
# Security groups
# ---------------------------------------------------------------------------

# Postgres SG: allow 5432 from the app SGs / CIDRs only.
resource "aws_security_group" "postgres" {
  name        = "${local.name_prefix}-postgres"
  description = "Access to the evm-indexer Postgres database"
  vpc_id      = var.vpc_id
  tags        = merge(local.common_tags, { Name = "${local.name_prefix}-postgres" })
}

resource "aws_security_group_rule" "postgres_from_sg" {
  for_each                 = toset(var.allowed_security_group_ids)
  type                     = "ingress"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  security_group_id        = aws_security_group.postgres.id
  source_security_group_id = each.value
  description              = "Postgres from app security group"
}

resource "aws_security_group_rule" "postgres_from_cidr" {
  count             = length(var.allowed_cidr_blocks) > 0 ? 1 : 0
  type              = "ingress"
  from_port         = 5432
  to_port           = 5432
  protocol          = "tcp"
  security_group_id = aws_security_group.postgres.id
  cidr_blocks       = var.allowed_cidr_blocks
  description       = "Postgres from allowed CIDR blocks"
}

resource "aws_security_group_rule" "postgres_egress" {
  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  security_group_id = aws_security_group.postgres.id
  cidr_blocks       = ["0.0.0.0/0"]
  description       = "Allow all egress"
}

# Redis SG: allow 6379 from the app SGs / CIDRs only.
resource "aws_security_group" "redis" {
  name        = "${local.name_prefix}-redis"
  description = "Access to the evm-indexer Redis cache"
  vpc_id      = var.vpc_id
  tags        = merge(local.common_tags, { Name = "${local.name_prefix}-redis" })
}

resource "aws_security_group_rule" "redis_from_sg" {
  for_each                 = toset(var.allowed_security_group_ids)
  type                     = "ingress"
  from_port                = 6379
  to_port                  = 6379
  protocol                 = "tcp"
  security_group_id        = aws_security_group.redis.id
  source_security_group_id = each.value
  description              = "Redis from app security group"
}

resource "aws_security_group_rule" "redis_from_cidr" {
  count             = length(var.allowed_cidr_blocks) > 0 ? 1 : 0
  type              = "ingress"
  from_port         = 6379
  to_port           = 6379
  protocol          = "tcp"
  security_group_id = aws_security_group.redis.id
  cidr_blocks       = var.allowed_cidr_blocks
  description       = "Redis from allowed CIDR blocks"
}

resource "aws_security_group_rule" "redis_egress" {
  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  security_group_id = aws_security_group.redis.id
  cidr_blocks       = ["0.0.0.0/0"]
  description       = "Allow all egress"
}

# ---------------------------------------------------------------------------
# RDS Postgres 16
# ---------------------------------------------------------------------------

resource "aws_db_subnet_group" "this" {
  name       = "${local.name_prefix}-db"
  subnet_ids = var.subnet_ids
  tags       = merge(local.common_tags, { Name = "${local.name_prefix}-db" })
}

resource "aws_db_instance" "postgres" {
  identifier     = "${local.name_prefix}-postgres"
  engine         = "postgres"
  engine_version = "16"
  instance_class = var.db_instance_class

  allocated_storage = var.db_allocated_storage
  storage_type      = "gp3"
  storage_encrypted = true # encrypt at rest

  db_name  = var.db_name
  username = var.db_username
  password = var.db_password
  port     = 5432

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.postgres.id]

  # Keep the database private — the indexer reaches it from within the VPC.
  publicly_accessible = false
  multi_az            = false

  backup_retention_period = 7
  # Illustrative module: allow clean teardown without a final snapshot.
  skip_final_snapshot = true
  apply_immediately   = true

  tags = merge(local.common_tags, { Name = "${local.name_prefix}-postgres" })
}

# ---------------------------------------------------------------------------
# ElastiCache Redis 7 (single node)
# ---------------------------------------------------------------------------

resource "aws_elasticache_subnet_group" "this" {
  name       = "${local.name_prefix}-redis"
  subnet_ids = var.subnet_ids
  tags       = merge(local.common_tags, { Name = "${local.name_prefix}-redis" })
}

resource "aws_elasticache_cluster" "redis" {
  cluster_id           = "${local.name_prefix}-redis"
  engine               = "redis"
  engine_version       = "7.1"
  node_type            = var.redis_node_type
  num_cache_nodes      = 1 # single node is fine for the event stream
  port                 = 6379
  parameter_group_name = "default.redis7"

  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = [aws_security_group.redis.id]

  tags = merge(local.common_tags, { Name = "${local.name_prefix}-redis" })
}
