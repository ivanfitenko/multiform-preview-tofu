package configs

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// This file reads config.cfg, a temporary, debugging-only configuration
// mechanism for multiform's state-handling directives (use_states and
// preferred_state - see Module.UseStates/Module.PreferredState). Its
// format and location are expected to change before release; nothing
// downstream should assume config.cfg itself is a stable interface.

// multiformConfigFile is the name of the file, if present in a
// configuration directory, that carries multiform's use_states and
// preferred_state directives.
const multiformConfigFile = "config.cfg"

// Recognized values for the use_states directive: which backend(s) get
// written to on apply.
const (
	UseStatesGlobal         = "global"           // only the root's own backend (pre-multiform behavior)
	UseStatesLocal          = "local"             // only each folded-in directory's own backend
	UseStatesGlobalAndLocal = "global_and_local"  // both
)

// Recognized values for the preferred_state directive: which state is read
// as the planning baseline.
const (
	PreferredStateGlobal = "global" // the root's own backend's state (pre-multiform behavior)
	PreferredStateLocal  = "local"  // the state assembled from folded-in directories' own backends
)

// multiformConfig reads dir's config.cfg file, if any, and returns the
// values of its use_states and preferred_state directives. If config.cfg
// does not exist, or a directive is absent from it, that directive's
// default ("global") is returned unchanged. Blank lines and lines starting
// with '#' are ignored; every other line must be of the form
// "key = value".
func (p *Parser) multiformConfig(dir string) (useStates, preferredState string, diags hcl.Diagnostics) {
	useStates = UseStatesGlobal
	preferredState = PreferredStateGlobal

	configPath := filepath.Join(dir, multiformConfigFile)
	raw, err := p.fs.ReadFile(configPath)
	if err != nil {
		return useStates, preferredState, diags
	}

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid config.cfg directive",
				Detail:   fmt.Sprintf("%s: expected a line of the form \"key = value\", got: %q.", configPath, line),
			})
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}

		switch key {
		case "use_states":
			switch value {
			case UseStatesGlobal, UseStatesLocal, UseStatesGlobalAndLocal:
				useStates = value
			default:
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid use_states value",
					Detail:   fmt.Sprintf("%s: use_states must be one of %q, %q, or %q, got %q.", configPath, UseStatesGlobal, UseStatesLocal, UseStatesGlobalAndLocal, value),
				})
			}
		case "preferred_state":
			switch value {
			case PreferredStateGlobal, PreferredStateLocal:
				preferredState = value
			default:
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid preferred_state value",
					Detail:   fmt.Sprintf("%s: preferred_state must be one of %q or %q, got %q.", configPath, PreferredStateGlobal, PreferredStateLocal, value),
				})
			}
		default:
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unrecognized config.cfg directive",
				Detail:   fmt.Sprintf("%s: unrecognized directive %q.", configPath, key),
			})
		}
	}

	// preferred_state names which state is read as the planning baseline;
	// use_states names which backend(s) get written on apply. If the one
	// preferred_state reads from isn't among the ones use_states writes
	// to, that state can never be updated by an apply made under this
	// same config.cfg, so it will only ever reflect whatever was last
	// written under a different configuration (or nothing at all). That's
	// rejected outright rather than allowed to silently drift.
	switch {
	case preferredState == PreferredStateGlobal && useStates == UseStatesLocal:
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "preferred_state and use_states are inconsistent",
			Detail:   fmt.Sprintf("%s: preferred_state = %q reads from the root backend's own state, but use_states = %q never writes to it, so it would never reflect the results of an apply made under this configuration.", configPath, PreferredStateGlobal, UseStatesLocal),
		})
	case preferredState == PreferredStateLocal && useStates == UseStatesGlobal:
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "preferred_state and use_states are inconsistent",
			Detail:   fmt.Sprintf("%s: preferred_state = %q reads from each folded-in directory's own backend, but use_states = %q never writes to them, so they would never reflect the results of an apply made under this configuration.", configPath, PreferredStateLocal, UseStatesGlobal),
		})
	}

	return useStates, preferredState, diags
}
