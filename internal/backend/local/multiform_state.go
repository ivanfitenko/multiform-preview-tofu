package local

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/opentofu/internal/backend"
	"github.com/opentofu/opentofu/internal/configs"
	"github.com/opentofu/opentofu/internal/encryption"
	"github.com/opentofu/opentofu/internal/states"
	"github.com/opentofu/opentofu/internal/states/statemgr"
	"github.com/opentofu/opentofu/internal/tfdiags"
	"github.com/opentofu/opentofu/internal/tofu"
)

// instantiateKnownBackend builds and configures a backend.Backend for one
// of a module's KnownBackends entries (a backend block folded in from an
// includes.conf-included directory), so its own state can be read or
// written independently of the primary run backend. See
// configs.Module's UseStates/PreferredState fields for the multiform
// state-handling modes that need this.
//
// resolver resolves a backend type name to its constructor, the same way
// internal/backend/init's registry does - it's threaded in via
// backend.Operation.BackendResolver rather than looked up directly here,
// since that registry imports this package (to register the "local" type
// itself) and so can never be imported back from here.
func instantiateKnownBackend(ctx context.Context, knownBackend *configs.Backend, resolver func(name string) (backend.InitFn, string), enc encryption.StateEncryption) (backend.Backend, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	if resolver == nil {
		diags = diags.Append(fmt.Errorf(
			"cannot instantiate the backend declared at %s for multiform local state handling: no backend resolver is available",
			knownBackend.DeclRange,
		))
		return nil, diags
	}

	f, canonType := resolver(knownBackend.Type)
	if f == nil {
		diags = diags.Append(fmt.Errorf("unsupported backend type %q for the backend declared at %s", knownBackend.Type, knownBackend.DeclRange))
		return nil, diags
	}
	if knownBackend.Type != canonType {
		diags = diags.Append(fmt.Errorf("backend configuration still contains alias type %q instead of canonical %q; this is a bug in OpenTofu", knownBackend.Type, canonType))
		return nil, diags
	}

	b := f(enc)

	schema := b.ConfigSchema()
	configVal, hclDiags := knownBackend.Decode(ctx, schema.NoneRequired())
	diags = diags.Append(hclDiags)
	if hclDiags.HasErrors() {
		return nil, diags
	}

	if !configVal.IsWhollyKnown() {
		diags = diags.Append(tfdiags.Sourceless(
			tfdiags.Error,
			"Unknown values within backend definition",
			fmt.Sprintf("The backend configuration declared at %s should contain only concrete and static values.", knownBackend.DeclRange),
		))
		return nil, diags
	}

	if knownBackend.Type == "local" {
		// A "local" backend's "path" is conventionally written relative to
		// its own declaring directory (see canonicalBackendKey in
		// remote_state_resolution.go, which resolves it the same way for
		// comparison purposes). We're about to Configure this backend from
		// a process potentially running in a different directory, so it
		// must be resolved to be relative to that directory rather than
		// left relative to the process's current directory.
		configVal = resolveLocalBackendPath(configVal, filepath.Dir(knownBackend.DeclRange.Filename))
	}

	newVal, validateDiags := b.PrepareConfig(configVal)
	diags = diags.Append(validateDiags.InConfigBody(knownBackend.Config, ""))
	if validateDiags.HasErrors() {
		return nil, diags
	}

	configureDiags := b.Configure(ctx, newVal)
	diags = diags.Append(configureDiags.InConfigBody(knownBackend.Config, ""))
	if configureDiags.HasErrors() {
		return nil, diags
	}

	return b, diags
}

// resolveLocalBackendPath returns configVal with its "path" attribute (if
// present, non-null, and not already absolute) rewritten to be resolved
// against baseDir. Any other shape of configVal is returned unchanged.
func resolveLocalBackendPath(configVal cty.Value, baseDir string) cty.Value {
	if configVal.IsNull() || !configVal.Type().IsObjectType() || !configVal.Type().HasAttribute("path") {
		return configVal
	}

	pathVal := configVal.GetAttr("path")
	if pathVal.IsNull() || pathVal.Type() != cty.String {
		return configVal
	}

	path := pathVal.AsString()
	if path == "" || filepath.IsAbs(path) {
		return configVal
	}

	attrs := configVal.AsValueMap()
	attrs["path"] = cty.StringVal(filepath.Join(baseDir, path))
	return cty.ObjectVal(attrs)
}

