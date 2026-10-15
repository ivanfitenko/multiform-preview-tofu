package local

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	hcljson "github.com/hashicorp/hcl/v2/json"

	"github.com/opentofu/opentofu/internal/backend"
	"github.com/opentofu/opentofu/internal/configs"
	"github.com/opentofu/opentofu/internal/tfdiags"
	"github.com/opentofu/opentofu/internal/tofu"
)

// This file lets multiform read .tfvars files declared inside
// includes.conf-included directories, which are otherwise completely
// invisible: OpenTofu's normal tfvars auto-loading (see
// internal/command/meta_vars.go's addVarsFromDir) only ever scans the
// single directory the CLI was invoked from, never recursing into
// subdirectories. Without this, any value set only in a folded-in
// directory's own terraform.tfvars would be silently ignored, even though
// that same directory's own variable *declarations* are folded in.
//
// Every folded-in directory's variables are namespaced to that directory
// (see internal/configs/variable_namespacing.go) - a variable named
// "region" in two different directories becomes two entirely distinct
// variables once flattened, each only ever satisfiable by its own
// directory's own tfvars (or the root's own tfvars/-var under the
// namespaced name - see below). A directory's own terraform.tfvars file
// is written using the plain, undecorated name the user always used
// (namespacing is an internal multiform detail the user never needs to
// know about), so every value read from it here is re-keyed to the
// namespaced name before being handed anywhere else - this is what
// actually fixes the "one directory's tfvars value silently overrides
// another's" bug: once names can't collide across directories, there's
// nothing left to silently overwrite.
//
// Precedence: values collected here are the LOWEST-precedence real
// source - lower than TF_VAR_* environment variables, the root's own
// tfvars/auto.tfvars files, and -var/-var-file, all of which are already
// merged into op.Variables by the time localRunDirect calls
// collectDirTfvars. They rank only above a variable's own declared
// default: they exist to fill gaps, not to compete with anything set at
// the root/CLI level. See localRunDirect's merge of the two maps. To
// override one specific folded-in directory's variable from the root
// (rather than from that directory's own tfvars), the root's own
// tfvars/-var must use that variable's final, namespaced name - the root
// is the one place multiform can't shield from this internal detail,
// since root-level overrides are inherently about reaching into one
// specific directory from outside it.

const (
	multiformVarsFilename     = "terraform.tfvars"
	multiformVarsFilenameJSON = multiformVarsFilename + ".json"
)

func isMultiformAutoVarFile(name string) bool {
	return strings.HasSuffix(name, ".auto.tfvars") || strings.HasSuffix(name, ".auto.tfvars.json")
}

// collectDirTfvars reads terraform.tfvars / terraform.tfvars.json /
// *.auto.tfvars(.json) from every includes.conf-included directory that
// declares at least one variable (see Module.KnownVariableDirs), and
// returns their values keyed by each variable's namespaced name (see
// ReadDirTfvarsFiles). Since every directory's own namespace is unique,
// there is no collision between directories here - each contributes a
// disjoint set of keys to the merged result.
func collectDirTfvars(mod *configs.Module) (map[string]backend.UnparsedVariableValue, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := map[string]backend.UnparsedVariableValue{}

	if len(mod.KnownVariableDirs) == 0 {
		return ret, diags
	}

	sortedDirs := make([]string, 0, len(mod.KnownVariableDirs))
	for dir := range mod.KnownVariableDirs {
		sortedDirs = append(sortedDirs, dir)
	}
	sort.Strings(sortedDirs)

	for _, dir := range sortedDirs {
		dirVars, dirDiags := ReadDirTfvarsFiles(dir)
		diags = diags.Append(dirDiags)
		for name, v := range dirVars {
			ret[name] = v
		}
	}

	return ret, diags
}

