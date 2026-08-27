package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	_ resource.Resource                = (*vlanResource)(nil)
	_ resource.ResourceWithConfigure   = (*vlanResource)(nil)
	_ resource.ResourceWithImportState = (*vlanResource)(nil)
)

type vlanResource struct {
	client *zyxel.Client
}

// NewVLANResource is the factory the provider registers.
func NewVLANResource() resource.Resource { return &vlanResource{} }

type vlanModel struct {
	VID      types.Int64 `tfsdk:"vid"`
	Tagged   types.Set   `tfsdk:"tagged"`
	Untagged types.Set   `tfsdk:"untagged"`
	Force    types.Bool  `tfsdk:"force"`
	Index    types.Int64 `tfsdk:"index"`
}

func (r *vlanResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_zyxel_vlan"
}

func (r *vlanResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "One 802.1Q VLAN on a Zyxel GS1200. Ports are 1-based, as printed " +
			"on the switch. A port listed in both `tagged` and `untagged` is treated as " +
			"tagged, which is what the firmware does.",
		Attributes: map[string]schema.Attribute{
			"vid": schema.Int64Attribute{
				MarkdownDescription: "VLAN id, 1-4094. Changing it destroys and recreates the VLAN.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 4094)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"tagged": schema.SetAttribute{
				MarkdownDescription: "Ports carrying this VLAN tagged — trunks towards another switch.",
				Optional:            true,
				ElementType:         types.Int64Type,
			},
			"untagged": schema.SetAttribute{
				MarkdownDescription: "Ports carrying this VLAN untagged — access ports. Setting a " +
					"port here does not set its PVID; use `schaltwerk_zyxel_pvid` for that.",
				Optional:    true,
				ElementType: types.Int64Type,
			},
			"force": schema.BoolAttribute{
				MarkdownDescription: "Bypass the safety check that refuses a change removing ports " +
					"from the switch's management VLAN. Recovering from such a change needs " +
					"physical access to the switch, so leave this off unless you have it.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"index": schema.Int64Attribute{
				MarkdownDescription: "The vendor table slot holding this VLAN. The firmware " +
					"addresses VLANs by slot rather than by id; exposed because it is what " +
					"appears in the switch's own web interface.",
				Computed: true,
			},
		},
	}
}

func (r *vlanResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *vlanResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vlanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vid := int(plan.VID.ValueInt64())

	// Refuse to adopt a VLAN that is already on the switch. The write path
	// would happily modify it, but silently taking ownership of live
	// configuration is how an apply reassigns traffic nobody asked it to
	// touch. Reading the table costs no session.
	existing, err := r.client.ReadVLANTable(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the switch's VLAN table", err.Error())
		return
	}
	for _, entry := range existing {
		if entry.VID == vid {
			resp.Diagnostics.AddError(
				fmt.Sprintf("VLAN %d already exists on this switch", vid),
				fmt.Sprintf("Import it instead of creating it:\n\n"+
					"  tofu import schaltwerk_zyxel_vlan.<name> %d\n\n"+
					"Creating it here would overwrite the membership the switch "+
					"currently has.", vid),
			)
			return
		}
	}

	r.write(ctx, plan, &resp.Diagnostics, &resp.State)
}

