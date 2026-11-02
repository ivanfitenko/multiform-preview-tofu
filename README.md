# multiform

Multiform is a patched build of [OpenTofu](https://opentofu.org/) that reconciles
and applies not just the configuration in the current directory, but also the
configurations of chosen subdirectories, as if they were one flat module, with
no `module` blocks needed. Every subdirectory stays a fully valid, standalone
configuration: you can still `apply` it on its own, with its own backend,
providers and `terraform.tfvars`, exactly as before.

The typical use is adopting a pile of independently deployed Terraform projects
(network, ingress, databases, ...) into a single plan/apply, so that a change in
one is propagated to the things that depend on another, without rewriting them
as modules.

This repository contains two deliverables:

1. **The `multiform` binary** - OpenTofu with the patches described below. With no
   multiform flags it behaves like plain `tofu`.
2. **`terraform-provider-multiform`** (in [`provider/`](provider/)) - a provider,
   shipped alongside the binary, that lets an unmodified Terraform/OpenTofu drive
   multiform by managing a whole directory tree as one `multiform_stack` resource.

## Building

Multiform needs the Go version declared in `go.mod`.

```sh
# the binary (the stock `make build` produces the same thing, named `tofu`)
go build -o multiform ./cmd/tofu

# the provider (a separate Go module); put the binary next to it
cd provider
go build -o cmd/terraform-provider-multiform/terraform-provider-multiform ./cmd/terraform-provider-multiform
cp ../multiform cmd/terraform-provider-multiform/multiform
```

The provider finds the binary, in order, via its `tofu_binary` attribute, the
`MULTIFORM_TOFU_BINARY` environment variable, or a file named `multiform`
(`multiform.exe` on Windows) next to the provider executable.

## Using the binary

Run `multiform` from the root directory, with the directories to fold in given
relative to it:

```sh
multiform init  -includes=sample1 -includes=sample2
multiform plan  -includes=sample1 -includes=sample2 -use-states=global_and_local -preferred-state=local
multiform apply -includes=sample1 -includes=sample2 -use-states=global_and_local -preferred-state=local
```

The flags are accepted by `init`, `plan`, `apply`, `refresh` and `show`. Each flag
simply sets the environment variable next to it, which can be used instead; an
unset flag leaves any existing environment variable alone.

| Flag | Environment variable | Meaning |
|---|---|---|
| `-includes=DIR` (repeatable) | `MULTIFORM_INCLUDES` (newline-separated) | Subdirectories, relative to the root, to fold in. |
| `-use-states=` `global` \| `local` \| `global_and_local` | `MULTIFORM_USE_STATES` | Which backend(s) get **written** on apply. Default `global`. |
| `-preferred-state=` `global` \| `local` | `MULTIFORM_PREFERRED_STATE` | Which state is **read** as the planning baseline. Default `global`. |

"global" is the root directory's own backend; "local" is each folded-in
directory's own backend. With `local`, each directory's backend receives exactly
what a standalone apply of that directory would have written (its own resources,
outputs and nested module instances), and with `preferred-state=local` the
baseline is assembled in memory from those backends. This is the mode for
adopting existing, already-deployed directories without recreating anything.
A combination whose read source is never written (`global` with `local`, or `local`
with `global`) is rejected as an error.

These settings are read fresh on every invocation and are not stored in saved
plans. When replaying a saved plan with `show <planfile>`, or applying one, pass
the same flags that were given to `plan -out=`.

### What happens to the folded-in directories

- **Backends.** Only the backend of the directory you run from is used for the
  global state; folded-in directories' `backend` blocks are not treated as
  duplicates, and are used only for the `local` state modes. A relative `local`
  backend `path` is resolved against its own directory.
- **`required_providers`.** Identical entries are merged; they must agree exactly
  across directories, otherwise it is an error (multiform will not pick a
  provider version a directory would not select on its own).
- **Duplicate `provider`, `data` and `output` blocks** in different directories are
  merged if they resolve to the same value (compared by evaluating them for real,
  so `region = "us-east-1"` matches `region = var.region` when that variable is
  `us-east-1`) and are a `Conflicting ...` error otherwise. Resources and
  modules are never merged. Duplicates inside a single directory are still errors.
- **`terraform_remote_state`.** A data source that points at a folded-in
  directory's backend is not read from the state file: references to its
  outputs are rewritten to the real underlying expressions, so OpenTofu gets a
  genuine dependency edge between the directories.
- **Variables.** Every variable of a folded-in directory is renamed to
  `<directory>_<name>` (non-identifier characters in the directory path become `_`),
  and all its `var.` references are rewritten, so same-named variables in
  different directories stay independent. Each directory's own `terraform.tfvars`,
  `terraform.tfvars.json` and `*.auto.tfvars(.json)` are loaded for its variables,
  at the lowest precedence: root-level tfvars, `TF_VAR_*`, `-var` and `-var-file`
  win, and from the root they must use the namespaced name, e.g. `sample2_region`.
- **Local modules.** A relative `source = "./x"` in a folded-in directory resolves
  against that directory.
- **Saved plans** (`plan -out`) work for folded configurations.

Known gaps are described in the commit messages (`git log`). In particular, state
reads of the folded-in backends are not locked, and a failed write to one of them
leaves no backup file.

## Using the provider

```hcl
terraform {
  required_providers {
    multiform = { source = "local/multiform" }
  }
}

resource "multiform_stack" "main" {
  root_dir        = "path/to/root"
  use_states      = "global_and_local"
  preferred_state = "local"
  includes        = ["sample1", "sample2", "sample3"]
}
```

Plan runs the inner `init`, `plan` and `show -json`, and the outer resource shows a
change only if the inner plan has real changes; apply runs exactly the plan that
was shown. The "global" state is kept in the resource's private state, so no
second backend file is needed. **Destroying the resource runs `destroy` on
everything multiform manages in `root_dir`.**

## Trying it

[`try_me/`](try_me/) is a small tree (`sample1`, `sample2`, `sample3`) that doubles as
a manual test suite; each file carries a `# TEST:` comment saying what it exercises.
It needs `jq` and the `random`, `external`, `null` and `aws` providers (no AWS
resources are created).

```sh
ln -s ../multiform try_me/multiform    # or copy the binary
cd try_me

# 1. each directory still works on its own
(cd sample1 && ../multiform init && ../multiform apply)    # generates a random string
(cd sample2 && ../multiform init && ../multiform apply)    # reads sample1's output via terraform_remote_state

# 2. everything together, from the root (see run.sh for the flags)
./multiform init -includes=sample1 -includes=sample2 -includes=sample3
./run.sh                                                   # runs `plan`; edit it to apply
```

[`try_me/provider_example/`](try_me/provider_example/) is the same setup expressed as a
`multiform_stack` resource.
