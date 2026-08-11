package configs

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/opentofu/internal/addrs"
)

// PROTOTYPE (dependency resolution, Strategy A): this file recognizes when
// a terraform_remote_state data source is pointed at the backend of a
// directory that was implicitly folded into this module via includes.conf
// (see Module.KnownBackends, populated in module.go's appendFile), prunes
// it from the module, and splices every reference to it
// (data.terraform_remote_state.<name>.outputs.<attr>) with the actual
// expression that produces <attr>'s value - i.e. rewrites the config's own
// HCL expression tree at merge time, rather than trying to redirect at the
// runtime reference-resolution layer (which was attempted first; see
// ~/claude/dependency-resolution-strategy-b-plan.md for why that doesn't
// work: OpenTofu evaluates an expression's own raw traversal text against
// the evaluation context independently of how referenced values got
// resolved, so redirecting reference resolution alone can't make one piece
// of text evaluate as if it said something else).
//
// Pruning the data source is still required even though this rewrites all
// (recognized) references to it: the plan graph builder walks every
// declared data resource unconditionally (transform_config.go), so an
// unreferenced-but-still-declared terraform_remote_state would still
// attempt its real (and likely broken) backend read.
//
// The rewrite itself is deliberately scoped, not a fully general HCL
// expression rewriter: it only rewrites an expression that is exactly a
// bare traversal, or a traversal used as the value of an object/tuple
// constructor literal one level deep (covering how this pattern actually
// shows up in practice - e.g. a data "external" data source's "query"
// object). Anything embedding the traversal more deeply (inside a function
// call, string interpolation, etc.) is left untouched, which - since the
// data source has been pruned - surfaces as an honest "reference to
// undeclared resource" error rather than silently failing to redirect.
// This also only rewrites native HCL syntax (backed by *hclsyntax.Body);
// .tf.json configs are left alone.

// resolveRemoteStateReferences scans mod.DataResources for
// terraform_remote_state data sources whose backend matches one recorded in
// mod.KnownBackends, prunes each match from mod.DataResources, and rewrites
// every reference to it found elsewhere in the module to point directly at
// the referenced output's own expression.
func resolveRemoteStateReferences(mod *Module) {
	if len(mod.KnownBackends) == 0 {
		return
	}

	redirects := make(map[string]map[string]hclsyntax.Expression)

	for name, r := range mod.DataResources {
		if r.Mode != addrs.DataResourceMode || r.Type != "terraform_remote_state" {
			continue
		}

		if !remoteStateMatchesKnownBackend(mod, r) {
			continue
		}

		delete(mod.DataResources, name)

		outputs := make(map[string]hclsyntax.Expression, len(mod.Outputs))
		for outputName, output := range mod.Outputs {
			// Outputs not backed by native HCL syntax (e.g. from .tf.json)
			// can't be spliced in this way; skip them.
			if se, ok := output.Expr.(hclsyntax.Expression); ok {
				outputs[outputName] = se
			}
		}
		redirects[r.Name] = outputs
	}

	if len(redirects) == 0 {
		return
	}

	for _, r := range mod.ManagedResources {
		rewriteBody(r.Config, redirects)
	}
	for _, r := range mod.DataResources {
		rewriteBody(r.Config, redirects)
	}
	for _, r := range mod.EphemeralResources {
		rewriteBody(r.Config, redirects)
	}
	for _, output := range mod.Outputs {
		output.Expr = rewriteExprGeneric(output.Expr, redirects)
	}
	for _, local := range mod.Locals {
		local.Expr = rewriteExprGeneric(local.Expr, redirects)
	}
}

// remoteStateMatchesKnownBackend checks whether r's own "backend"/"config"
// attributes statically match one of mod's known folded-in-directory
// backends. It returns false (conservatively left alone) if r doesn't
// match, or couldn't be statically evaluated.
func remoteStateMatchesKnownBackend(mod *Module, r *Resource) bool {
	attrs, diags := r.Config.JustAttributes()
	if diags.HasErrors() {
		return false
	}

	backendAttr, ok := attrs["backend"]
	if !ok {
		return false
	}
	backendVal, backendDiags := backendAttr.Expr.Value(nil)
	if backendDiags.HasErrors() || backendVal.IsNull() || backendVal.Type() != cty.String {
		return false
	}

	// Resolve any filesystem-relative attributes (e.g. the "local" backend's
	// "path") against the directory of the file that declared this data
	// source, so they can be compared on equal footing with a backend block
	// declared in a different (folded-in) directory - see canonicalBackendKey.
	baseDir := filepath.Dir(r.DeclRange.Filename)

	var key string
	if configAttr, ok := attrs["config"]; ok {
		configVal, configDiags := configAttr.Expr.Value(nil)
		if configDiags.HasErrors() {
			return false
		}
		var keyOK bool
		key, keyOK = backendConfigKeyFromCtyValue(backendVal.AsString(), configVal, baseDir)
		if !keyOK {
			return false
		}
	} else {
		key = canonicalBackendKey(backendVal.AsString(), nil, baseDir)
	}

	_, known := mod.KnownBackends[key]
	return known
}

// rewriteBody applies rewriteExpr to every attribute expression in body,
// recursing into nested blocks. Bodies not backed by native HCL syntax
// (e.g. parsed from .tf.json) are left untouched.
func rewriteBody(body hcl.Body, redirects map[string]map[string]hclsyntax.Expression) {
	sb, ok := body.(*hclsyntax.Body)
	if !ok {
		return
	}

	for _, attr := range sb.Attributes {
		attr.Expr = rewriteExpr(attr.Expr, redirects)
	}
	for _, block := range sb.Blocks {
		rewriteBody(block.Body, redirects)
	}
}

