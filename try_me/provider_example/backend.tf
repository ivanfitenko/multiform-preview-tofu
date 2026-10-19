# Trivial local backend for the OUTER state - this tracks only the
# multiform_stack.test resource below, not try_me's own managed resources
# (those live in try_me's own backend.tf / per-directory backends, one
# level up).
terraform {
  backend "local" {
    path = "outer.tfstate"
  }
}
