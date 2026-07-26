terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # NOTE: backend/state configuration is intentionally omitted. Wire up your
  # own remote backend (e.g. S3 + DynamoDB lock) when consuming this module.
}

provider "aws" {
  region = var.region
}
