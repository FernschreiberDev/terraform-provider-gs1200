package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/FernschreiberDev/terraform-provider-schaltwerk/internal/zyxel"
)

var (
	_ resource.Resource                = (*portResource)(nil)
	_ resource.ResourceWithConfigure   = (*portResource)(nil)
	_ resource.ResourceWithImportState = (*portResource)(nil)
)

type portResource struct {
	client *zyxel.Client
}

// NewPortResource is the factory the provider registers.
func NewPortResource() resource.Resource { return &portResource{} }

type portModel struct {
	Port     types.Int64 `tfsdk:"port"`
	PVID     types.Int64 `tfsdk:"pvid"`
	Tagged   types.Set   `tfsdk:"tagged"`
	Untagged types.Set   `tfsdk:"untagged"`
	Force    types.Bool  `tfsdk:"force"`
}

func (r *portResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_zyxel_port"
}

func (r *portResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "One physical port's whole 802.1Q configuration.\n\n" +
			"The switch stores the opposite arrangement — a table of VLANs, each holding a " +
			"bitmap of member ports — so this resource is a view the provider assembles and " +
			"writes back. A write only ever moves **this port's** bit in each VLAN row, which " +
			"is what lets every port be its own resource without two of them undoing each " +
			"other.\n\n" +
			"Every VLAN named here must already exist as a `schaltwerk_zyxel_vlan`.\n\n" +
			"**Destroying this resource does not change the switch.** A port always has some " +
			"configuration; there is no \"unconfigured\" state to return it to, and picking one " +
			"during a destroy would move traffic nobody asked to move. Terraform simply stops " +
			"tracking the port.",
		Attributes: map[string]schema.Attribute{
			"port": schema.Int64Attribute{
				MarkdownDescription: "1-based port number, as printed on the switch.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 32)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"pvid": schema.Int64Attribute{
				MarkdownDescription: "**Ingress.** The VLAN an *untagged* frame arriving on this " +
					"port is placed into. One value per port — an untagged frame carries " +
					"nothing to choose from.\n\n" +
					"It must be one of the `untagged` VLANs below. A PVID naming a VLAN the " +
					"port does not carry untagged means frames enter in that VLAN and cannot " +
					"leave the same way; the hardware permits it and reports nothing, so the " +
					"provider refuses it unless `force` is set.",
				Required:   true,
				Validators: []validator.Int64{int64validator.Between(1, 4094)},
			},
			"untagged": schema.SetAttribute{
				MarkdownDescription: "**Egress.** VLAN ids whose frames leave this port with " +
					"their 802.1Q tag stripped. What an access port gives a device that knows " +
					"nothing of VLANs.",
				Optional:    true,
				ElementType: types.Int64Type,
			},
			"tagged": schema.SetAttribute{
				MarkdownDescription: "**Egress.** VLAN ids whose frames leave this port still " +
					"carrying their tag — a trunk towards another switch or a VLAN-aware device.",
				Optional:    true,
				ElementType: types.Int64Type,
			},
			"force": schema.BoolAttribute{
				MarkdownDescription: "Bypass the safety checks: removing this port from the " +
					"switch's management VLAN, and a `pvid` the port does not carry untagged. " +
					"Recovering from the first needs physical access to the switch.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
		},
	}
}

func (r *portResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *portResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan portModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, plan, &resp.Diagnostics, &resp.State)
}

func (r *portResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state portModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A port's view needs the PVIDs, which live behind the login, so this
	// briefly claims the switch's single web session. The provider caches the
	// answer for the run, so five ports cost one session, not five.
	config, err := r.client.ReadConfig(ctx)
	if err != nil {
		addDriverError(&resp.Diagnostics, "Cannot read the switch's configuration", err)
		return
	}
	if config.Partial {
		resp.Diagnostics.AddError(
			"A port's configuration is not readable without a password",
			"Set `password` on the provider block for this switch: the page carrying "+
				"the PVIDs requires a web session.",
		)
		return
	}

	port := int(state.Port.ValueInt64())
	if config.PortCount > 0 && port > config.PortCount {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(applyPort(ctx, &state, config.ReadPort(port))...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *portResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan portModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, plan, &resp.Diagnostics, &resp.State)
}

// Delete deliberately touches nothing on the switch. See the resource
// description: choosing a configuration for a port on the user's behalf during
// a destroy is how traffic ends up somewhere nobody picked.
func (r *portResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state portModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.AddWarning(
		fmt.Sprintf("Port %d keeps its configuration on the switch", state.Port.ValueInt64()),
		"Terraform has stopped managing this port, but the switch is unchanged. "+
			"Set it explicitly in the web interface if it should be something else.",
	)
}

func (r *portResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	port, err := strconv.Atoi(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import id",
			fmt.Sprintf("A port is imported by its number, for example `tofu import "+
				"schaltwerk_zyxel_port.uplink 1`. Got %q.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("port"), int64(port))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("force"), false)...)
}

func (r *portResource) write(ctx context.Context, plan portModel, diags *diagList, state stateSetter) {
	tagged, d := intsFromSet(ctx, plan.Tagged)
	diags.Append(d...)
	untagged, d := intsFromSet(ctx, plan.Untagged)
	diags.Append(d...)
	if diags.HasError() {
		return
	}

	port := int(plan.Port.ValueInt64())
	config, err := r.client.WritePort(ctx, zyxel.PortConfig{
		Port:     port,
		PVID:     int(plan.PVID.ValueInt64()),
		Tagged:   tagged,
		Untagged: untagged,
	}, plan.Force.ValueBool())
	if err != nil {
		addDriverError(diags, fmt.Sprintf("Cannot configure port %d", port), err)
		return
	}

	diags.Append(applyPort(ctx, &plan, config.ReadPort(port))...)
	if diags.HasError() {
		return
	}
	diags.Append(state.Set(ctx, &plan)...)
}

// applyPort copies the switch's own view into the model. An omitted list stays
// omitted when the device reports none, so an absent attribute does not read
// back as an empty set — Terraform treats that as an inconsistent result.
func applyPort(ctx context.Context, model *portModel, view zyxel.PortConfig) diagList {
	var diags diagList
	tagged, d := setOrKeepNull(ctx, view.Tagged, model.Tagged)
	diags.Append(d...)
	untagged, d := setOrKeepNull(ctx, view.Untagged, model.Untagged)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}
	model.Port = types.Int64Value(int64(view.Port))
	model.PVID = types.Int64Value(int64(view.PVID))
	model.Tagged = tagged
	model.Untagged = untagged
	return diags
}