// rewriteExprGeneric is rewriteExpr for a plain hcl.Expression-typed slot
// (Output.Expr, Local.Expr): it only rewrites when expr is backed by native
// HCL syntax, leaving anything else (e.g. from .tf.json) untouched.
func rewriteExprGeneric(expr hcl.Expression, redirects map[string]map[string]hclsyntax.Expression) hcl.Expression {
	se, ok := expr.(hclsyntax.Expression)
	if !ok {
		return expr
	}
	return rewriteExpr(se, redirects)
}

// rewriteExpr returns expr, or a modified/replacement expression, with any
// reference to data.terraform_remote_state.<name>.outputs.<attr> - where
// redirects has an entry for <name>/<attr> - spliced with that output's own
// expression. See the file-level comment for the (deliberately limited)
// scope of what gets recursed into.
func rewriteExpr(expr hclsyntax.Expression, redirects map[string]map[string]hclsyntax.Expression) hclsyntax.Expression {
	switch e := expr.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		if replacement, ok := redirectForTraversal(e.Traversal, redirects); ok {
			return replacement
		}
		return expr
	case *hclsyntax.TupleConsExpr:
		for i, elem := range e.Exprs {
			e.Exprs[i] = rewriteExpr(elem, redirects)
		}
		return e
	case *hclsyntax.ObjectConsExpr:
		for i, item := range e.Items {
			e.Items[i].ValueExpr = rewriteExpr(item.ValueExpr, redirects)
		}
		return e
	default:
		return expr
	}
}

// redirectForTraversal checks whether traversal is exactly
// data.terraform_remote_state.<name>.outputs.<attr> (no further trailing
// steps) and redirects has a matching entry.
func redirectForTraversal(traversal hcl.Traversal, redirects map[string]map[string]hclsyntax.Expression) (hclsyntax.Expression, bool) {
	if len(traversal) != 5 {
		return nil, false
	}
	root, ok := traversal[0].(hcl.TraverseRoot)
	if !ok || root.Name != "data" {
		return nil, false
	}
	typeStep, ok := traversal[1].(hcl.TraverseAttr)
	if !ok || typeStep.Name != "terraform_remote_state" {
		return nil, false
	}
	nameStep, ok := traversal[2].(hcl.TraverseAttr)
	if !ok {
		return nil, false
	}
	outputsStep, ok := traversal[3].(hcl.TraverseAttr)
	if !ok || outputsStep.Name != "outputs" {
		return nil, false
	}
	attrStep, ok := traversal[4].(hcl.TraverseAttr)
	if !ok {
		return nil, false
	}

	outputs, ok := redirects[nameStep.Name]
	if !ok {
		return nil, false
	}
	replacement, ok := outputs[attrStep.Name]
	return replacement, ok
}

// backendConfigKey computes a canonical string key for a `backend "TYPE" {
// ... }` block's configuration, for comparison against a
// terraform_remote_state data source's own "backend"/"config" attributes
// via backendConfigKeyFromCtyValue. baseDir is the directory of the file
// that declared this backend block, used to resolve any filesystem-relative
// attributes (see canonicalBackendKey). Returns ok=false if the
// configuration isn't fully statically evaluable (e.g. it references a
// variable or local), in which case it's conservatively treated as
// unrecognized.
func backendConfigKey(backendType string, config hcl.Body, baseDir string) (string, bool) {
	attrs, diags := config.JustAttributes()
	if diags.HasErrors() {
		return "", false
	}

	values := make(map[string]cty.Value, len(attrs))
	for name, attr := range attrs {
		val, valDiags := attr.Expr.Value(nil)
		if valDiags.HasErrors() {
			return "", false
		}
		values[name] = val
	}
	return canonicalBackendKey(backendType, values, baseDir), true
}

// backendConfigKeyFromCtyValue is the equivalent of backendConfigKey for a
// terraform_remote_state data source's "config" attribute, which evaluates
// to a single cty object/map value rather than a set of hcl.Body
// attributes.
func backendConfigKeyFromCtyValue(backendType string, config cty.Value, baseDir string) (string, bool) {
	if config.IsNull() {
		return canonicalBackendKey(backendType, nil, baseDir), true
	}
	if !config.IsWhollyKnown() || !config.CanIterateElements() {
		return "", false
	}

	values := make(map[string]cty.Value)
	it := config.ElementIterator()
	for it.Next() {
		k, v := it.Element()
		if k.Type() != cty.String {
			return "", false
		}
		values[k.AsString()] = v
	}
	return canonicalBackendKey(backendType, values, baseDir), true
}

// canonicalBackendKey builds a deterministic string key from a backend type
// and a set of already-evaluated, statically-known attribute values.
// Attribute values are expected to be simple (strings, numbers, bools),
// consistent with how backend configuration blocks are conventionally
// written.
//
// For the "local" backend's "path" attribute specifically, the value is
// resolved against baseDir before being included in the key. Without this,
// a backend block's own path (conventionally written relative to its own
// directory, e.g. "terraform.tfstate") would never canonicalize the same
// as an equivalent terraform_remote_state config's path (conventionally
// written relative to *its* directory, e.g. "../sample1/terraform.tfstate")
// even when they resolve to the same file.
func canonicalBackendKey(backendType string, values map[string]cty.Value, baseDir string) string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(backendType)
	for _, name := range names {
		val := values[name]
		b.WriteByte('\x00')
		b.WriteString(name)
		b.WriteByte('=')
		switch {
		case val.IsNull():
			b.WriteString("<null>")
		case backendType == "local" && name == "path" && val.Type() == cty.String:
			b.WriteString(filepath.Join(baseDir, val.AsString()))
		case val.Type() == cty.String:
			b.WriteString(val.AsString())
		default:
			b.WriteString(val.GoString())
		}
	}
	return b.String()
}
