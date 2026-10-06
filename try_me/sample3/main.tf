# Main Terraform configuration
# Read sample1 state as a data source
# TEST: dependency resolution, unreferenced case - pruned with nothing spliced
# in, since (unlike sample2) nothing here reads its outputs
data "terraform_remote_state" "sample1" {
  backend = "local"

  config = {
    path = "../sample1/terraform.tfstate"
  }
}

# TEST: local value referencing a (deduped) variable
locals {
  variable_test = var.region
}

# TEST: plain folded resource, unrelated to dedup
resource "null_resource" "sample_command" {
  provisioner "local-exec" {
    command = "echo ok"
  }
}
