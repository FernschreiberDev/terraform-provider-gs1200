package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

	// Electrical settings, as opposed to 802.1Q membership. Optional and
	// computed: leaving one out means "whatever the switch already has",
	// not "reset it".
	Enabled     types.Bool   `tfsdk:"enabled"`
	Speed       types.String `tfsdk:"speed"`
	FlowControl types.Bool   `tfsdk:"flow_control"`
	IngressKbps types.Int64  `tfsdk:"ingress_rate_kbps"`
	EgressKbps  types.Int64  `tfsdk:"egress_rate_kbps"`
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
					"switch's management VLAN, switching off a port that carries it, and a " +
					"`pvid` the port does not carry untagged. Recovering from the first two " +
					"needs physical access to the switch.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the port is switched on at all.\n\n" +
					"Switching off a port that carries the management VLAN takes the switch " +
					"off the network; the provider refuses that unless `force` is set. Leave " +
					"this out to keep whatever the switch already has.",
				Optional: true,
				Computed: true,
			},
			"speed": schema.StringAttribute{
				MarkdownDescription: "Link speed and duplex: `auto` (the default on this " +
					"hardware), `1000-full`, `100-auto`, `100-full`, `10-auto` or `10-full`. " +
					"Forcing a rate the other end does not agree to leaves the link down.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.String{stringvalidator.OneOf(zyxel.Speeds()...)},
			},
			"ingress_rate_kbps": schema.Int64Attribute{
				MarkdownDescription: "Cap on traffic entering this port, in kbps. `0` lifts " +
					"the cap.\n\n" +
					"The firmware stores rates in steps of 32 kbps, so a figure it cannot " +
					"represent exactly is refused rather than quietly rounded. Range: 32 to " +
					"1000000.",
				Optional: true,
				Computed: true,
			},
			"egress_rate_kbps": schema.Int64Attribute{
				MarkdownDescription: "Cap on traffic leaving this port, in kbps. `0` lifts the " +
					"cap. Same 32 kbps grid as `ingress_rate_kbps`.",
				Optional: true,
				Computed: true,
			},
			"flow_control": schema.BoolAttribute{
				MarkdownDescription: "802.3x pause frames. Off throughout on this hardware, " +
					"and rarely worth turning on outside storage networks.",
				Optional: true,
				Computed: true,
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

	settings, err := r.client.ReadSettings(ctx)
	if err != nil {
		addDriverError(&resp.Diagnostics, "Cannot read the switch's port settings", err)
		return
	}
	if electrical, ok := settings.Port(port); ok {
		applySettings(&state, electrical)
	}

	management, err := r.client.ReadManagement(ctx)
	if err != nil {
		addDriverError(&resp.Diagnostics, "Cannot read the port's rate limits", err)
		return
	}
	applyRates(&state, management, port)
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

	// The electrical settings live on a different page and go out in a second
	// write. Both are serialised by the device lock, so the switch still only
	// ever handles one at a time.
	settings, err := r.client.ReadSettings(ctx)
	if err != nil {
		addDriverError(diags, "Cannot read the switch's port settings", err)
		return
	}
	current, ok := settings.Port(port)
	if !ok {
		diags.AddError(
			fmt.Sprintf("Port %d has no settings on this switch", port),
			"The 802.1Q side applied, but the port page does not list this port.")
		return
	}

	// An attribute left out of the configuration keeps whatever the switch
	// has: this resource declines to reset what it was not asked about.
	wanted := current
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		wanted.Enabled = plan.Enabled.ValueBool()
	}
	if !plan.Speed.IsNull() && !plan.Speed.IsUnknown() {
		wanted.Speed = plan.Speed.ValueString()
	}
	if !plan.FlowControl.IsNull() && !plan.FlowControl.IsUnknown() {
		wanted.FlowControl = plan.FlowControl.ValueBool()
	}

	if wanted != current {
		after, err := r.client.WritePortSettings(ctx, wanted, plan.Force.ValueBool())
		if err != nil {
			addDriverError(diags, fmt.Sprintf("Cannot configure port %d", port), err)
			return
		}
		if applied, ok := after.Port(port); ok {
			wanted = applied
		}
	}
	applySettings(&plan, wanted)

	// Rate limits live on a third page again, and go out only when asked for.
	management, err := r.client.ReadManagement(ctx)
	if err != nil {
		addDriverError(diags, "Cannot read the port's rate limits", err)
		return
	}
	if port > len(management.IngressKbps) {
		diags.AddError(
			fmt.Sprintf("Port %d has no rate limit entry on this switch", port),
			"The switch lists fewer ports than this resource addresses.")
		return
	}
	wantIn, wantOut := management.IngressKbps[port-1], management.EgressKbps[port-1]
	if v := plan.IngressKbps; !v.IsNull() && !v.IsUnknown() {
		wantIn = int(v.ValueInt64())
	}
	if v := plan.EgressKbps; !v.IsNull() && !v.IsUnknown() {
		wantOut = int(v.ValueInt64())
	}
	if wantIn != management.IngressKbps[port-1] || wantOut != management.EgressKbps[port-1] {
		after, err := r.client.WritePortRates(ctx, port, wantIn, wantOut)
		if err != nil {
			addDriverError(diags, fmt.Sprintf("Cannot cap port %d", port), err)
			return
		}
		management = after
	}
	applyRates(&plan, management, port)

	diags.Append(state.Set(ctx, &plan)...)
}

// applyRates copies the switch's own view of a port's caps into the model.
func applyRates(model *portModel, management zyxel.Management, port int) {
	if port-1 < len(management.IngressKbps) {
		model.IngressKbps = types.Int64Value(int64(management.IngressKbps[port-1]))
	}
	if port-1 < len(management.EgressKbps) {
		model.EgressKbps = types.Int64Value(int64(management.EgressKbps[port-1]))
	}
}

// applySettings copies the switch's own view of a port's electrical settings
// into the model. They are Computed, so an attribute the configuration left
// out still gets a value — the device's.
func applySettings(model *portModel, settings zyxel.PortSettings) {
	model.Enabled = types.BoolValue(settings.Enabled)
	model.Speed = types.StringValue(settings.Speed)
	model.FlowControl = types.BoolValue(settings.FlowControl)
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
