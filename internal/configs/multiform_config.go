package configs

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
)

// This file reads multiform's state-handling directives (use_states and
// preferred_state - see Module.UseStates/Module.PreferredState) from
// environment variables, rather than from a file - these are unconditional,
// live overrides read fresh on every invocation, the same way Terraform's
// own TF_VAR_* variables are: there is no snapshotting of their values into
// a saved plan file, so if they differ between `plan -out=` and a later
// `apply <planfile>`, the resulting plan mismatch is caught by the normal
// "saved plan doesn't match the current configuration" staleness check, the
// same as it would be for any other configuration change made in between.
// This is deliberate: switching between use_states/preferred_state values is
// an operator-driven migration/troubleshooting workflow, not something this
// tool should try to track or validate across runs on its own.
//
// internal/command/arguments sets these same environment variables from
// equivalent CLI flags (-use-states, -preferred-state), so a human caller
// can set either; terraform-provider-multiform sets them directly as
// subprocess environment variables when driving multiform as its inner
// binary.

const (
	UseStatesEnvVar      = "MULTIFORM_USE_STATES"
	PreferredStateEnvVar = "MULTIFORM_PREFERRED_STATE"
)

// Recognized values for the use_states directive: which backend(s) get
// written to on apply.
const (
	UseStatesGlobal         = "global"          // only the root's own backend (pre-multiform behavior)
	UseStatesLocal          = "local"           // only each folded-in directory's own backend
	UseStatesGlobalAndLocal = "global_and_local" // both
)

// Recognized values for the preferred_state directive: which state is read
// as the planning baseline.
const (
	PreferredStateGlobal = "global" // the root's own backend's state (pre-multiform behavior)
	PreferredStateLocal  = "local"  // the state assembled from folded-in directories' own backends
)

// multiformConfig returns the current values of multiform's use_states and
// preferred_state directives, read from MULTIFORM_USE_STATES and
// MULTIFORM_PREFERRED_STATE. If either is unset, that directive's default
// ("global") is returned unchanged.
func (p *Parser) multiformConfig() (useStates, preferredState string, diags hcl.Diagnostics) {
	useStates = UseStatesGlobal
	preferredState = PreferredStateGlobal

	if v := os.Getenv(UseStatesEnvVar); v != "" {
		switch v {
		case UseStatesGlobal, UseStatesLocal, UseStatesGlobalAndLocal:
			useStates = v
		default:
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid " + UseStatesEnvVar + " value",
				Detail:   fmt.Sprintf("%s must be one of %q, %q, or %q, got %q.", UseStatesEnvVar, UseStatesGlobal, UseStatesLocal, UseStatesGlobalAndLocal, v),
			})
		}
	}

	if v := os.Getenv(PreferredStateEnvVar); v != "" {
		switch v {
		case PreferredStateGlobal, PreferredStateLocal:
			preferredState = v
		default:
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid " + PreferredStateEnvVar + " value",
				Detail:   fmt.Sprintf("%s must be one of %q or %q, got %q.", PreferredStateEnvVar, PreferredStateGlobal, PreferredStateLocal, v),
			})
		}
	}

	// preferred_state names which state is read as the planning baseline;
	// use_states names which backend(s) get written on apply. If the one
	// preferred_state reads from isn't among the ones use_states writes
	// to, that state can never be updated by an apply made under this
	// same configuration, so it will only ever reflect whatever was last
	// written under a different configuration (or nothing at all). That's
	// rejected outright rather than allowed to silently drift.
	switch {
	case preferredState == PreferredStateGlobal && useStates == UseStatesLocal:
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "preferred_state and use_states are inconsistent",
			Detail:   fmt.Sprintf("%s = %q reads from the root backend's own state, but %s = %q never writes to it, so it would never reflect the results of an apply made under this configuration.", PreferredStateEnvVar, PreferredStateGlobal, UseStatesEnvVar, UseStatesLocal),
		})
	case preferredState == PreferredStateLocal && useStates == UseStatesGlobal:
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "preferred_state and use_states are inconsistent",
			Detail:   fmt.Sprintf("%s = %q reads from each folded-in directory's own backend, but %s = %q never writes to them, so they would never reflect the results of an apply made under this configuration.", PreferredStateEnvVar, PreferredStateLocal, UseStatesEnvVar, UseStatesGlobal),
		})
	}

	return useStates, preferredState, diags
}
