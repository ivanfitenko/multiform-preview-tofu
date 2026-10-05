# Main Terraform configuration
# Read sample1 state as a data source
data "terraform_remote_state" "sample1" {
  backend = "local"

  config = {
    path = "../sample1/terraform.tfstate"
  }
}

resource "null_resource" "sample_command" {
  provisioner "local-exec" {
    command = "echo ok"
  }
}
