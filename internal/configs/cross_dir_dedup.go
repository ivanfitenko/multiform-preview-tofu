package configs

import (
	"context"
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// This file implements a reusable "deduplicable declaration" pattern:
// appendFile (module.go) uses it to tell whether two declarations of the
// same name - a `provider "TYPE" { ... }` block, a `variable "NAME"`, an
// `output "NAME"`, or a `data "TYPE" "NAME"` block - one already merged
// in, one just encountered, possibly from two different includes.conf-
// folded directories (including root) - declare exactly the same thing,
// so a coincidental duplicate across two standalone-capable directories
// can be silently deduplicated rather than rejected as a conflict (which
// remains the correct, unchanged behavior for a genuine duplicate
// declared twice within one directory).
// This mirrors how required_providers conflicts are already handled: an
// exact match is fine, any discrepancy is a hard error, since silently
// picking one over the other could make the merged configuration behave
// differently than either directory would standalone.
//
// Deliberately NOT included: `resource` and `module` blocks. Nothing here
// generalizes to those by design - they have their own identity (state,
// lifecycle) that makes "silently treat two as one" a different, much
// riskier question than it is for a provider/data/output/variable
// declaration.
//
// The pattern has two layers, so that adding a new dedupable declaration
// kind later only means writing one new small `fooEqual` function plus
// wiring it into its own loop in module.go - not touching either layer
// below:
//   - A generic, kind-agnostic comparison core: eqResult (the
//     equal/not-equal/pending three-way outcome), hclExprEqual (expression
//     comparison, including the deferred-real-evaluation rule below),
//     hclsyntaxBodiesEqual/providerConfigsEqual (recursive body
//     comparison), and pendingCrossDirEquality/
//     Module.resolvePendingCrossDirEqualities (the deferred-resolution
//     bookkeeping and post-pass, parameterized via a closure so it never
//     needs to know what kind of declaration it's resolving).
//   - One small kind-specific `fooEqual` function per declaration kind
//     (variablesEqual, outputsEqual, dataResourcesEqual, and
//     providerConfigsEqual doubling as the provider-block comparison)
//     that lists just the fields particular to that kind and delegates
//     to the generic core for anything expression/body-shaped.
//
// WHY A THIRD ("PENDING") OUTCOME IS NEEDED, NOT JUST A BOOL, AND WHY IT'S
// RESOLVED BY REAL EVALUATION RATHER THAN A STRUCTURAL GUESS:
//
// A provider/data/output comparison can hinge on an expression - most
// commonly a bare `var.X` reference - that can't be evaluated with no
// variables in scope. appendFile processes files one at a time, so at
// comparison time there's no evaluation context available yet: variable
// values come from the CLI/tfvars/env-var pipeline, which isn't wired up
// until the whole module exists. Deciding equality by guesswork here would
// be wrong in either direction: assuming "probably fine" risks silently
// merging two things that aren't actually equivalent (e.g. two different
// region values), while assuming "probably different" produces a
// confusing "conflicting configuration" error even when the two
// expressions - once actually evaluated - turn out to resolve to the same
// real value (e.g. a literal "us-east-1" against a var.region whose value
// happens to BE "us-east-1").
//
// So instead of guessing, such a comparison is deferred (eqPending) until
// Module.resolvePendingCrossDirEqualities runs - late enough that
// mod.StaticEvaluator exists (see NewModule) and can evaluate each side
// for real, using the exact same mechanism backend blocks already rely on
// for their own "early evaluation" of variable references
// (StaticEvaluator.Evaluate, backed by the real CLI/tfvars/env-var/default
// resolution pipeline - see internal/command/meta_config.go's
// rootModuleCall). This also means an actually-undeclared variable
// surfaces as OpenTofu's own standard "Reference to undeclared input
// variable" diagnostic - produced by the real evaluator, not a bespoke
// message invented here - and a declared one gets compared by its real,
// resolved value, not by the shape of the expression that produced it.

// pendingCrossDirEquality records a provider/data/output cross-directory
// dedup comparison that couldn't be immediately resolved during appendFile
// because one or more expression pairs need real evaluation (see
// hclExprEqual) to compare. Module.resolvePendingCrossDirEqualities
// resolves each of these once mod.StaticEvaluator is available.
type pendingCrossDirEquality struct {
	// exprPairs are the expression pairs (one from the existing
	// declaration, one from the new candidate) that need real evaluation
	// to compare.
	exprPairs []exprPair
	// ident identifies, for diagnostic purposes, what's being evaluated -
	// matching the StaticIdentifier a backend block's own early
	// evaluation would use.
	ident StaticIdentifier
	// conflict builds the "Conflicting ..." diagnostic appropriate to
	// this declaration kind (provider/data/output), to be used if real
	// evaluation shows any exprPair actually resolves to two different
	// values. Taking this as a closure - rather than, say, a
	// kind+label+range struct - is what lets
	// resolvePendingCrossDirEqualities stay completely generic: it never
	// needs to know what kind of declaration produced this entry.
	conflict func() *hcl.Diagnostic
}

// exprPair is one expression from an already-registered declaration
// paired with the corresponding expression from a new candidate
// declaration, deferred for real-evaluation comparison.
type exprPair struct {
	existing, candidate hcl.Expression
}

// resolvePendingCrossDirEqualities finalizes every comparison deferred by
// appendFile (see pendingCrossDirEquality), by actually evaluating each
// deferred expression pair via mod.StaticEvaluator - the same real
// variable-resolution pipeline (CLI -var, tfvars, env vars, defaults)
// backend blocks already use for their own early evaluation. Must only be
// called once mod.StaticEvaluator has been set (see NewModule); unlike
// resolveRemoteStateReferences this performs no AST mutation, so it's
// safe to call regardless of SelectiveLoader mode.
//
// Evaluation can fail for two very different reasons, which need very
// different treatment (see internal/configs/static_scope.go for the full
// list of diagnostics the static evaluator can produce):
//   - The reference is simply missing a declaration ("Undefined
//     variable"/"Undefined local") - this is just as informative and
//     actionable here as it is anywhere else a variable/local gets
//     evaluated, so it's surfaced exactly as the evaluator produced it.
//   - The expression references something that early/static evaluation
//     fundamentally can't ever resolve regardless of declarations -
//     e.g. a module output ("Module output not supported in static
//     context"), a provider function, a dynamic value, a circular
//     reference. Surfacing THOSE as-is would be actively misleading
//     here: a user reading "Module output not supported in static
//     context" on an output that only collides by name with another
//     directory's would reasonably conclude multiform can't handle
//     module outputs at all, when the real, fixable problem is just
//     that two same-named declarations can't be proven identical. These
//     are suppressed and treated the same as a real mismatch (eqResult's
//     normal "not equal" outcome), falling back to the same
//     "Conflicting ..." diagnostic a provably-different pair would get.
func (m *Module) resolvePendingCrossDirEqualities(ctx context.Context) hcl.Diagnostics {
	var diags hcl.Diagnostics
	for _, p := range m.pendingCrossDirEqualities {
		conflicted := false
		for _, pair := range p.exprPairs {
			existingVal, existingDiags := m.StaticEvaluator.Evaluate(ctx, pair.existing, p.ident)
			candidateVal, candidateDiags := m.StaticEvaluator.Evaluate(ctx, pair.candidate, p.ident)

			if existingDiags.HasErrors() || candidateDiags.HasErrors() {
				if onlyUndefinedReferenceDiags(existingDiags) && onlyUndefinedReferenceDiags(candidateDiags) {
					diags = append(diags, existingDiags...)
					diags = append(diags, candidateDiags...)
				} else {
					conflicted = true
				}
				continue
			}

			if !existingVal.RawEquals(candidateVal) {
				conflicted = true
			}
		}
		if conflicted {
			diags = append(diags, p.conflict())
		}
	}
	return diags
}

// onlyUndefinedReferenceDiags reports whether every error-severity
// diagnostic in diags is the static evaluator's "Undefined variable" or
// "Undefined local" (an empty or error-free set trivially qualifies too -
// there's nothing disqualifying it).
func onlyUndefinedReferenceDiags(diags hcl.Diagnostics) bool {
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		if d.Summary != "Undefined variable" && d.Summary != "Undefined local" {
			return false
		}
	}
	return true
}

