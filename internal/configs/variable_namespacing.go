package configs

import (
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// PROTOTYPE (variable disambiguation): this file rewrites every var.X
// reference within an includes.conf-included directory's own files to
// match the namespaced name applied to that directory's variable
// declarations (see Module.VariableRenames, populated in module.go's
// appendFile), so that similarly-named variables declared in different
// folded-in directories (e.g. two directories each declaring
// variable "s3_bucket") can't collide or be confused with one another once
// flattened into a single module.
//
// Unlike the remote-state reference rewrite (which only needs to handle a
// data.terraform_remote_state.X.outputs.Y traversal appearing bare, or as
// the value of an object/tuple literal, since the data source is pruned
// and so any missed case surfaces as a clear error), var.X genuinely needs
// to be found and renamed wherever it appears, however deeply nested
// (inside function calls, string templates, conditionals, and so on) -
// that's how variables are actually used in practice. Rather than
// hand-write a recursive rewriter covering every hclsyntax expression node
// type, this uses hclsyntax.Variables (the same facility OpenTofu's own
// dependency-graph reference discovery uses), which - per its Walk-based
// implementation - returns the *same* underlying traversal slices that
// each expression node holds, not copies. Renaming the variable-name step
// of each returned traversal in place therefore mutates the original AST
// node directly, with no need to reconstruct or replace any expression.

// variableNamespace turns a directory path into a valid HCL identifier
// fragment suitable as a variable name prefix, e.g. "sample1" -> "sample1",
// "some/nested/dir" -> "some_nested_dir".
func variableNamespace(dir string) string {
	var b strings.Builder
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// resolveVariableReferences rewrites every var.X reference found in each
// included directory's own resource/data/ephemeral configs, output and
// local expressions, and variable validation blocks, to the namespaced
// name recorded for it in mod.VariableRenames.
func resolveVariableReferences(mod *Module) {
	if len(mod.VariableRenames) == 0 {
		return
	}

	for _, r := range mod.ManagedResources {
		rewriteBodyVars(r.Config, mod.VariableRenames[filepath.Dir(r.DeclRange.Filename)])
	}
	for _, r := range mod.DataResources {
		rewriteBodyVars(r.Config, mod.VariableRenames[filepath.Dir(r.DeclRange.Filename)])
	}
	for _, r := range mod.EphemeralResources {
		rewriteBodyVars(r.Config, mod.VariableRenames[filepath.Dir(r.DeclRange.Filename)])
	}
	for _, output := range mod.Outputs {
		rewriteExprVars(output.Expr, mod.VariableRenames[filepath.Dir(output.DeclRange.Filename)])
	}
	for _, local := range mod.Locals {
		rewriteExprVars(local.Expr, mod.VariableRenames[filepath.Dir(local.DeclRange.Filename)])
	}
	for _, v := range mod.Variables {
		// A variable's own validation blocks may reference var.<itself>;
		// they belong to the same directory as the variable declaration.
		renames := mod.VariableRenames[filepath.Dir(v.DeclRange.Filename)]
		if len(renames) == 0 {
			continue
		}
		for _, validation := range v.Validations {
			rewriteExprVars(validation.Condition, renames)
			rewriteExprVars(validation.ErrorMessage, renames)
		}
	}
}

// rewriteBodyVars applies rewriteExprVars to every attribute expression in
// body, recursing into nested blocks. Bodies not backed by native HCL
// syntax (e.g. parsed from .tf.json) are left untouched.
func rewriteBodyVars(body hcl.Body, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	sb, ok := body.(*hclsyntax.Body)
	if !ok {
		return
	}

	for _, attr := range sb.Attributes {
		rewriteExprVars(attr.Expr, renames)
	}
	for _, block := range sb.Blocks {
		rewriteBodyVars(block.Body, renames)
	}
}

// rewriteExprVars renames the variable-name step of every var.<name>
// traversal found anywhere within expr - at any nesting depth - to
// renames[<name>], by mutating the traversal in place. See the file-level
// comment for why this is safe and doesn't need a recursive rewriter.
func rewriteExprVars(expr hcl.Expression, renames map[string]string) {
	if len(renames) == 0 || expr == nil {
		return
	}
	se, ok := expr.(hclsyntax.Expression)
	if !ok {
		return
	}

	for _, t := range hclsyntax.Variables(se) {
		if len(t) < 2 {
			continue
		}
		root, ok := t[0].(hcl.TraverseRoot)
		if !ok || root.Name != "var" {
			continue
		}
		attr, ok := t[1].(hcl.TraverseAttr)
		if !ok {
			continue
		}
		newName, ok := renames[attr.Name]
		if !ok {
			continue
		}
		t[1] = hcl.TraverseAttr{Name: newName, SrcRange: attr.SrcRange}
	}
}
