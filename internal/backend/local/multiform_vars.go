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
// that same directory's own variable *declarations* are folded in (see
// internal/configs/module.go's appendFile and cross_dir_dedup.go's
// variablesEqual - deduplicated if identical to one declared elsewhere,
// a hard error if not, but never renamed).
//
// Precedence: values collected here are the LOWEST-precedence real
// source - lower than TF_VAR_* environment variables, the root's own
// tfvars/auto.tfvars files, and -var/-var-file, all of which are already
// merged into op.Variables by the time localRunDirect calls
// collectDirTfvars. They rank only above a variable's own declared
// default: they exist to fill gaps, not to compete with anything set at
// the root/CLI level. See localRunDirect's merge of the two maps. Since a
// folded-in directory's own variable is never renamed, a value for it
// found in the ROOT's own terraform.tfvars/auto.tfvars (or -var) under
// its plain, undecorated name already takes precedence correctly, with
// no special-casing needed here.

const (
	multiformVarsFilename     = "terraform.tfvars"
	multiformVarsFilenameJSON = multiformVarsFilename + ".json"
)

func isMultiformAutoVarFile(name string) bool {
	return strings.HasSuffix(name, ".auto.tfvars") || strings.HasSuffix(name, ".auto.tfvars.json")
}

// collectDirTfvars reads terraform.tfvars / terraform.tfvars.json /
// *.auto.tfvars(.json) from every includes.conf-included directory that
// declares at least one variable (see Module.KnownVariableDirs - this
// has to be consulted rather than inferred from Module.Variables, since a
// directory's variable declarations might have been entirely
// deduplicated away in favor of an identical one kept from elsewhere),
// and returns their values keyed by the plain variable name (never
// renamed - see module.go's appendFile and cross_dir_dedup.go's
// variablesEqual).
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
// keyed by plain attribute name. Exported for internal/command's
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
		diags = diags.Append(addDirTfvarsFromFile(file, ret))
	}

	return ret, diags
}

// addDirTfvarsFromFile parses filename (native HCL syntax or JSON, matching
// the rules internal/command's own addVarsFromFile uses) and adds each of
// its top-level attributes to "to", keyed by its plain attribute name.
func addDirTfvarsFromFile(filename string, to map[string]backend.UnparsedVariableValue) tfdiags.Diagnostics {
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
		to[name] = unparsedVariableValueDirExpr{expr: attr.Expr}
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
