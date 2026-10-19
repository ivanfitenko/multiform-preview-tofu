// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0

package arguments

import (
	"flag"
	"os"
	"strings"

	"github.com/opentofu/opentofu/internal/command/flags"
	"github.com/opentofu/opentofu/internal/configs"
)

// Multiform describes the CLI flags for multiform's own state-handling and
// directory-folding directives: which directories to fold in (see
// configs.IncludesEnvVar), and which backend(s) state is read from/written
// to (see configs.UseStatesEnvVar/PreferredStateEnvVar). These directives
// are read, ultimately, from environment variables rather than as ordinary
// parsed struct fields threaded through the call chain the way
// State/Operation/Vars are: Apply, called after the flag set has parsed
// args, sets the corresponding environment variable for the remainder of
// this process - the same thing a caller could do directly by setting the
// env var themselves - so that configs.Parser's multiformConfig/
// includedDirs (which read those env vars directly) see them uniformly
// regardless of whether they came from a flag or a real environment
// variable already in this process's environment. A flag left unset
// (empty string / empty slice) leaves any existing environment variable of
// the same name untouched, rather than clearing it.
type Multiform struct {
	useStatesRaw      string
	preferredStateRaw string
	includesRaw       []string
}

// AddFlags registers multiform's own flags on f.
func (m *Multiform) AddFlags(f *flag.FlagSet) {
	f.StringVar(&m.useStatesRaw, "use-states", "", "use-states")
	f.StringVar(&m.preferredStateRaw, "preferred-state", "", "preferred-state")
	f.Var((*flags.FlagStringSlice)(&m.includesRaw), "includes", "includes")
}

// Apply sets configs.UseStatesEnvVar/PreferredStateEnvVar/IncludesEnvVar
// from any of these flags that were actually given. Call after the
// owning flag.FlagSet has parsed its arguments.
func (m *Multiform) Apply() {
	if m.useStatesRaw != "" {
		os.Setenv(configs.UseStatesEnvVar, m.useStatesRaw)
	}
	if m.preferredStateRaw != "" {
		os.Setenv(configs.PreferredStateEnvVar, m.preferredStateRaw)
	}
	if len(m.includesRaw) > 0 {
		os.Setenv(configs.IncludesEnvVar, strings.Join(m.includesRaw, "\n"))
	}
}
