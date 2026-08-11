# Outputs
output "sample2_random_string_result" {
  description = "sample1's random_string_result with each digit literal replaced by 1"
  value       = data.external.replace_literals.result.random_string_result
}

output "sample2_random_id_result" {
  description = "sample1's random_id_result with each digit literal replaced by 1"
  value       = data.external.replace_literals.result.random_id_result
}
