# Outputs
output "sample1_random_string_result" {
  description = "A randomly generated string"
  value       = random_string.example.result
}

output "sample1_random_id_result" {
  description = "A randomly generated ID in hex format"
  value       = random_id.short_id.hex
}
