# Main Terraform configuration
# Read sample1 state as a data source
# TEST: dependency resolution - this gets pruned and spliced into a direct
# reference to sample1's own resources, since sample1 is a folded-in backend
data "terraform_remote_state" "sample1" {
  backend = "local"

  config = {
    path = "../sample1/terraform.tfstate"
  }
}

# Replace each digit literal in sample1's outputs with 1, via a piped shell command
# TEST: consumes the spliced reference above through a real provider call
data "external" "replace_literals" {
  program = ["sh", "-c", "jq '{random_string_result: (.random_string_result | gsub(\"[0-9]\"; \"1\")), random_id_result: (.random_id_result | gsub(\"[0-9]\"; \"1\"))}'"]

  query = {
    random_string_result = data.terraform_remote_state.sample1.outputs.sample1_random_string_result
    random_id_result     = data.terraform_remote_state.sample1.outputs.sample1_random_id_result
  }
}
