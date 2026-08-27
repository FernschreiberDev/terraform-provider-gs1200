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
	VID   types.Int64 `tfsdk:"vid"`
	Force types.Bool  `tfsdk:"force"`
	Index types.Int64 `tfsdk:"index"`
}

func (r *vlanResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_zyxel_vlan"
}

func (r *vlanResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The existence of one 802.1Q VLAN on a Zyxel GS1200.\n\n" +
			"This resource declares that the VLAN exists, and nothing else. Which ports " +
			"carry it, tagged or untagged, belongs to `schaltwerk_zyxel_port` — the two " +
			"never write the same bytes, so they cannot fight over them. A VLAN created " +
			"here starts with no members.",
		Attributes: map[string]schema.Attribute{
			"vid": schema.Int64Attribute{
				MarkdownDescription: "VLAN id, 1-4094. Changing it destroys and recreates the VLAN.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 4094)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"force": schema.BoolAttribute{
				MarkdownDescription: "Bypass the safety check that refuses to delete the " +
					"switch's management VLAN.",
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

	// Refuse to adopt a VLAN already on the switch. Creating it here would be
	// harmless — membership is not touched — but silently taking ownership of
	// something nobody declared means a later destroy removes configuration
	// this run never created. Reading the table costs no session.
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
					"  tofu import schaltwerk_zyxel_vlan.<name> %d", vid),
			)
			return
		}
	}

	config, err := r.client.EnsureVLAN(ctx, vid, plan.Force.ValueBool())
	if err != nil {
		addDriverError(&resp.Diagnostics, fmt.Sprintf("Cannot create VLAN %d", vid), err)
		return
	}
	r.store(ctx, plan, config, &resp.Diagnostics, &resp.State)
}

func (r *vlanResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vlanModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// vlanEntry.xml is unauthenticated, so refreshing a VLAN never claims the
	// switch's single web session.
	entries, err := r.client.ReadVLANTable(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the switch's VLAN table", err.Error())
		return
	}

	vid := int(state.VID.ValueInt64())
	for _, entry := range entries {
		if entry.VID == vid {
			state.Index = types.Int64Value(int64(entry.Index))
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

// Update has nothing to send: `vid` replaces the resource and `force` only
// governs how the next write is checked.
func (r *vlanResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vlanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	entries, err := r.client.ReadVLANTable(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the switch's VLAN table", err.Error())
		return
	}
	plan.Index = types.Int64Value(0)
	for _, entry := range entries {
		if entry.VID == int(plan.VID.ValueInt64()) {
			plan.Index = types.Int64Value(int64(entry.Index))
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vlanResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vlanModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.DeleteVLAN(ctx, int(state.VID.ValueInt64()), state.Force.ValueBool()); err != nil {
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
				"schaltwerk_zyxel_vlan.iot 1003`. Got %q.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("vid"), int64(vid))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("force"), false)...)
}

func (r *vlanResource) store(ctx context.Context, plan vlanModel, config zyxel.Config, diags *diagList, state stateSetter) {
	entry, ok := config.VLAN(int(plan.VID.ValueInt64()))
	if !ok {
		diags.AddError(
			fmt.Sprintf("VLAN %d is missing after a write the switch accepted", plan.VID.ValueInt64()),
			"The switch reported success but the VLAN is not in its table.",
		)
		return
	}
	plan.Index = types.Int64Value(int64(entry.Index))
	diags.Append(state.Set(ctx, &plan)...)
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

// setOrKeepNull turns the device's answer into a set, unless the device
// reports none and the configuration left the attribute out — then it stays
// out. `tagged` and `untagged` are Optional and not Computed, so Terraform
// requires the value it gets back to equal the one in the configuration:
// writing an empty set where the configuration said nothing is an
// "inconsistent result after apply", and on refresh a permanent diff.
func setOrKeepNull(ctx context.Context, values []int, configured types.Set) (types.Set, diagList) {
	if len(values) == 0 && configured.IsNull() {
		return types.SetNull(types.Int64Type), nil
	}
	return setFromInts(ctx, values)
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
			"\n\nPoint those ports at another VLAN and apply, then remove this one.")
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
