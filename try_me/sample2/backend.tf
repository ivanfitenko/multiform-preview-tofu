# backend configuration
# Using local backend to store state in the current directory
# TEST: used when use_states or preferred_state is local or global_and_local
terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}