// eqResult is the three-way outcome of comparing two expressions or
// bodies for cross-directory deduplication purposes.
type eqResult struct {
	equal   bool
	pending []exprPair // non-empty only when equal is false but not yet decided - see package comment
}

func eqEqual() eqResult    { return eqResult{equal: true} }
func eqNotEqual() eqResult { return eqResult{equal: false} }
func eqPending(pairs ...exprPair) eqResult {
	return eqResult{equal: false, pending: pairs}
}

// decided reports whether this result is final (either equal or
// definitely not equal), as opposed to pending on real evaluation.
func (r eqResult) decided() bool { return r.equal || len(r.pending) == 0 }

// providerConfigsEqual compares a and b for cross-directory deduplication
// purposes: the same attributes (by name, each comparable per
// hclExprEqual) and the same nested blocks (by position, type, labels,
// and recursively-equal body). Bodies that aren't backed by native HCL
// syntax are treated as definitely unequal - not provably identical, so
// they must not be silently merged. Used for both `provider` blocks and
// `data` resources' own Config body.
func providerConfigsEqual(a, b hcl.Body) eqResult {
	sa, ok := a.(*hclsyntax.Body)
	if !ok {
		return eqNotEqual()
	}
	sb, ok := b.(*hclsyntax.Body)
	if !ok {
		return eqNotEqual()
	}
	return hclsyntaxBodiesEqual(sa, sb)
}

