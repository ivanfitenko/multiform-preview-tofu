package multiformprovider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// planPrivateKey is the key used to stash a computed plan file's raw
// bytes between ModifyPlan and Update, so apply runs the exact plan that
// was shown - not a freshly recomputed one.
const planPrivateKey = "planfile"

func NewStackResource() resource.Resource {
	return &StackResource{}
}

// StackResource is the multiform_stack resource: one includes.conf-rooted
// directory tree, managed as a single resource by shelling out to the
// bundled multiform binary. See ModifyPlan/Create/Update/Delete for the
// init/plan/apply/destroy mapping.
type StackResource struct {
	binaryPath string
}

type stackResourceModel struct {
	RootDir        types.String `tfsdk:"root_dir"`
	UseStates      types.String `tfsdk:"use_states"`
	PreferredState types.String `tfsdk:"preferred_state"`
	Includes       types.List   `tfsdk:"includes"`
	Plan           types.String `tfsdk:"plan"`
	Id             types.String `tfsdk:"id"`
}

// multiformFlags translates m's use_states/preferred_state/includes
// attributes (mirroring multiform's own -use-states/-preferred-state/
// -includes CLI flags one-for-one - see
// internal/command/arguments/multiform.go) into the equivalent flags for
// the inner multiform subprocess. Each is included only if actually set
// on the resource; left unset, multiform falls back to its own default
// (global/global/no folded directories), exactly as it would standalone.
func multiformFlags(ctx context.Context, m *stackResourceModel) ([]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	var flags []string

	if !m.UseStates.IsNull() && !m.UseStates.IsUnknown() {
		flags = append(flags, "-use-states="+m.UseStates.ValueString())
	}
	if !m.PreferredState.IsNull() && !m.PreferredState.IsUnknown() {
		flags = append(flags, "-preferred-state="+m.PreferredState.ValueString())
	}
	if !m.Includes.IsNull() && !m.Includes.IsUnknown() {
		var includes []string
		diags.Append(m.Includes.ElementsAs(ctx, &includes, false)...)
		for _, inc := range includes {
			flags = append(flags, "-includes="+inc)
		}
	}

	return flags, diags
}

func (r *StackResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stack"
}

func (r *StackResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an entire multiform-folded directory tree (one that already has its own " +
			"backend.tf on disk) as a single resource. WARNING: deleting this resource runs " +
			"`multiform destroy` against root_dir - it tears down everything multiform manages there, " +
			"not an inert wrapper object.",
		Attributes: map[string]schema.Attribute{
			"root_dir": schema.StringAttribute{
				Required:    true,
				Description: "Path to the multiform-managed directory tree.",
			},
			"use_states": schema.StringAttribute{
				Optional: true,
				Description: "Mirrors multiform's own -use-states flag (\"global\", \"local\", or " +
					"\"global_and_local\") - which backend(s) get written to on apply. Left unset, " +
					"multiform defaults to \"global\". The global role's own bytes always live stashed " +
					"in this resource's own state, not in a separate file under root_dir - see the " +
					"provider's own docs.",
			},
			"preferred_state": schema.StringAttribute{
				Optional: true,
				Description: "Mirrors multiform's own -preferred-state flag (\"global\" or \"local\") - " +
					"which state is read as the planning baseline. Left unset, multiform defaults to " +
					"\"global\".",
			},
			"includes": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "Mirrors multiform's own -includes flag - directories (relative to " +
					"root_dir) whose configuration should implicitly be folded in. Left unset, no " +
					"directories are folded in.",
			},
			"plan": schema.StringAttribute{
				Computed: true,
				Sensitive: true,
				Description: "Rendered text of the last meaningful inner plan (left unchanged if the " +
					"last refresh found no real changes).",
			},
			"id": schema.StringAttribute{
				Computed: true,
			},
		},
	}
}

func (r *StackResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *providerData, got %T. This is a provider bug.", req.ProviderData),
		)
		return
	}
	r.binaryPath = data.binaryPath
}

