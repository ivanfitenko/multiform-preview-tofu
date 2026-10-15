# TEST: per-directory tfvars folding; also feeds the early-eval path used by
# cross-directory provider dedup (change this value to mismatch sample1/sample3)
region = "us-east-1"

# TEST: independent per-directory value for a same-named variable - must stay
# "sample2-own-value" regardless of what sample3/terraform.tfvars sets
duplicate = "sample2-own-value"