// ReadDirTfvarsFiles reads terraform.tfvars / terraform.tfvars.json /
// *.auto.tfvars(.json) from a single directory and returns their values
// keyed by each attribute's namespaced name (see
// configs.VariableNamespace - the same namespace applied to that
// directory's variable declarations and references at parse time), so
// the result lines up with that directory's variables' real, final names
// in the flattened module. Exported for internal/command's
// rootModuleCall (see meta_config.go), which falls back to this for a
// folded-in directory's own required variable that has no value from any
// other source: that early/static evaluation pipeline (used for provider
// and data resource cross-directory dedup comparisons, among other
// things - see cross_dir_dedup.go) runs before collectDirTfvars's own
// merge into op.Variables ever happens, so without this fallback a
// variable whose only value comes from its own directory's tfvars would
// incorrectly appear undefined at that earlier stage.
func ReadDirTfvarsFiles(dir string) (map[string]backend.UnparsedVariableValue, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := map[string]backend.UnparsedVariableValue{}
	namespace := configs.VariableNamespace(dir)

	var files []string
	if _, err := os.Stat(filepath.Join(dir, multiformVarsFilename)); err == nil {
		files = append(files, filepath.Join(dir, multiformVarsFilename))
	}
	if _, err := os.Stat(filepath.Join(dir, multiformVarsFilenameJSON)); err == nil {
		files = append(files, filepath.Join(dir, multiformVarsFilenameJSON))
	}
	if infos, err := os.ReadDir(dir); err == nil {
		for _, info := range infos {
			if isMultiformAutoVarFile(info.Name()) {
				files = append(files, filepath.Join(dir, info.Name()))
			}
		}
	}

	for _, file := range files {
		diags = diags.Append(addDirTfvarsFromFile(file, namespace, ret))
	}

	return ret, diags
}

// addDirTfvarsFromFile parses filename (native HCL syntax or JSON, matching
// the rules internal/command's own addVarsFromFile uses) and adds each of
// its top-level attributes to "to", keyed by namespace + "_" + its plain
// attribute name (see ReadDirTfvarsFiles).
func addDirTfvarsFromFile(filename string, namespace string, to map[string]backend.UnparsedVariableValue) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics

	src, err := os.ReadFile(filename)
	if err != nil {
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Failed to read variables file",
			fmt.Sprintf("Error while reading %s: %s.", filename, err),
		))
		return diags
	}

	var f *hcl.File
	if strings.HasSuffix(filename, ".json") {
		var hclDiags hcl.Diagnostics
		f, hclDiags = hcljson.Parse(src, filename)
		diags = diags.Append(hclDiags)
	} else {
		var hclDiags hcl.Diagnostics
		f, hclDiags = hclsyntax.ParseConfig(src, filename, hcl.Pos{Line: 1, Column: 1})
		diags = diags.Append(hclDiags)
	}
	if f == nil || f.Body == nil {
		return diags
	}

	attrs, hclDiags := f.Body.JustAttributes()
	diags = diags.Append(hclDiags)

	for name, attr := range attrs {
		to[namespace+"_"+name] = unparsedVariableValueDirExpr{expr: attr.Expr}
	}

	return diags
}

// unparsedVariableValueDirExpr is a backend.UnparsedVariableValue for an
// expression already parsed from a folded-in directory's own tfvars file -
// the same idea as internal/command's own (unexported)
// unparsedVariableValueExpression, duplicated here rather than imported
// because internal/command imports internal/backend/local, so the reverse
// import isn't possible.
type unparsedVariableValueDirExpr struct {
	expr hcl.Expression
}

func (v unparsedVariableValueDirExpr) ParseVariableValue(mode configs.VariableParsingMode) (*tofu.InputValue, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	val, hclDiags := v.expr.Value(nil) // nil because no function calls or variable references are allowed here
	diags = diags.Append(hclDiags)

	return &tofu.InputValue{
		Value:       val,
		SourceType:  tofu.ValueFromAutoFile,
		SourceRange: tfdiags.SourceRangeFromHCL(v.expr.Range()),
	}, diags
}
