package configs

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// This file lets each includes.conf-included directory have its own,
// independent value for a variable of a given name, even if another
// folded-in directory (or the root) happens to declare a variable with
// the exact same name - a real need for adopting multiform into existing
// projects, where every directory used to be deployed independently and
// its own terraform.tfvars was never written with any other directory's
// variables in mind. Without this, two folded-in directories' same-named
// variables collapse into a single flattened-module variable with a
// single value, and whichever directory's terraform.tfvars happens to be
// read last silently wins over the others (see multiform_vars.go's
// collectDirTfvars) - a real bug, not a feature.
//
// The fix is to rename every variable declared in a folded-in directory,
// and every var.X reference to it, by prefixing it with a namespace
// derived from that directory's own path (see VariableNamespace) - making
// collisions with any other directory's (or root's) variables impossible
// by construction, so there's no more a shared single value to fight
// over.
//
// A namespace is a pure function of (directory, original name): it does
// not depend on what any other directory declares, so there is no
// two-pass "collect every variable name across every directory, then
// decide how to rename" step. This is applied at parse time, to the raw
// syntax tree returned by Parser.LoadHCLFile, before it's decomposed into
// Resources/Variables/Outputs/etc. (see loadConfigFileBody) - walking the
// ENTIRE body recursively, covering every block at every nesting depth
// uniformly, rather than visiting a hand-maintained list of "the places a
// variable reference can appear" in the already-decomposed *File. An
// earlier version of this rewrite (since removed, then reinstated here)
// took the hand-maintained-list approach and silently missed some places -
// provider blocks were one; a module call argument was reportedly another,
// manifesting as a deeply confusing error about an internal, never-typed-
// by-the-user variable name (e.g. "var.amazonmq_region") not being set.
// Walking the raw tree generically, before anything has had a chance to
// extract or cache an un-rewritten copy of any expression, makes that
// whole class of bug impossible by construction - there is no list to
// forget an entry from.

// VariableNamespace turns a directory path into a valid HCL identifier
// fragment suitable as a variable name prefix, e.g. "sample1" -> "sample1",
// "some/nested/dir" -> "some_nested_dir".
func VariableNamespace(dir string) string {
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

// namespaceFoldedInFile rewrites every variable declaration's own label
// and every var.X reference found anywhere in body - the raw top-level
// syntax tree of a single file - to be prefixed with namespace. body must
// be the same body later passed to loadConfigFileBody for this to have
// any effect: everything that function (and anything built from its
// result) later observes is a view onto the very same underlying nodes
// this mutates in place, not a copy.
func namespaceFoldedInFile(body hcl.Body, namespace string) {
	sb, ok := body.(*hclsyntax.Body)
	if !ok {
		// Not native syntax (e.g. a .tf.json file) - nothing we can rewrite
		// in place. .tf.json folded-in directories simply don't get this
		// disambiguation; that's a pre-existing, unrelated limitation.
		return
	}
	namespaceBody(sb, namespace)
}

// namespacePrefix returns the prefix namespaceFoldedInFile adds to every
// renamed identifier.
func namespacePrefix(namespace string) string {
	return namespace + "_"
}

// namespaceBody is called at most once per file (see
// Parser.namespacedFiles) - it does not need to guard against being run
// twice on the same body itself, and deliberately doesn't try to: an
// earlier version checked whether a name already had the target prefix
// before renaming it, intending that as an idempotency guard, but that
// check is unsound - a folded-in directory's own pre-existing variable
// can easily already be named e.g. "ingress_external_domain" (a common
// hand-rolled collision-avoidance convention in real pre-multiform
// projects), which would be wrongly mistaken for an already-namespaced
// "external_domain" and skipped, leaving its declaration un-namespaced
// while terraform.tfvars values for it still get (correctly) re-keyed
// with the namespace prefix elsewhere - a real, reported bug where the
// two sides disagreed on the final name and the variable's value was
// never found.
func namespaceBody(body *hclsyntax.Body, namespace string) {
	prefix := namespacePrefix(namespace)
	for _, attr := range body.Attributes {
		namespaceExpr(attr.Expr, namespace)
	}
	for _, block := range body.Blocks {
		if block.Type == "variable" && len(block.Labels) == 1 {
			block.Labels[0] = prefix + block.Labels[0]
		}
		namespaceBody(block.Body, namespace)
	}
}

// namespaceExpr renames the variable-name step of every var.<name>
// traversal found anywhere within expr - at any nesting depth (inside
// function calls, string templates, conditionals, and so on) - to
// namespace + "_" + <name>, by mutating the traversal in place.
// hclsyntax.Variables (the same facility OpenTofu's own dependency-graph
// reference discovery uses) returns the *same* underlying traversal
// slices that expr holds, not copies, so this mutates expr's real AST
// node directly with no need to reconstruct or replace it.
func namespaceExpr(expr hcl.Expression, namespace string) {
	se, ok := expr.(hclsyntax.Expression)
	if !ok {
		return
	}
	prefix := namespacePrefix(namespace)
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
		t[1] = hcl.TraverseAttr{Name: prefix + attr.Name, SrcRange: attr.SrcRange}
	}
}
