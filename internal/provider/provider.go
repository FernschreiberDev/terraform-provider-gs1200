// Package provider exposes the Zyxel GS1200 web driver as an OpenTofu
// provider.
//
// One provider instance addresses one switch, so a fleet is declared with
// aliases:
//
//	provider "schaltwerk" {
//	  alias    = "gs1200"
//	  host     = "192.168.2.6"
//	  password = var.gs1200_password
//	}
//
// That mapping is deliberate. The GS1200 serves a single web session, so the
// unit of contention is the device; making it the unit of configuration keeps
// credentials, timeouts and TLS settings attached to the thing they describe.
package provider

import (
	"context"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/FernschreiberDev/terraform-provider-schaltwerk/internal/zyxel"
)

// Ensure the implementation satisfies the framework's interface.
var _ provider.Provider = (*schaltwerkProvider)(nil)

type schaltwerkProvider struct {
	version string
}

// New returns the provider factory the plugin server needs.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &schaltwerkProvider{version: version}
	}
}

func (p *schaltwerkProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "schaltwerk"
	resp.Version = p.version
}

type providerModel struct {
	Host      types.String `tfsdk:"host"`
	Password  types.String `tfsdk:"password"`
	Scheme    types.String `tfsdk:"scheme"`
	VerifyTLS types.Bool   `tfsdk:"verify_tls"`
	Timeout   types.Int64  `tfsdk:"timeout"`
}

func (p *schaltwerkProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages Zyxel GS1200 (v3) switches over their web interface. " +
			"Declare one aliased provider instance per switch.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				MarkdownDescription: "Address of the switch's web interface, without a scheme " +
					"(for example `192.168.2.6`). Falls back to `SCHALTWERK_HOST`.",
				Optional: true,
			},
			"password": schema.StringAttribute{
				MarkdownDescription: "Web-interface password. It is hashed with SHA-256 before " +
					"being sent, exactly as the firmware's own login page does, so the " +
					"plaintext never reaches the wire. Falls back to `SCHALTWERK_PASSWORD`.",
				Optional:  true,
				Sensitive: true,
			},
			"scheme": schema.StringAttribute{
				MarkdownDescription: "`https` (default) or `http`.",
				Optional:            true,
			},
			"verify_tls": schema.BoolAttribute{
				MarkdownDescription: "Verify the switch's TLS certificate. Off by default: these " +
					"switches ship a self-signed certificate that cannot be replaced. Turn it " +
					"on when a proxy with a real certificate fronts the switch.",
				Optional: true,
			},
			"timeout": schema.Int64Attribute{
				MarkdownDescription: "Per-request timeout in seconds (default 10). The GS1200 has " +
					"a small, slow CPU; be generous rather than clever.",
				Optional: true,
			},
		},
	}
}

func (p *schaltwerkProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An unknown value here means it comes from another resource's output and
	// is not resolved yet. Say so plainly rather than building a client that
	// would talk to the wrong address.
	if config.Host.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path("host"),
			"Switch address not known at configure time",
			"The provider cannot be configured from a value that is only known after apply. "+
				"Use a variable or a literal for `host`.",
		)
		return
	}

	host := stringOrEnv(config.Host, "SCHALTWERK_HOST")
	if host == "" {
		resp.Diagnostics.AddAttributeError(
			path("host"),
			"Missing switch address",
			"Set `host` on the provider block, or the SCHALTWERK_HOST environment variable.",
		)
		return
	}

	password := stringOrEnv(config.Password, "SCHALTWERK_PASSWORD")
	scheme := stringOrEnv(config.Scheme, "SCHALTWERK_SCHEME")
	if scheme == "" {
		scheme = "https"
	}
	timeout := 10 * time.Second
	if !config.Timeout.IsNull() && config.Timeout.ValueInt64() > 0 {
		timeout = time.Duration(config.Timeout.ValueInt64()) * time.Second
	}

	client, err := zyxel.NewClient(host, password, scheme, config.VerifyTLS.ValueBool(), timeout)
	if err != nil {
		resp.Diagnostics.AddError("Cannot build the switch client", err.Error())
		return
	}

	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *schaltwerkProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewVLANResource,
		NewPortResource,
	}
}

func (p *schaltwerkProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewSwitchDataSource,
	}
}

// stringOrEnv prefers the configured value and falls back to the environment,
// so credentials can stay out of the .tf files and out of the state's inputs.
func stringOrEnv(value types.String, envVar string) string {
	if !value.IsNull() && value.ValueString() != "" {
		return value.ValueString()
	}
	return os.Getenv(envVar)
}
