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
	_ resource.Resource                = (*pvidResource)(nil)
	_ resource.ResourceWithConfigure   = (*pvidResource)(nil)
	_ resource.ResourceWithImportState = (*pvidResource)(nil)
)

type pvidResource struct {
	client *zyxel.Client
}

// NewPVIDResource is the factory the provider registers.
func NewPVIDResource() resource.Resource { return &pvidResource{} }

type pvidModel struct {
	Port  types.Int64 `tfsdk:"port"`
	VID   types.Int64 `tfsdk:"vid"`
	Force types.Bool  `tfsdk:"force"`
}

func (r *pvidResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_zyxel_pvid"
}

func (r *pvidResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The native VLAN (PVID) of one port on a Zyxel GS1200 — the VLAN " +
			"untagged traffic arriving on that port is placed in.\n\n" +
			"The port must already be an untagged member of that VLAN, which means declaring " +
			"the matching `schaltwerk_zyxel_vlan` and letting Terraform order the two with " +
			"`depends_on` or an attribute reference.\n\n" +
			"**Destroying this resource does not change the switch.** There is no \"no PVID\" " +
			"state to return a port to, and resetting it to VLAN 1 would silently move the " +
			"port into the management VLAN. Terraform simply stops tracking it, and the port " +
			"keeps whatever PVID it has.",
		Attributes: map[string]schema.Attribute{
			"port": schema.Int64Attribute{
				MarkdownDescription: "1-based port number, as printed on the switch.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 32)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"vid": schema.Int64Attribute{
				MarkdownDescription: "VLAN id to make native on this port, 1-4094.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 4094)},
			},
			"force": schema.BoolAttribute{
				MarkdownDescription: "Bypass the safety check that refuses a PVID pointing at a " +
					"VLAN the switch does not have.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
		},
	}
}

func (r *pvidResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *pvidResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan pvidModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, plan, &resp.Diagnostics, &resp.State)
}

func (r *pvidResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state pvidModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unlike VLAN membership, PVIDs live on a page that requires a session, so
	// a refresh does claim the switch's single web session for a moment.
	config, err := r.client.ReadConfig(ctx)
	if err != nil {
		addDriverError(&resp.Diagnostics, "Cannot read the switch's PVIDs", err)
		return
	}
	if config.Partial {
		resp.Diagnostics.AddError(
			"PVIDs are not readable without a password",
			"Set `password` on the provider block for this switch; the page carrying "+
				"the PVIDs requires a web session.",
		)
		return
	}

	port := int(state.Port.ValueInt64())
	vid, ok := config.PVID[port]
	if !ok {
		// The port is gone — a different model, or the table shrank.
		resp.State.RemoveResource(ctx)
		return
	}
	state.VID = types.Int64Value(int64(vid))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *pvidResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan pvidModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, plan, &resp.Diagnostics, &resp.State)
}

// Delete deliberately touches nothing on the switch. See the resource
// description: a port always has some PVID, and picking one on the user's
// behalf during a destroy is how traffic ends up somewhere nobody chose.
func (r *pvidResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state pvidModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.AddWarning(
		fmt.Sprintf("Port %d keeps PVID %d on the switch",
			state.Port.ValueInt64(), state.VID.ValueInt64()),
		"Terraform has stopped managing this port's native VLAN, but the switch is "+
			"unchanged. Set it explicitly in the web interface if it should be something else.",
	)
}

func (r *pvidResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	port, err := strconv.Atoi(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import id",
			fmt.Sprintf("A PVID is imported by its port number, for example `tofu import "+
				"schaltwerk_zyxel_pvid.port5 5`. Got %q.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("port"), int64(port))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("force"), false)...)
}

func (r *pvidResource) write(ctx context.Context, plan pvidModel, diags *diagList, state stateSetter) {
	port := int(plan.Port.ValueInt64())
	vid := int(plan.VID.ValueInt64())

	// One port at a time: the driver merges this into the table the switch
	// currently reports, so ports Terraform does not manage keep their values.
	config, err := r.client.WritePVID(ctx, map[int]int{port: vid}, plan.Force.ValueBool())
	if err != nil {
		addDriverError(diags, fmt.Sprintf("Cannot set the PVID of port %d", port), err)
		return
	}
	if applied, ok := config.PVID[port]; ok {
		plan.VID = types.Int64Value(int64(applied))
	}
	diags.Append(state.Set(ctx, &plan)...)
}
