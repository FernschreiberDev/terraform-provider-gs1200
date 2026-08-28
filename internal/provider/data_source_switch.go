package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/FernschreiberDev/terraform-provider-schaltwerk/internal/zyxel"
)

var (
	_ datasource.DataSource              = (*switchDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*switchDataSource)(nil)
)

type switchDataSource struct {
	client *zyxel.Client
}

// NewSwitchDataSource is the factory the provider registers.
func NewSwitchDataSource() datasource.DataSource { return &switchDataSource{} }

type switchModel struct {
	Host           types.String `tfsdk:"host"`
	Model          types.String `tfsdk:"model"`
	Firmware       types.String `tfsdk:"firmware"`
	PortCount      types.Int64  `tfsdk:"port_count"`
	VLANEnabled    types.Bool   `tfsdk:"vlan_enabled"`
	ManagementVLAN types.Int64  `tfsdk:"management_vlan"`
	Partial        types.Bool   `tfsdk:"partial"`
	VLANs          types.List   `tfsdk:"vlans"`
	PVIDs          types.Map    `tfsdk:"pvids"`

	Name     types.String `tfsdk:"name"`
	Hardware types.String `tfsdk:"hardware"`
	MAC      types.String `tfsdk:"mac"`
	Gateway  types.String `tfsdk:"gateway"`
	Netmask  types.String `tfsdk:"netmask"`
	UptimeS  types.Int64  `tfsdk:"uptime_seconds"`
	Links    types.List   `tfsdk:"links"`
}

func (d *switchDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_zyxel_switch"
}

func (d *switchDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Everything the switch will report about its 802.1Q configuration. " +
			"Useful for discovering what is on a switch before writing resources for it, and " +
			"for asserting invariants with `check` blocks.",
		Attributes: map[string]schema.Attribute{
			"host":     schema.StringAttribute{Computed: true, MarkdownDescription: "Address this data was read from."},
			"name":     schema.StringAttribute{Computed: true, MarkdownDescription: "Device name."},
			"hardware": schema.StringAttribute{Computed: true, MarkdownDescription: "Hardware revision."},
			"mac":      schema.StringAttribute{Computed: true, MarkdownDescription: "Base MAC address."},
			"gateway":  schema.StringAttribute{Computed: true, MarkdownDescription: "Default gateway the switch uses."},
			"netmask":  schema.StringAttribute{Computed: true, MarkdownDescription: "Netmask of the switch's own address."},
			"uptime_seconds": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Seconds since the switch last booted.",
			},
			"links": schema.ListNestedAttribute{
				Computed: true,
				MarkdownDescription: "Live electrical state per port: whether something is " +
					"plugged in and at what rate. Read without a session, so refreshing it " +
					"never locks the switch's web interface.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"port":     schema.Int64Attribute{Computed: true},
						"up":       schema.BoolAttribute{Computed: true},
						"speed_mb": schema.Int64Attribute{Computed: true, MarkdownDescription: "Negotiated rate; zero when the link is down."},
					},
				},
			},
			"model":    schema.StringAttribute{Computed: true, MarkdownDescription: "Model string, from the login page."},
			"firmware": schema.StringAttribute{Computed: true, MarkdownDescription: "Firmware version, from the login page."},
			"port_count": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Number of physical ports the firmware reports.",
			},
			"vlan_enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether 802.1Q VLAN mode is switched on at all.",
			},
			"management_vlan": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "The VLAN carrying the switch's own traffic. Removing a port " +
					"from it is what the safety guard refuses. Zero when unknown.",
			},
			"partial": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "True when no password was configured, so only the " +
					"unauthenticated VLAN table could be read and `pvids` is empty.",
			},
			"vlans": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Every VLAN in the switch's table, in table order.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"vid":      schema.Int64Attribute{Computed: true},
						"name":     schema.StringAttribute{Computed: true},
						"index":    schema.Int64Attribute{Computed: true},
						"tagged":   schema.SetAttribute{Computed: true, ElementType: types.Int64Type},
						"untagged": schema.SetAttribute{Computed: true, ElementType: types.Int64Type},
					},
				},
			},
			"pvids": schema.MapAttribute{
				Computed:    true,
				ElementType: types.Int64Type,
				MarkdownDescription: "Port number (as a string key) to its native VLAN. Empty " +
					"when `partial` is true.",
			},
		},
	}
}