// ModifyPlan computes the real inner plan - it also runs again,
// automatically, right before apply (see resource.ResourceWithModifyPlan's
// own docs) - and decides whether the outer resource should show a diff
// at all: Terraform Core only ever compares the attribute values a
// provider returns against prior state, so "no real inner changes" has
// to mean "return prior state completely unchanged," not a separately
// reported count.
func (r *StackResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		// Destroy plan - nothing to adjust, Delete handles it.
		return
	}

	var planned stackResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var prior *stackResourceModel
	if !req.State.Raw.IsNull() {
		var p stackResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &p)...)
		if resp.Diagnostics.HasError() {
			return
		}
		prior = &p
	}

	rootDir := planned.RootDir.ValueString()

	mfFlags, mfDiags := multiformFlags(ctx, &planned)
	resp.Diagnostics.Append(mfDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	encodedGlobalState, diags := req.Private.GetKey(ctx, globalStatePrivateKey)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	globalStatePath, cleanupGlobalState, err := stageGlobalState(encodedGlobalState)
	if err != nil {
		resp.Diagnostics.AddError("Unable to stage the stashed global state", err.Error())
		return
	}
	defer cleanupGlobalState()
	extraEnv := map[string]string{globalStateFileEnvVar: globalStatePath}

	if _, err := run(ctx, r.binaryPath, rootDir, extraEnv, append([]string{"init", "-input=false"}, mfFlags...)...); err != nil {
		resp.Diagnostics.AddError("multiform init failed", err.Error())
		return
	}

	planFile, err := os.CreateTemp("", "multiform-plan-*.tfplan")
	if err != nil {
		resp.Diagnostics.AddError("Unable to create a temporary plan file", err.Error())
		return
	}
	planFileName := planFile.Name()
	planFile.Close()
	defer os.Remove(planFileName)

	if _, err := run(ctx, r.binaryPath, rootDir, extraEnv, append([]string{"plan", "-input=false", "-out=" + planFileName}, mfFlags...)...); err != nil {
		resp.Diagnostics.AddError("multiform plan failed", err.Error())
		return
	}

	jsonResult, err := run(ctx, r.binaryPath, rootDir, extraEnv, append(append([]string{"show", "-json"}, mfFlags...), planFileName)...)
	if err != nil {
		resp.Diagnostics.AddError("multiform show -json failed", err.Error())
		return
	}

	changeCount, err := countRealChanges([]byte(jsonResult.Stdout))
	if err != nil {
		resp.Diagnostics.AddError("Unable to parse the inner plan's JSON output", err.Error())
		return
	}

	if changeCount == 0 && prior != nil {
		resp.Diagnostics.Append(resp.Plan.Set(ctx, prior)...)
		// There is nothing real to apply, so clear any plan stashed by an
		// earlier ModifyPlan call: otherwise an Update triggered purely by
		// an unrelated attribute change (e.g. root_dir's own resources are
		// untouched, but use_states/preferred_state/includes changed) would
		// have Update replay that stale, unrelated plan file instead of
		// correctly being a no-op.
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, planPrivateKey, nil)...)
		r.restashGlobalState(ctx, resp.Private, globalStatePath, &resp.Diagnostics)
		return
	}

	textResult, err := run(ctx, r.binaryPath, rootDir, extraEnv, append(append([]string{"show", "-no-color"}, mfFlags...), planFileName)...)
	if err != nil {
		resp.Diagnostics.AddError("multiform show failed", err.Error())
		return
	}
	planBytes, err := os.ReadFile(planFileName)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the computed plan file", err.Error())
		return
	}

	planned.Plan = types.StringValue(textResult.Stdout)
	if prior != nil {
		planned.Id = prior.Id
	} else {
		planned.Id = types.StringValue(rootDir)
	}

	resp.Diagnostics.Append(resp.Plan.Set(ctx, planned)...)
	// Private state must be valid JSON (and therefore valid UTF-8), but a
	// plan file's raw bytes are a binary zip archive - so stash it as a
	// base64 string, JSON-encoded (a quoted JSON string), not raw.
	encodedPlan, err := json.Marshal(base64.StdEncoding.EncodeToString(planBytes))
	if err != nil {
		resp.Diagnostics.AddError("Unable to encode the computed plan file for private state", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, planPrivateKey, encodedPlan)...)
	r.restashGlobalState(ctx, resp.Private, globalStatePath, &resp.Diagnostics)
}

// restashGlobalState re-encodes whatever is now at globalStatePath (which
// init/plan may not have changed at all - plan doesn't persist state on
// its own) and stashes it via setter.SetKey, so the next call (another
// ModifyPlan, or Update) sees the latest content. A privateSetter is
// satisfied by both *privatestate.ProviderData itself and anything with
// the same SetKey signature - framework types already match this
// structurally.
func (r *StackResource) restashGlobalState(ctx context.Context, setter privateSetter, globalStatePath string, diags *diag.Diagnostics) {
	encoded, err := collectGlobalState(globalStatePath)
	if err != nil {
		diags.AddError("Unable to read back the global state for stashing", err.Error())
		return
	}
	if encoded == nil {
		return
	}
	diags.Append(setter.SetKey(ctx, globalStatePrivateKey, encoded)...)
}