func hclsyntaxBodiesEqual(a, b *hclsyntax.Body) eqResult {
	if len(a.Attributes) != len(b.Attributes) || len(a.Blocks) != len(b.Blocks) {
		return eqNotEqual()
	}

	var pending []exprPair
	for name, attrA := range a.Attributes {
		attrB, ok := b.Attributes[name]
		if !ok {
			return eqNotEqual()
		}
		r := hclExprEqual(attrA.Expr, attrB.Expr)
		if !r.equal {
			if r.decided() {
				return eqNotEqual()
			}
			pending = append(pending, r.pending...)
		}
	}

	// Blocks are compared positionally (order-sensitive) rather than by
	// some order-independent matching - good enough for the common case
	// this exists for (a block copy-pasted as-is across directories),
	// without the complexity of pairing up blocks that happen to repeat.
	for i, blockA := range a.Blocks {
		blockB := b.Blocks[i]
		if blockA.Type != blockB.Type || !slices.Equal(blockA.Labels, blockB.Labels) {
			return eqNotEqual()
		}
		r := hclsyntaxBodiesEqual(blockA.Body, blockB.Body)
		if !r.equal {
			if r.decided() {
				return eqNotEqual()
			}
			pending = append(pending, r.pending...)
		}
	}

	if len(pending) > 0 {
		return eqPending(pending...)
	}
	return eqEqual()
}

// hclExprEqual compares a and b for cross-directory deduplication
// purposes: both nil; both statically evaluate (with no variables/
// functions in scope) to the same cty value; or - if either side fails to
// evaluate that way, e.g. because it references a variable - deferred for
// real evaluation (see eqPending and
// Module.resolvePendingCrossDirEqualities). Deferring rather than
// guessing means a literal value and a variable reference that happens to
// resolve to that same value ARE eventually recognized as equal (true
// cross-directory deduplication "by value", not just by text), while an
// undeclared or genuinely different variable is still caught - just via
// the real evaluator's own diagnostics/comparison, once available,
// instead of a guess made here.
func hclExprEqual(a, b hcl.Expression) eqResult {
	if a == nil || b == nil {
		if a == nil && b == nil {
			return eqEqual()
		}
		return eqNotEqual()
	}
	valA, diagsA := a.Value(nil)
	valB, diagsB := b.Value(nil)
	if diagsA.HasErrors() || diagsB.HasErrors() {
		return eqPending(exprPair{existing: a, candidate: b})
	}
	if valA.RawEquals(valB) {
		return eqEqual()
	}
	return eqNotEqual()
}

