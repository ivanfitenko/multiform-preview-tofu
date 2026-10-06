# TEST: identical to sample3's provider "aws" -> must dedupe by real value;
# value only resolvable via this directory's own terraform.tfvars/variables.tf
provider "aws" {
  region = var.region
}

terraform {
  required_version = "~> 1.12.0"
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}