// privateSetter is the subset of privatestate.ProviderData's API that
// restashGlobalState needs - letting it accept the *privatestate.ProviderData
// held by any of ModifyPlanResponse/CreateResponse/UpdateResponse.Private
// without this package needing to import privatestate directly.
type privateSetter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// Create runs on the very first apply of a given multiform_stack resource.
// The plan/apply plugin protocol doesn't carry private state into Create
// (there is, by design, no prior private state for a brand-new resource
// instance) - so unlike Update, Create always plans-and-applies in one
// step rather than applying a plan stashed earlier. There is likewise no
// prior stashed global state to seed from: a brand-new resource means a
// brand-new root_dir, so the global role (if used_states/preferred_state
// even consult it) starts fresh, the same as it would standalone.
func (r *StackResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan stackResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rootDir := plan.RootDir.ValueString()

	mfFlags, mfDiags := multiformFlags(ctx, &plan)
	resp.Diagnostics.Append(mfDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	globalStatePath, cleanupGlobalState, err := stageGlobalState(nil)
	if err != nil {
		resp.Diagnostics.AddError("Unable to stage the global state", err.Error())
		return
	}
	defer cleanupGlobalState()
	extraEnv := map[string]string{globalStateFileEnvVar: globalStatePath}

	if _, err := run(ctx, r.binaryPath, rootDir, extraEnv, append([]string{"apply", "-input=false", "-auto-approve"}, mfFlags...)...); err != nil {
		resp.Diagnostics.AddError("multiform apply failed", err.Error())
		return
	}

	if plan.Id.IsUnknown() || plan.Id.IsNull() {
		plan.Id = types.StringValue(rootDir)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	r.restashGlobalState(ctx, resp.Private, globalStatePath, &resp.Diagnostics)
}

// Update applies the exact plan file ModifyPlan computed and stashed via
// private state - no re-planning, no gap between what was shown and what
// gets applied.
func (r *StackResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan stackResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rootDir := plan.RootDir.ValueString()

	mfFlags, mfDiags := multiformFlags(ctx, &plan)
	resp.Diagnostics.Append(mfDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	encodedPlan, diags := req.Private.GetKey(ctx, planPrivateKey)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var planBytes []byte
	if len(encodedPlan) > 0 {
		var b64 string
		if err := json.Unmarshal(encodedPlan, &b64); err != nil {
			resp.Diagnostics.AddError("Unable to decode the stashed plan file", err.Error())
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			resp.Diagnostics.AddError("Unable to decode the stashed plan file", err.Error())
			return
		}
		planBytes = decoded
	}

	encodedGlobalState, diags := req.Private.GetKey(ctx, globalStatePrivateKey)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	globalStatePath, cleanupGlobalState, err := stageGlobalState(encodedGlobalState)
	if err != nil {
		resp.Diagnostics.AddError("Unable to stage the stashed global state", err.Error())
		return
	}
	defer cleanupGlobalState()
	extraEnv := map[string]string{globalStateFileEnvVar: globalStatePath}

	if len(planBytes) == 0 {
		// Shouldn't normally happen (ModifyPlan always stashes a plan
		// when it reports a real change) - fall back rather than fail.
		if _, err := run(ctx, r.binaryPath, rootDir, extraEnv, append([]string{"apply", "-input=false", "-auto-approve"}, mfFlags...)...); err != nil {
			resp.Diagnostics.AddError("multiform apply failed", err.Error())
			return
		}
	} else {
		planFile, err := os.CreateTemp("", "multiform-apply-*.tfplan")
		if err != nil {
			resp.Diagnostics.AddError("Unable to create a temporary plan file", err.Error())
			return
		}
		planFileName := planFile.Name()
		_, writeErr := planFile.Write(planBytes)
		planFile.Close()
		defer os.Remove(planFileName)
		if writeErr != nil {
			resp.Diagnostics.AddError("Unable to write the stashed plan file", writeErr.Error())
			return
		}

		if _, err := run(ctx, r.binaryPath, rootDir, extraEnv, append(append([]string{"apply", "-input=false"}, mfFlags...), planFileName)...); err != nil {
			resp.Diagnostics.AddError("multiform apply failed", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	r.restashGlobalState(ctx, resp.Private, globalStatePath, &resp.Diagnostics)
}

// Read is a deliberate passthrough: all real drift detection already
// happens in ModifyPlan (running it again here would mean computing a
// potentially large inner plan twice per outer plan cycle for no benefit).
func (r *StackResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state stackResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete runs `multiform destroy` against root_dir - this tears down
// everything multiform manages there. There's no "next call" to re-stash
// the global state for afterward, since the resource is going away.
func (r *StackResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state stackResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rootDir := state.RootDir.ValueString()

	mfFlags, mfDiags := multiformFlags(ctx, &state)
	resp.Diagnostics.Append(mfDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	encodedGlobalState, diags := req.Private.GetKey(ctx, globalStatePrivateKey)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	globalStatePath, cleanupGlobalState, err := stageGlobalState(encodedGlobalState)
	if err != nil {
		resp.Diagnostics.AddError("Unable to stage the stashed global state", err.Error())
		return
	}
	defer cleanupGlobalState()
	extraEnv := map[string]string{globalStateFileEnvVar: globalStatePath}

	if _, err := run(ctx, r.binaryPath, rootDir, extraEnv, append([]string{"destroy", "-input=false", "-auto-approve"}, mfFlags...)...); err != nil {
		resp.Diagnostics.AddError("multiform destroy failed", err.Error())
		return
	}
}