func (d *switchDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// vlanObjectType mirrors the nested schema above; the framework needs it
// spelled out to build the list value.
var linkObjectType = types.ObjectType{AttrTypes: map[string]attrType{
	"port":     types.Int64Type,
	"up":       types.BoolType,
	"speed_mb": types.Int64Type,
}}

var vlanObjectType = types.ObjectType{AttrTypes: map[string]attrType{
	"vid":      types.Int64Type,
	"name":     types.StringType,
	"index":    types.Int64Type,
	"tagged":   types.SetType{ElemType: types.Int64Type},
	"untagged": types.SetType{ElemType: types.Int64Type},
}}

func (d *switchDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	config, err := d.client.ReadConfig(ctx)
	if err != nil {
		addDriverError(&resp.Diagnostics, "Cannot read the switch's configuration", err)
		return
	}

	// Model and firmware come off the login page and need no session; a
	// failure there should not lose the configuration we already have.
	model, firmware, err := d.client.Identify(ctx)
	if err != nil {
		resp.Diagnostics.AddWarning("Cannot read the switch's model and firmware", err.Error())
	}

	vlans := make([]attrValue, 0, len(config.VLANs))
	for _, entry := range config.VLANs {
		tagged, diags := setFromInts(ctx, entry.Tagged)
		resp.Diagnostics.Append(diags...)
		untagged, diags := setFromInts(ctx, entry.Untagged)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		object, diags := types.ObjectValue(vlanObjectType.AttrTypes, map[string]attrValue{
			"vid":      types.Int64Value(int64(entry.VID)),
			"name":     types.StringValue(entry.Name),
			"index":    types.Int64Value(int64(entry.Index)),
			"tagged":   tagged,
			"untagged": untagged,
		})
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		vlans = append(vlans, object)
	}

	vlanList, diags := types.ListValue(vlanObjectType, vlans)
	resp.Diagnostics.Append(diags...)

	pvids := make(map[string]attrValue, len(config.PVID))
	for port, vid := range config.PVID {
		pvids[itoa(port)] = types.Int64Value(int64(vid))
	}
	pvidMap, diags := types.MapValue(types.Int64Type, pvids)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := d.client.ReadDeviceInfo(ctx)
	if err != nil {
		resp.Diagnostics.AddWarning("Cannot read the switch's identity", err.Error())
	}

	links := []attrValue{}
	status, err := d.client.ReadLinkStatus(ctx)
	if err != nil {
		resp.Diagnostics.AddWarning("Cannot read the ports' live link state", err.Error())
	}
	for _, link := range status {
		object, diags := types.ObjectValue(linkObjectType.AttrTypes, map[string]attrValue{
			"port":     types.Int64Value(int64(link.Port)),
			"up":       types.BoolValue(link.Up),
			"speed_mb": types.Int64Value(int64(link.SpeedMB)),
		})
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		links = append(links, object)
	}
	linkList, diags := types.ListValue(linkObjectType, links)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &switchModel{
		Host:           types.StringValue(d.client.Host),
		Name:           types.StringValue(info.Name),
		Hardware:       types.StringValue(info.Hardware),
		MAC:            types.StringValue(info.MAC),
		Gateway:        types.StringValue(info.Gateway),
		Netmask:        types.StringValue(info.Netmask),
		UptimeS:        types.Int64Value(int64(info.UptimeS)),
		Links:          linkList,
		Model:          types.StringValue(model),
		Firmware:       types.StringValue(firmware),
		PortCount:      types.Int64Value(int64(config.PortCount)),
		VLANEnabled:    types.BoolValue(config.Enabled),
		ManagementVLAN: types.Int64Value(int64(config.ManagementVLAN)),
		Partial:        types.BoolValue(config.Partial),
		VLANs:          vlanList,
		PVIDs:          pvidMap,
	})...)
}
