# Outputs
# TEST: plain output folding (unique names, no dedup/conflict involved)
output "sample2_random_string_result" {
  description = "sample1's random_string_result with each digit literal replaced by 1"
  value       = data.external.replace_literals.result.random_string_result
}

output "sample2_random_id_result" {
  description = "sample1's random_id_result with each digit literal replaced by 1"
  value       = data.external.replace_literals.result.random_id_result
}

# TEST: identical to sample3's "duplicate" output -> must dedupe, not conflict
output "duplicate" {
  value = "test duplicate outputs"
}

# TEST: proves sample2's own "duplicate" variable kept its own value
# ("sample2-own-value") despite sample3 declaring a variable with the same
# name and a different value - see sample2/terraform.tfvars
output "sample2_duplicate_value" {
  value = var.duplicate
}
