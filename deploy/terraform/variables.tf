variable "region" {
  description = "AWS region to deploy the managed dependencies into."
  type        = string
  default     = "us-east-1"
}

variable "environment" {
  description = "Environment name (e.g. dev, staging, prod). Used in resource names and tags."
  type        = string
  default     = "dev"
}

variable "vpc_id" {
  description = "ID of the VPC the RDS and ElastiCache resources live in."
  type        = string
}

variable "subnet_ids" {
  description = "Subnet IDs for the DB and cache subnet groups (use private subnets)."
  type        = list(string)
}

# --- RDS Postgres ---

variable "db_instance_class" {
  description = "RDS instance class for the Postgres database."
  type        = string
  default     = "db.t3.micro"
}

variable "db_allocated_storage" {
  description = "Allocated storage (GiB) for the Postgres database."
  type        = number
  default     = 20
}

variable "db_name" {
  description = "Initial database name created on the RDS instance."
  type        = string
  default     = "evm_indexer"
}

variable "db_username" {
  description = "Master username for the Postgres database."
  type        = string
  default     = "indexer"
}

variable "db_password" {
  description = "Master password for the Postgres database."
  type        = string
  sensitive   = true
}

# --- ElastiCache Redis ---

variable "redis_node_type" {
  description = "ElastiCache node type for the Redis cluster."
  type        = string
  default     = "cache.t3.micro"
}

# --- Access control ---

variable "allowed_security_group_ids" {
  description = "Security group IDs allowed to reach Postgres (5432) and Redis (6379). Typically the indexer's app/node SG."
  type        = list(string)
  default     = []
}

variable "allowed_cidr_blocks" {
  description = "CIDR blocks allowed to reach Postgres (5432) and Redis (6379). Use sparingly; prefer allowed_security_group_ids."
  type        = list(string)
  default     = []
}

variable "tags" {
  description = "Additional tags applied to all resources."
  type        = map(string)
  default     = {}
}
