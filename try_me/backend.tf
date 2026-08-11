# backend configuration
# Using local backend to store state in the current directory
terraform {
  backend "local" {
    path = "common_terraform.tfstate"
  }
}