package multiformprovider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// multiformBinaryEnvVar overrides where the bundled multiform (patched
// tofu) binary is found. Takes precedence over the sibling-file lookup
// but not over the provider's own explicit tofu_binary attribute.
const multiformBinaryEnvVar = "MULTIFORM_TOFU_BINARY"

// New returns a factory function suitable for providerserver.Serve.
func New() provider.Provider {
	return &MultiformProvider{}
}

// MultiformProvider is the top-level `multiform` provider. Its only job
// is to resolve, once, the path to the bundled multiform binary that
// every multiform_stack resource then shells out to - see resolveBinaryPath.
type MultiformProvider struct{}

type providerModel struct {
	TofuBinary types.String `tfsdk:"tofu_binary"`
}

// providerData is handed to every resource via resp.ResourceData.
type providerData struct {
	binaryPath string
}

func (p *MultiformProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "multiform"
}

func (p *MultiformProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Runs multiform (a patched OpenTofu build) against includes.conf-rooted directory trees, one resource per tree. The outer Terraform/OpenTofu running this provider does not itself need any multiform patches - only the binary this provider bundles does.",
		Attributes: map[string]schema.Attribute{
			"tofu_binary": schema.StringAttribute{
				Optional: true,
				Description: "Explicit path to the bundled multiform binary. If unset, resolved from the " +
					multiformBinaryEnvVar + " environment variable, then from a file named \"" +
					binarySiblingName() + "\" next to this provider's own executable.",
			},
		},
	}
}

func (p *MultiformProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	binaryPath, err := resolveBinaryPath(config.TofuBinary.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to locate the multiform binary", err.Error())
		return
	}

	resp.ResourceData = &providerData{binaryPath: binaryPath}
}

func (p *MultiformProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewStackResource,
	}
}

func (p *MultiformProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return nil
}

// resolveBinaryPath finds the bundled multiform binary, in order: the
// provider's own explicit attribute, the MULTIFORM_TOFU_BINARY
// environment variable, then a sibling file next to this provider's own
// executable - the same resolution order that works for real
// distribution, where the provider's release package bundles both
// binaries together in one zip.
func resolveBinaryPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if v := os.Getenv(multiformBinaryEnvVar); v != "" {
		return v, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not determine this provider's own executable path: %w", err)
	}
	candidate := filepath.Join(filepath.Dir(exe), binarySiblingName())
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf(
			"no multiform binary found: set the provider's tofu_binary attribute, the %s environment variable, or place a %q binary next to the provider executable (looked for %s): %w",
			multiformBinaryEnvVar, binarySiblingName(), candidate, err,
		)
	}
	return candidate, nil
}

func binarySiblingName() string {
	if runtime.GOOS == "windows" {
		return "multiform.exe"
	}
	return "multiform"
}
