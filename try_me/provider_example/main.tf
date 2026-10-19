# Manages the whole try_me directory tree (one level up) as a single
# resource via the terraform-provider-multiform plugin, instead of
# running multiform directly (compare with ../run.sh). Same settings as
# ../run.sh's -use-states/-preferred-state/-includes flags.
resource "multiform_stack" "test" {
  root_dir        = ".."
  use_states      = "global_and_local"
  preferred_state = "local"
  includes        = ["sample1", "sample2", "sample3"]
}