// mergeKnownBackendStates reads each of mod's KnownBackends' own current
// state via its own backend, and unions their root-module resources and
// output values into a single states.State. This is the "local"
// preferred_state planning baseline (see configs.Module.PreferredState):
// since folded-in directories are merged into one flat module (no module
// nesting - that's the whole premise of multiform), each per-directory
// state's own root-module content can be inserted directly into the
// result's root module, with no address rewriting needed. Resource address
// collisions between directories can't happen here: they would already
// have been rejected as a "Duplicate resource configuration" error when
// the directories' configs were merged (see Module.appendFile).
//
// This performs unlocked reads of each per-directory backend's state,
// unlike the primary run backend (which op.StateLocker locks around the
// whole operation) - concurrent writers to a folded-in directory's own
// backend could in principle be observed mid-write. That's a narrow,
// currently-accepted gap in this prototype-stage feature, not a deliberate
// design choice.
func mergeKnownBackendStates(ctx context.Context, op *backend.Operation, mod *configs.Module, enc encryption.StateEncryption) (*states.State, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	merged := states.NewState()
	mergedRoot := merged.RootModule()

	for _, knownBackend := range mod.KnownBackends {
		dirBackend, kbDiags := instantiateKnownBackend(ctx, knownBackend, op.BackendResolver, enc)
		diags = diags.Append(kbDiags)
		if kbDiags.HasErrors() {
			continue
		}

		dirStateMgr, err := dirBackend.StateMgr(ctx, op.Workspace)
		if err != nil {
			diags = diags.Append(fmt.Errorf("error loading state for the backend declared at %s: %w", knownBackend.DeclRange, err))
			continue
		}

		dirState, err := statemgr.RefreshAndRead(ctx, dirStateMgr)
		if err != nil {
			diags = diags.Append(fmt.Errorf("error reading state for the backend declared at %s: %w", knownBackend.DeclRange, err))
			continue
		}
		if dirState == nil {
			continue
		}

		dirRoot := dirState.RootModule()
		if dirRoot == nil {
			continue
		}

		for key, resource := range dirRoot.Resources {
			mergedRoot.Resources[key] = resource
		}
		for name, output := range dirRoot.OutputValues {
			mergedRoot.OutputValues[name] = output
		}
	}

	return merged, diags
}

// writeKnownBackendStates splits applyState by which includes.conf-included
// directory (if any) each of its resources and output values was declared
// in, and writes each directory's own subset to that directory's own
// backend - the "local" use_states mode (see configs.Module.UseStates).
// Provenance is recovered the same way variable_namespacing.go and
// remote_state_resolution.go already do: by looking up each state object's
// declaring *configs.Resource/*configs.Output and taking the directory of
// its DeclRange.Filename, since folded-in directories are merged into one
// flat module with no module-path distinction of their own.
//
// Resources/outputs belonging to the root directory itself, or to any
// directory without a known backend, are left out of every per-directory
// write - they exist only in the global merged state, if that's also being
// written (see use_states = "global_and_local").
//
// A write failure for one directory is reported as a diagnostic but does
// not prevent attempting the remaining directories; unlike the primary
// backend's write (see opApply), there is currently no local backup-file
// fallback for a failed per-directory write, since this is a
// prototype-stage feature - a failure here means that directory's own
// state may be out of sync until the next successful apply.
func writeKnownBackendStates(ctx context.Context, op *backend.Operation, mod *configs.Module, applyState *states.State, schemas *tofu.Schemas, enc encryption.StateEncryption) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if len(mod.KnownBackends) == 0 {
		return diags
	}

	backendsByDir := make(map[string]*configs.Backend, len(mod.KnownBackends))
	for _, knownBackend := range mod.KnownBackends {
		backendsByDir[filepath.Dir(knownBackend.DeclRange.Filename)] = knownBackend
	}

	dirStates := make(map[string]*states.State, len(backendsByDir))
	for dir := range backendsByDir {
		dirStates[dir] = states.NewState()
	}

	if root := applyState.RootModule(); root != nil {
		for key, resource := range root.Resources {
			configResource := mod.ResourceByAddr(resource.Addr.Resource)
			if configResource == nil {
				continue
			}
			if dirState, ok := dirStates[filepath.Dir(configResource.DeclRange.Filename)]; ok {
				dirState.RootModule().Resources[key] = resource
			}
		}
		for name, output := range root.OutputValues {
			configOutput, ok := mod.Outputs[name]
			if !ok {
				continue
			}
			if dirState, ok := dirStates[filepath.Dir(configOutput.DeclRange.Filename)]; ok {
				dirState.RootModule().OutputValues[name] = output
			}
		}
	}

	for dir, knownBackend := range backendsByDir {
		dirBackend, kbDiags := instantiateKnownBackend(ctx, knownBackend, op.BackendResolver, enc)
		diags = diags.Append(kbDiags)
		if kbDiags.HasErrors() {
			continue
		}

		dirStateMgr, err := dirBackend.StateMgr(ctx, op.Workspace)
		if err != nil {
			diags = diags.Append(fmt.Errorf("error loading state for the backend declared at %s: %w", knownBackend.DeclRange, err))
			continue
		}

		if err := statemgr.WriteAndPersist(ctx, dirStateMgr, dirStates[dir], schemas); err != nil {
			diags = diags.Append(fmt.Errorf(
				"error writing local state for directory %q (backend declared at %s): %w; this directory's own state may now be out of sync and should be re-applied",
				dir, knownBackend.DeclRange, err,
			))
			continue
		}
	}

	return diags
}
