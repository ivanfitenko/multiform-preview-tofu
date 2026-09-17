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
// that same directory's own variable *declarations* are folded in (and
// disambiguated - see variable_namespacing.go).
//
// Precedence: values collected here are the LOWEST-precedence real
// source - lower than TF_VAR_* environment variables, the root's own
// tfvars/auto.tfvars files, and -var/-var-file, all of which are already
// merged into op.Variables by the time localRunDirect calls
// collectDirTfvars. They rank only above a variable's own declared
// default: they exist to fill gaps, not to compete with anything set at
// the root/CLI level. See localRunDirect's merge of the two maps.

const (
	multiformVarsFilename     = "terraform.tfvars"
	multiformVarsFilenameJSON = multiformVarsFilename + ".json"
)

func isMultiformAutoVarFile(name string) bool {
	return strings.HasSuffix(name, ".auto.tfvars") || strings.HasSuffix(name, ".auto.tfvars.json")
}

// collectDirTfvars reads terraform.tfvars / terraform.tfvars.json /
// *.auto.tfvars(.json) from every includes.conf-included directory that
// declares at least one variable (renamed or not - see
// Module.VariableRenames), and returns their values keyed by each
// variable's *final* name in the merged module: the renamed name if that
// variable was namespaced, or its original name otherwise (covering the
// self-referencing-validation exception in variable_namespacing.go, and
// any genuinely-undeclared key, which backend.ParseVariableValues will
// separately warn about as usual).
func collectDirTfvars(mod *configs.Module) (map[string]backend.UnparsedVariableValue, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	ret := map[string]backend.UnparsedVariableValue{}

	dirs := map[string]struct{}{}
	for _, v := range mod.Variables {
		dir := filepath.Dir(v.DeclRange.Filename)
		if dir != filepath.Clean(mod.SourceDir) {
			dirs[dir] = struct{}{}
		}
	}
	if len(dirs) == 0 {
		return ret, diags
	}

	sortedDirs := make([]string, 0, len(dirs))
	for dir := range dirs {
		sortedDirs = append(sortedDirs, dir)
	}
	sort.Strings(sortedDirs)

	for _, dir := range sortedDirs {
		renames := mod.VariableRenames[dir]

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
			diags = diags.Append(addDirTfvarsFromFile(file, renames, ret))
		}
	}

	return ret, diags
}

// addDirTfvarsFromFile parses filename (native HCL syntax or JSON, matching
// the rules internal/command's own addVarsFromFile uses) and adds each of
// its top-level attributes to "to", renaming the key via "renames" where
// applicable.
func addDirTfvarsFromFile(filename string, renames map[string]string, to map[string]backend.UnparsedVariableValue) tfdiags.Diagnostics {
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
		key := name
		if renamed, ok := renames[name]; ok {
			key = renamed
		}
		to[key] = unparsedVariableValueDirExpr{expr: attr.Expr}
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