// variablesEqual reports whether a and b are equivalent `variable`
// declarations for cross-directory deduplication purposes. Unlike
// provider/data/output comparisons, this is always immediately decidable:
// a variable's Default is fully evaluated at decode time (HCL doesn't
// allow a variable's default to reference anything else), so there's no
// deferred-evaluation concern here. A variable with a validation block
// (on either side) is always treated as unequal here - not because
// comparing them would be unsafe (variables are no longer renamed at
// all, so there's no AST-mutation risk to worry about), but simply
// because comparing two validation blocks' condition/error-message
// expressions for real equivalence isn't implemented; a validated
// variable that doesn't collide by name with anything else is otherwise
// handled completely normally (registered under its own name, no
// restriction).
func variablesEqual(a, b *Variable) bool {
	if len(a.Validations) != 0 || len(b.Validations) != 0 {
		return false
	}
	if a.Description != b.Description || a.DescriptionSet != b.DescriptionSet {
		return false
	}
	if a.Sensitive != b.Sensitive || a.SensitiveSet != b.SensitiveSet {
		return false
	}
	if a.Deprecated != b.Deprecated {
		return false
	}
	if a.Ephemeral != b.Ephemeral || a.EphemeralSet != b.EphemeralSet {
		return false
	}
	if a.Nullable != b.Nullable || a.NullableSet != b.NullableSet {
		return false
	}
	if a.ParsingMode != b.ParsingMode {
		return false
	}
	if !a.Type.Equals(b.Type) || !a.ConstraintType.Equals(b.ConstraintType) {
		return false
	}
	return a.Default.RawEquals(b.Default)
}

// outputsEqual reports whether a and b are equivalent `output`
// declarations for cross-directory deduplication purposes. An output
// with any depends_on or precondition is conservatively treated as
// unequal to anything (not worth the complexity of comparing check
// blocks/traversals here), and the value expression itself is compared
// per hclExprEqual - including its deferred-real-evaluation rule.
func outputsEqual(a, b *Output) eqResult {
	if a.Description != b.Description || a.DescriptionSet != b.DescriptionSet {
		return eqNotEqual()
	}
	if a.Sensitive != b.Sensitive || a.SensitiveSet != b.SensitiveSet {
		return eqNotEqual()
	}
	if a.Deprecated != b.Deprecated {
		return eqNotEqual()
	}
	if a.Ephemeral != b.Ephemeral || a.EphemeralSet != b.EphemeralSet {
		return eqNotEqual()
	}
	if len(a.Preconditions) != 0 || len(b.Preconditions) != 0 {
		return eqNotEqual()
	}
	if len(a.DependsOn) != 0 || len(b.DependsOn) != 0 {
		return eqNotEqual()
	}
	return hclExprEqual(a.Expr, b.Expr)
}

// dataResourcesEqual reports whether a and b are equivalent `data`
// resource declarations for cross-directory deduplication purposes. Only
// the Config body and the count/for_each/enabled meta-arguments are
// compared (each via hclExprEqual/providerConfigsEqual, including their
// deferred-real-evaluation rule); a data resource with any depends_on or
// precondition/postcondition, on either side, is conservatively treated
// as unequal (same rationale as outputsEqual: not worth comparing check
// blocks/traversals here).
func dataResourcesEqual(a, b *Resource) eqResult {
	if len(a.Preconditions) != 0 || len(b.Preconditions) != 0 {
		return eqNotEqual()
	}
	if len(a.Postconditions) != 0 || len(b.Postconditions) != 0 {
		return eqNotEqual()
	}
	if len(a.DependsOn) != 0 || len(b.DependsOn) != 0 {
		return eqNotEqual()
	}
	if (a.ProviderConfigRef == nil) != (b.ProviderConfigRef == nil) {
		return eqNotEqual()
	}
	if a.ProviderConfigRef != nil && (a.ProviderConfigRef.Name != b.ProviderConfigRef.Name || a.ProviderConfigRef.Alias != b.ProviderConfigRef.Alias) {
		return eqNotEqual()
	}

	var pending []exprPair
	for _, r := range []eqResult{
		providerConfigsEqual(a.Config, b.Config),
		hclExprEqual(a.Count, b.Count),
		hclExprEqual(a.ForEach, b.ForEach),
		hclExprEqual(a.Enabled, b.Enabled),
	} {
		if !r.equal {
			if r.decided() {
				return eqNotEqual()
			}
			pending = append(pending, r.pending...)
		}
	}
	if len(pending) > 0 {
		return eqPending(pending...)
	}
	return eqEqual()
}
