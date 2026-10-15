# Outputs
# TEST: identical to sample2's "duplicate" output -> must dedupe, not conflict
output "duplicate" {
  value = "test duplicate outputs"
}

# TEST: proves sample3's own "duplicate" variable kept its own value
# ("sample3-own-value") despite sample2 declaring a variable with the same
# name and a different value - see sample3/terraform.tfvars
output "sample3_duplicate_value" {
  value = var.duplicate
}
