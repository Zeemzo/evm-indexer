output "db_endpoint" {
  description = "RDS Postgres endpoint (host:port)."
  value       = aws_db_instance.postgres.endpoint
}

output "database_url" {
  description = "Postgres connection string for the indexer's DATABASE_URL."
  value = format(
    "postgres://%s:%s@%s:%d/%s?sslmode=require",
    var.db_username,
    var.db_password,
    aws_db_instance.postgres.address,
    aws_db_instance.postgres.port,
    var.db_name,
  )
  sensitive = true
}

output "redis_endpoint" {
  description = "ElastiCache Redis primary endpoint (host:port)."
  value = format(
    "%s:%d",
    aws_elasticache_cluster.redis.cache_nodes[0].address,
    aws_elasticache_cluster.redis.cache_nodes[0].port,
  )
}

output "redis_url" {
  description = "Redis connection string for the indexer's REDIS_URL."
  value = format(
    "redis://%s:%d/0",
    aws_elasticache_cluster.redis.cache_nodes[0].address,
    aws_elasticache_cluster.redis.cache_nodes[0].port,
  )
}
