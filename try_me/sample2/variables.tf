# TEST: same name as sample3's "duplicate" variable, but namespaced per
# directory now -> each directory keeps its own independent value
variable "duplicate" {
}

# TEST: same name as sample3's "region" variable, namespaced per directory;
# both happen to be set to the same real value via tfvars, which is what lets
# the provider "aws" blocks below still dedupe by resolved value
variable "region" {
}
