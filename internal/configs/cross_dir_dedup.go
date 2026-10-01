package configs

import (
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// This file lets appendFile (module.go) tell whether two declarations of
// the same name - a `provider "TYPE" { ... }` block, a `variable "NAME"`,
// an `output "NAME"`, or a `data "TYPE" "NAME"` block - one already merged
// in, one just encountered, possibly from two different includes.conf-
// folded directories (including root) - declare exactly the same thing,
// so a coincidental duplicate across two standalone-capable directories
// can be silently deduplicated rather than rejected as a conflict (which
// remains the correct, unchanged behavior for a genuine duplicate
// declared twice within one directory). This mirrors how
// required_providers conflicts are already handled: an exact match is
// fine, any discrepancy is a hard error, since silently picking one over
// the other could make the merged configuration behave differently than
// either directory would standalone.

// providerConfigsEqual reports whether a and b are structurally identical:
// the same attributes (by name, each evaluating to an equal, statically-
// known value) and the same nested blocks (by position, type, labels, and
// recursively-equal body). Bodies that aren't backed by native HCL syntax,
// or that contain any attribute that can't be statically evaluated (e.g. a
// reference to a variable), are conservatively treated as unequal - not
// provably identical, so they must not be silently merged. Used for both
// `provider` blocks and `data` resources' own Config body.
func providerConfigsEqual(a, b hcl.Body) bool {
	sa, ok := a.(*hclsyntax.Body)
	if !ok {
		return false
	}
	sb, ok := b.(*hclsyntax.Body)
	if !ok {
		return false
	}
	return hclsyntaxBodiesEqual(sa, sb)
}

func hclsyntaxBodiesEqual(a, b *hclsyntax.Body) bool {
	if len(a.Attributes) != len(b.Attributes) || len(a.Blocks) != len(b.Blocks) {
		return false
	}

	for name, attrA := range a.Attributes {
		attrB, ok := b.Attributes[name]
		if !ok {
			return false
		}
		if !hclExprEqual(attrA.Expr, attrB.Expr) {
			return false
		}
	}

	// Blocks are compared positionally (order-sensitive) rather than by
	// some order-independent matching - good enough for the common case
	// this exists for (a block copy-pasted as-is across directories),
	// without the complexity of pairing up blocks that happen to repeat.
	for i, blockA := range a.Blocks {
		blockB := b.Blocks[i]
		if blockA.Type != blockB.Type || !slices.Equal(blockA.Labels, blockB.Labels) {
			return false
		}
		if !hclsyntaxBodiesEqual(blockA.Body, blockB.Body) {
			return false
		}
	}

	return true
}

// hclExprEqual reports whether a and b are both nil, or both statically
// evaluate (with no variables/functions in scope) to the same cty value.
// An expression that can't be statically evaluated - e.g. one referencing
// a variable, local, or resource attribute - is conservatively treated as
// unequal to anything, including an identical copy of itself, since two
// such expressions appearing in two different directories may resolve
// differently once each directory's own references are taken into
// account (e.g. a namespaced variable).
func hclExprEqual(a, b hcl.Expression) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	valA, diagsA := a.Value(nil)
	if diagsA.HasErrors() {
		return false
	}
	valB, diagsB := b.Value(nil)
	if diagsB.HasErrors() {
		return false
	}
	return valA.RawEquals(valB)
}

// variablesEqual reports whether a and b are equivalent `variable`
// declarations for cross-directory deduplication purposes. A variable
// with a validation block (on either side) is always treated as unequal
// here: validations aren't compared at all, since a folded-in directory's
// own validation-bearing variable never reaches this comparison in the
// first place (see appendFile's pre-existing "cannot be disambiguated"
// scope limit), and conflating a validated declaration with an
// unvalidated one of the same name is exactly the kind of mismatch that
// should surface as a conflict, not a silent dedup.
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
// blocks/traversals here), and the value expression itself must be
// statically equal per hclExprEqual - an output whose value depends on a
// variable, local, or resource is never considered a match, even against
// a textually identical copy, since each directory's own references
// (e.g. a namespaced variable) may make two identical-looking expressions
// resolve differently.
func outputsEqual(a, b *Output) bool {
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
	if len(a.Preconditions) != 0 || len(b.Preconditions) != 0 {
		return false
	}
	if len(a.DependsOn) != 0 || len(b.DependsOn) != 0 {
		return false
	}
	return hclExprEqual(a.Expr, b.Expr)
}

// dataResourcesEqual reports whether a and b are equivalent `data`
// resource declarations for cross-directory deduplication purposes. Only
// the Config body and the count/for_each/enabled meta-arguments are
// compared; a data resource with any depends_on or precondition/
// postcondition, on either side, is conservatively treated as unequal
// (same rationale as outputsEqual: not worth comparing check blocks/
// traversals here).
func dataResourcesEqual(a, b *Resource) bool {
	if !providerConfigsEqual(a.Config, b.Config) {
		return false
	}
	if !hclExprEqual(a.Count, b.Count) {
		return false
	}
	if !hclExprEqual(a.ForEach, b.ForEach) {
		return false
	}
	if !hclExprEqual(a.Enabled, b.Enabled) {
		return false
	}
	if len(a.Preconditions) != 0 || len(b.Preconditions) != 0 {
		return false
	}
	if len(a.Postconditions) != 0 || len(b.Postconditions) != 0 {
		return false
	}
	if len(a.DependsOn) != 0 || len(b.DependsOn) != 0 {
		return false
	}
	if (a.ProviderConfigRef == nil) != (b.ProviderConfigRef == nil) {
		return false
	}
	if a.ProviderConfigRef != nil && (a.ProviderConfigRef.Name != b.ProviderConfigRef.Name || a.ProviderConfigRef.Alias != b.ProviderConfigRef.Alias) {
		return false
	}
	return true
}
