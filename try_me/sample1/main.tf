# Main Terraform configuration
# Generate a random string

resource "random_string" "example" {
  length  = 16
  special = false
  upper   = true
  lower   = true
  numeric = true
}

# Alternative: random_id for shorter identifiers
resource "random_id" "short_id" {
  byte_length = 8
}