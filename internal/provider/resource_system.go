package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/FernschreiberDev/terraform-provider-gs1200/internal/zyxel"
)

var (
	_ resource.Resource                = (*systemResource)(nil)
	_ resource.ResourceWithConfigure   = (*systemResource)(nil)
	_ resource.ResourceWithImportState = (*systemResource)(nil)
)

type systemResource struct {
	client *zyxel.Client
}

// NewSystemResource is the factory the provider registers.
func NewSystemResource() resource.Resource { return &systemResource{} }

type systemModel struct {
	Name            types.String `tfsdk:"name"`
	LoopPrevention  types.Bool   `tfsdk:"loop_prevention"`
	StormControl    types.Bool   `tfsdk:"storm_control"`
	StormControlPPS types.Int64  `tfsdk:"storm_control_pps"`
	Force           types.Bool   `tfsdk:"force"`

	IGMPSnooping    types.Bool  `tfsdk:"igmp_snooping"`
	IGMPUnknownDrop types.Bool  `tfsdk:"igmp_unknown_multicast_drop"`
	IGMPRouterPort  types.Int64 `tfsdk:"igmp_static_router_port"`
	LED             types.Bool  `tfsdk:"led"`
	EEE             types.Bool  `tfsdk:"energy_efficient_ethernet"`
	SNMP            types.Bool  `tfsdk:"snmp"`
	IsolationUplink types.Int64 `tfsdk:"port_isolation_uplink"`

	ManagementVLAN types.Int64  `tfsdk:"management_vlan"`
	Model          types.String `tfsdk:"model"`
	Hardware       types.String `tfsdk:"hardware"`
	Firmware       types.String `tfsdk:"firmware"`
	MAC            types.String `tfsdk:"mac"`
}

func (r *systemResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system"
}

func (r *systemResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The switch itself: its name and the two protections that guard " +
			"the whole device rather than one port.\n\n" +
			"There is one of these per switch, so it takes no identifier. An attribute left " +
			"out keeps whatever the switch already has — this resource declines to reset " +
			"what it was not asked about.\n\n" +
			"**Destroying it does not change the switch.** A switch always has a name and " +
			"some protection setting; there is no unconfigured state to return it to.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "Device name. The firmware accepts 1 to 14 characters, " +
					"letters, digits, underscore or hyphen — its own page refuses anything " +
					"else, and so does the provider, before touching the switch.",
				Optional: true,
				Computed: true,
			},
			"loop_prevention": schema.BoolAttribute{
				MarkdownDescription: "Detects a cable plugged back into the same switch and " +
					"shuts the loop down.\n\n" +
					"Switching it off is refused unless `force` is set: a loop floods the " +
					"segment, and the flood is what would keep you from reaching the switch " +
					"to undo it.",
				Optional: true,
				Computed: true,
			},
			"storm_control": schema.BoolAttribute{
				MarkdownDescription: "Caps broadcast, multicast and unknown-unicast floods at " +
					"`storm_control_pps` packets per second.",
				Optional: true,
				Computed: true,
			},
			"storm_control_pps": schema.Int64Attribute{
				MarkdownDescription: "The cap, in packets per second, 1 to 500000. Only " +
					"meaningful while `storm_control` is on.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.Int64{int64validator.Between(1, 500000)},
			},
			"force": schema.BoolAttribute{
				MarkdownDescription: "Bypass the refusal to switch loop prevention off.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},

			"igmp_snooping": schema.BoolAttribute{
				MarkdownDescription: "Learn which ports asked for which multicast groups, and " +
					"send each group only there instead of flooding every port.",
				Optional: true,
				Computed: true,
			},
			"igmp_unknown_multicast_drop": schema.BoolAttribute{
				MarkdownDescription: "Discard multicast nobody subscribed to, rather than " +
					"flooding it. Only meaningful while `igmp_snooping` is on.",
				Optional: true,
				Computed: true,
			},
			"igmp_static_router_port": schema.Int64Attribute{
				MarkdownDescription: "Port where the multicast router sits, `0` to discover it " +
					"automatically — which is what this hardware does by default.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.Int64{int64validator.Between(0, 32)},
			},
			"led": schema.BoolAttribute{
				MarkdownDescription: "The firmware's own Disable/Enable control for the panel " +
					"lights. Both switches in both units tested report it off.",
				Optional: true,
				Computed: true,
			},
			"energy_efficient_ethernet": schema.BoolAttribute{
				MarkdownDescription: "802.3az. Applied to every port at once on this hardware, " +
					"and the switch takes about ten seconds to settle after the change.",
				Optional: true,
				Computed: true,
			},
			"snmp": schema.BoolAttribute{
				MarkdownDescription: "SNMP v1 and v2c, which the firmware switches together.\n\n" +
					"Turning it off is refused unless `force` is set: anything polling this " +
					"switch goes blind, and what stops working is a dashboard nobody is " +
					"watching at the moment of the apply.",
				Optional: true,
				Computed: true,
			},
			"port_isolation_uplink": schema.Int64Attribute{
				MarkdownDescription: "`0` lets every port talk to every other. Any other value " +
					"names the single port the others may reach — a guest-network arrangement " +
					"where devices see the uplink and nothing else.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.Int64{int64validator.Between(0, 32)},
			},

			"management_vlan": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "The VLAN carrying the switch's own traffic. Read-only " +
					"here on purpose: changing it is how a switch is lost, and the provider " +
					"would sever its own connection halfway through its own write.",
			},
			"model":    schema.StringAttribute{Computed: true, MarkdownDescription: "Model, as the switch reports it."},
			"hardware": schema.StringAttribute{Computed: true, MarkdownDescription: "Hardware revision."},
			"firmware": schema.StringAttribute{Computed: true, MarkdownDescription: "Firmware version."},
			"mac":      schema.StringAttribute{Computed: true, MarkdownDescription: "Base MAC address."},
		},
	}
}

