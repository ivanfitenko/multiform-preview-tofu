# TEST: literal region must dedupe with sample2/sample3's var.region by real
# value (not text); mismatched value -> "Conflicting provider configuration"
provider "aws" {
  region = "us-east-1"
}

terraform {
  required_version = "~> 1.12.0"
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}