func (r *vlanResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vlanModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// vlanEntry.xml is unauthenticated, so a refresh never claims the switch's
	// single web session — planning cannot lock its owner out of the web UI.
	entries, err := r.client.ReadVLANTable(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the switch's VLAN table", err.Error())
		return
	}

	vid := int(state.VID.ValueInt64())
	for _, entry := range entries {
		if entry.VID != vid {
			continue
		}
		resp.Diagnostics.Append(applyEntry(ctx, &state, entry)...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	// Gone from the switch: let Terraform plan a fresh create.
	resp.State.RemoveResource(ctx)
}

func (r *vlanResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vlanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, plan, &resp.Diagnostics, &resp.State)
}

func (r *vlanResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vlanModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.DeleteVLAN(ctx, int(state.VID.ValueInt64()), state.Force.ValueBool())
	if err != nil {
		addDriverError(&resp.Diagnostics,
			fmt.Sprintf("Cannot delete VLAN %d", state.VID.ValueInt64()), err)
	}
}

func (r *vlanResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vid, err := strconv.Atoi(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import id",
			fmt.Sprintf("A VLAN is imported by its id, for example `tofu import "+
				"schaltwerk_zyxel_vlan.iot 20`. Got %q.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("vid"), int64(vid))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("force"), false)...)
}

// write pushes the planned membership and stores what the switch confirmed.
func (r *vlanResource) write(ctx context.Context, plan vlanModel, diags *diagList, state stateSetter) {
	tagged, d := intsFromSet(ctx, plan.Tagged)
	diags.Append(d...)
	untagged, d := intsFromSet(ctx, plan.Untagged)
	diags.Append(d...)
	if diags.HasError() {
		return
	}

	vid := int(plan.VID.ValueInt64())
	config, err := r.client.WriteVLAN(ctx, zyxel.VLANEntry{
		VID:      vid,
		Tagged:   tagged,
		Untagged: untagged,
	}, plan.Force.ValueBool())
	if err != nil {
		addDriverError(diags, fmt.Sprintf("Cannot write VLAN %d", vid), err)
		return
	}

	entry, ok := config.VLAN(vid)
	if !ok {
		diags.AddError(
			fmt.Sprintf("VLAN %d is missing after a write the switch accepted", vid),
			"The switch reported success but the VLAN is not in its table. "+
				"Check the switch's web interface before running apply again.",
		)
		return
	}
	diags.Append(applyEntry(ctx, &plan, entry)...)
	if diags.HasError() {
		return
	}
	diags.Append(state.Set(ctx, &plan)...)
}

// applyEntry copies what the switch reports into the model, so state always
// holds the device's own view rather than what was asked for.
//
// The one exception is an omitted port list. `tagged` and `untagged` are
// Optional and not Computed, so Terraform requires the value it gets back to
// equal the one in the configuration: writing an empty set where the
// configuration said nothing is an "inconsistent result after apply", and on
// refresh it would show as a permanent diff. An absent list and an empty one
// mean the same thing to the switch, so the configuration's spelling wins.
func applyEntry(ctx context.Context, model *vlanModel, entry zyxel.VLANEntry) diagList {
	var diags diagList
	tagged, d := setOrKeepNull(ctx, entry.Tagged, model.Tagged)
	diags.Append(d...)
	untagged, d := setOrKeepNull(ctx, entry.Untagged, model.Untagged)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}
	model.VID = types.Int64Value(int64(entry.VID))
	model.Tagged = tagged
	model.Untagged = untagged
	model.Index = types.Int64Value(int64(entry.Index))
	return diags
}

// setOrKeepNull turns the device's ports into a set, unless the device reports
// none and the configuration left the attribute out — then it stays out.
func setOrKeepNull(ctx context.Context, ports []int, configured types.Set) (types.Set, diagList) {
	if len(ports) == 0 && configured.IsNull() {
		return types.SetNull(types.Int64Type), nil
	}
	return setFromInts(ctx, ports)
}

// -- conversions -----------------------------------------------------------

func intsFromSet(ctx context.Context, set types.Set) ([]int, diagList) {
	var diags diagList
	if set.IsNull() || set.IsUnknown() {
		return nil, diags
	}
	var values []int64
	diags.Append(set.ElementsAs(ctx, &values, false)...)
	ports := make([]int, 0, len(values))
	for _, value := range values {
		ports = append(ports, int(value))
	}
	sort.Ints(ports)
	return ports, diags
}

func setFromInts(ctx context.Context, ports []int) (types.Set, diagList) {
	values := make([]int64, 0, len(ports))
	for _, port := range ports {
		values = append(values, int64(port))
	}
	set, d := types.SetValueFrom(ctx, types.Int64Type, values)
	var diags diagList
	diags.Append(d...)
	return set, diags
}

// addDriverError turns a driver failure into a diagnostic that says what to do
// next, because "unsafe change refused" without the way out is just a wall.
func addDriverError(diags *diagList, summary string, err error) {
	switch {
	case errors.Is(err, zyxel.ErrUnsafe):
		diags.AddError(summary, err.Error()+
			"\n\nSet `force = true` on this resource if you are certain, and have "+
			"physical access to the switch should it become unreachable.")
	case errors.Is(err, zyxel.ErrInUseAsPVID):
		diags.AddError(summary, err.Error()+
			"\n\nDestroying a `schaltwerk_zyxel_pvid` resource deliberately leaves the "+
			"port's native VLAN alone, so removing both at once cannot work. Point "+
			"those ports at another VLAN and apply, then remove this VLAN.")
	case errors.Is(err, zyxel.ErrBusy):
		diags.AddError(summary, err.Error()+
			"\n\nThe switch serves one web session at a time. Close the browser tab "+
			"holding it, or wait about a minute, then apply again.")
	case errors.Is(err, zyxel.ErrAuth):
		diags.AddError(summary, err.Error()+
			"\n\nCheck the `password` on the provider block for this switch.")
	default:
		diags.AddError(summary, err.Error())
	}
}
