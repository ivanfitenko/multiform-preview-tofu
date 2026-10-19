// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0

package arguments

import (
	"os"
	"strings"

	"github.com/opentofu/opentofu/internal/configs"
	"github.com/opentofu/opentofu/internal/tfdiags"
)

// Multiform describes the CLI flags for multiform's own state-handling and
// directory-folding directives: which directories to fold in (see
// configs.IncludesEnvVar), and which backend(s) state is read from/written
// to (see configs.UseStatesEnvVar/PreferredStateEnvVar). These directives
// are read, ultimately, from environment variables rather than as ordinary
// parsed struct fields threaded through the call chain the way
// State/Operation/Vars are: the pre-hook registered by BindMultiform, run
// after the command line has been parsed, sets the corresponding
// environment variable for the remainder of this process - the same thing
// a caller could do directly by setting the env var themselves - so that
// configs.Parser's multiformConfig/includedDirs (which read those env vars
// directly) see them uniformly regardless of whether they came from a flag
// or a real environment variable already in this process's environment. A
// flag left unset (empty string / empty slice) leaves any existing
// environment variable of the same name untouched, rather than clearing it.
type Multiform struct {
	useStatesRaw      string
	preferredStateRaw string
	includesRaw       []string
}

// BindMultiform registers multiform's own flags on cli, and a pre-hook that
// applies them to the process environment once parsing has finished.
func BindMultiform(cli *CommandLine) *Multiform {
	m := &Multiform{}
	cli.StringVar(&m.useStatesRaw, "use-states", "",
		`Which backend(s) state is written to: "global" (this directory's own backend), "local" (each included directory's own backend) or "global_and_local".`,
	).SetDisplay("=mode")
	cli.StringVar(&m.preferredStateRaw, "preferred-state", "",
		`Which backend's state is read as the planning baseline: "global" or "local". Must be consistent with -use-states.`,
	).SetDisplay("=mode")
	cli.StringArrayVar(&m.includesRaw, "includes", nil,
		`A subdirectory, relative to the working directory, to fold into the configuration. Use this option more than once to fold in more than one directory.`,
	).SetDisplay("=dir")
	cli.PreHook(func() tfdiags.Diagnostics {
		m.apply()
		return nil
	})
	return m
}

// apply sets configs.UseStatesEnvVar/PreferredStateEnvVar/IncludesEnvVar
// from any of these flags that were actually given.
func (m *Multiform) apply() {
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