func (r *systemResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *systemResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan systemModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, plan, &resp.Diagnostics, &resp.State)
}

func (r *systemResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state systemModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := r.client.ReadDeviceInfo(ctx)
	if err != nil {
		addDriverError(&resp.Diagnostics, "Cannot read the switch's identity", err)
		return
	}
	settings, err := r.client.ReadSettings(ctx)
	if err != nil {
		addDriverError(&resp.Diagnostics, "Cannot read the switch's protection settings", err)
		return
	}
	management, err := r.client.ReadManagement(ctx)
	if err != nil {
		addDriverError(&resp.Diagnostics, "Cannot read the switch's management settings", err)
		return
	}

	applySystem(&state, info, settings, management)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *systemResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan systemModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, plan, &resp.Diagnostics, &resp.State)
}

// Delete touches nothing. A switch always has a name and a protection setting;
// picking one on the user's behalf during a destroy would change the device
// while claiming to stop managing it.
func (r *systemResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(
		"The switch keeps its name and protection settings",
		"Terraform has stopped managing them, but the switch is unchanged.",
	)
}

// ImportState takes any identifier: there is one switch behind each provider
// instance, so there is nothing to choose between.
func (r *systemResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path("force"), false)...)
}

func (r *systemResource) write(ctx context.Context, plan systemModel, diags *diagList, state stateSetter) {
	info, err := r.client.ReadDeviceInfo(ctx)
	if err != nil {
		addDriverError(diags, "Cannot read the switch's identity", err)
		return
	}
	settings, err := r.client.ReadSettings(ctx)
	if err != nil {
		addDriverError(diags, "Cannot read the switch's protection settings", err)
		return
	}

	if name := plan.Name; !name.IsNull() && !name.IsUnknown() && name.ValueString() != info.Name {
		if err := r.client.WriteDeviceName(ctx, name.ValueString()); err != nil {
			addDriverError(diags, "Cannot rename the switch", err)
			return
		}
		if info, err = r.client.ReadDeviceInfo(ctx); err != nil {
			addDriverError(diags, "Cannot read the switch's identity back", err)
			return
		}
	}

	// Whatever the configuration leaves out keeps the switch's current value.
	loop, storm, rate := settings.LoopPrevention, settings.StormControl, settings.StormRatePPS
	if v := plan.LoopPrevention; !v.IsNull() && !v.IsUnknown() {
		loop = v.ValueBool()
	}
	if v := plan.StormControl; !v.IsNull() && !v.IsUnknown() {
		storm = v.ValueBool()
	}
	if v := plan.StormControlPPS; !v.IsNull() && !v.IsUnknown() {
		rate = int(v.ValueInt64())
	}

	if loop != settings.LoopPrevention || storm != settings.StormControl ||
		(storm && rate != settings.StormRatePPS) {
		after, err := r.client.WriteProtection(ctx, loop, storm, rate, plan.Force.ValueBool())
		if err != nil {
			addDriverError(diags, "Cannot change the switch's protection settings", err)
			return
		}
		settings = after
	} else {
		settings.LoopPrevention, settings.StormControl, settings.StormRatePPS = loop, storm, rate
	}

	management, err := r.client.ReadManagement(ctx)
	if err != nil {
		addDriverError(diags, "Cannot read the switch's management settings", err)
		return
	}

	// Whatever the configuration leaves out keeps the switch's current value.
	wanted := management
	if v := plan.IGMPSnooping; !v.IsNull() && !v.IsUnknown() {
		wanted.IGMPSnooping = v.ValueBool()
	}
	if v := plan.IGMPUnknownDrop; !v.IsNull() && !v.IsUnknown() {
		wanted.IGMPUnknownDrop = v.ValueBool()
	}
	if v := plan.IGMPRouterPort; !v.IsNull() && !v.IsUnknown() {
		wanted.IGMPStaticRouterPort = int(v.ValueInt64())
	}
	if v := plan.LED; !v.IsNull() && !v.IsUnknown() {
		wanted.LED = v.ValueBool()
	}
	if v := plan.EEE; !v.IsNull() && !v.IsUnknown() {
		wanted.EEE = v.ValueBool()
	}
	if v := plan.SNMP; !v.IsNull() && !v.IsUnknown() {
		wanted.SNMPEnabled = v.ValueBool()
	}
	if v := plan.IsolationUplink; !v.IsNull() && !v.IsUnknown() {
		wanted.PortIsolationUplink = int(v.ValueInt64())
	}

	if wanted.IGMPSnooping != management.IGMPSnooping ||
		wanted.IGMPUnknownDrop != management.IGMPUnknownDrop ||
		wanted.IGMPStaticRouterPort != management.IGMPStaticRouterPort ||
		wanted.LED != management.LED || wanted.EEE != management.EEE ||
		wanted.SNMPEnabled != management.SNMPEnabled ||
		wanted.PortIsolationUplink != management.PortIsolationUplink {
		after, err := r.client.WriteManagement(ctx, wanted, plan.Force.ValueBool())
		if err != nil {
			addDriverError(diags, "Cannot change the switch's management settings", err)
			return
		}
		management = after
	} else {
		management = wanted
	}

	applySystem(&plan, info, settings, management)
	diags.Append(state.Set(ctx, &plan)...)
}

func applySystem(model *systemModel, info zyxel.DeviceInfo, settings zyxel.SwitchSettings, management zyxel.Management) {
	model.Name = types.StringValue(info.Name)
	model.Model = types.StringValue(info.Model)
	model.Hardware = types.StringValue(info.Hardware)
	model.Firmware = types.StringValue(info.Firmware)
	model.MAC = types.StringValue(info.MAC)
	model.LoopPrevention = types.BoolValue(settings.LoopPrevention)
	model.StormControl = types.BoolValue(settings.StormControl)
	model.StormControlPPS = types.Int64Value(int64(settings.StormRatePPS))

	model.IGMPSnooping = types.BoolValue(management.IGMPSnooping)
	model.IGMPUnknownDrop = types.BoolValue(management.IGMPUnknownDrop)
	model.IGMPRouterPort = types.Int64Value(int64(management.IGMPStaticRouterPort))
	model.LED = types.BoolValue(management.LED)
	model.EEE = types.BoolValue(management.EEE)
	model.SNMP = types.BoolValue(management.SNMPEnabled)
	model.IsolationUplink = types.Int64Value(int64(management.PortIsolationUplink))
	model.ManagementVLAN = types.Int64Value(int64(management.ManagementVLAN))
}
