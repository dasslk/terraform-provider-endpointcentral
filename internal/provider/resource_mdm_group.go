package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

// ---------------------------------------------------------------------------
// endpointcentral_mdm_group

type mdmGroupResource struct{ client *client.Client }

func NewMDMGroupResource() resource.Resource { return &mdmGroupResource{} }

type mdmGroupModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

func (r *mdmGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mdm_group"
}

func (r *mdmGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "An MDM device group: the target of Mac custom profiles. Add devices with " +
			"`endpointcentral_mdm_group_membership` and profiles with `endpointcentral_mdm_group_profile_assignment`.\n\n" +
			"The MDM API cannot rename a group, so changing `name` or `description` replaces it, and with it its members and " +
			"assignments. MDM groups are separate from Endpoint Central custom groups (`endpointcentral_custom_group`).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Group ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Group name. Changing it replaces the group.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 250)}, PlanModifiers: replace,
			},
			"description": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Description. Changing it replaces the group.", PlanModifiers: replace,
			},
		},
	}
}

func (r *mdmGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

func (r *mdmGroupResource) refresh(ctx context.Context, m *mdmGroupModel) error {
	g, err := r.client.GetMDMGroup(ctx, m.ID.ValueString())
	if err != nil {
		return err
	}
	m.Name = types.StringValue(g.Name)
	if d := cleanDescription(g.Description); d != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(d)
	}
	return nil
}

func (r *mdmGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan mdmGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.client.CreateMDMGroup(ctx, plan.Name.ValueString(), plan.Description.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error creating MDM group", err.Error())
		return
	}
	plan.ID = types.StringValue(g.GroupID.String())
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *mdmGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state mdmGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.refresh(ctx, &state); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading MDM group", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is never called: every argument requires replacement.
func (r *mdmGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan mdmGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *mdmGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state mdmGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteMDMGroup(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting MDM group", err.Error())
	}
}

func (r *mdmGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---------------------------------------------------------------------------
// Relationships: one resource per pair, like aws_iam_role_policy_attachment.

// pairResource implements a relationship between a group and one other
// object (a device or a profile). The MDM API adds and removes these one at
// a time, so unlike custom group membership no read-modify-write is needed.
type pairResource struct {
	client      *client.Client
	typeName    string
	otherAttr   string // device_id or profile_id
	description string
	list        func(c *client.Client, ctx context.Context, group string) ([]string, error)
	add, remove func(c *client.Client, ctx context.Context, group, other string) error
}

func NewMDMGroupMembershipResource() resource.Resource {
	return &pairResource{
		typeName: "_mdm_group_membership", otherAttr: "device_id",
		description: "Adds one device to an MDM group. Non-authoritative: other members are left alone. " +
			"The device gets every profile assigned to the group. Find device IDs with the `endpointcentral_mdm_device` data source.",
		list:   (*client.Client).MDMGroupMembers,
		add:    (*client.Client).AddMDMGroupMember,
		remove: (*client.Client).RemoveMDMGroupMember,
	}
}

func NewMDMGroupProfileAssignmentResource() resource.Resource {
	return &pairResource{
		typeName: "_mdm_group_profile_assignment", otherAttr: "profile_id",
		description: "Assigns a Mac custom profile to an MDM group: the group's devices install it. Destroying the " +
			"assignment removes the profile from those devices. Non-authoritative: other assignments of the group are left alone.",
		list:   (*client.Client).MDMGroupProfiles,
		add:    (*client.Client).AssignMDMProfile,
		remove: (*client.Client).UnassignMDMProfile,
	}
}

func (r *pairResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.typeName
}

func (r *pairResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: r.description,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "`<group_id>/<" + r.otherAttr + ">`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "MDM group ID.",
				Validators: []validator.String{numericIDValidator}, PlanModifiers: replace,
			},
			r.otherAttr: schema.StringAttribute{
				Required: true, MarkdownDescription: strings.ReplaceAll(strings.TrimSuffix(r.otherAttr, "_id"), "_", " ") + " ID.",
				Validators: []validator.String{numericIDValidator}, PlanModifiers: replace,
			},
		},
	}
}

func (r *pairResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

func (r *pairResource) get(ctx context.Context, state interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}) (group, other string, diags diag.Diagnostics) {
	var g, o types.String
	diags.Append(state.GetAttribute(ctx, path.Root("group_id"), &g)...)
	diags.Append(state.GetAttribute(ctx, path.Root(r.otherAttr), &o)...)
	return g.ValueString(), o.ValueString(), diags
}

func (r *pairResource) set(ctx context.Context, state *tfsdk.State, group, other string) diag.Diagnostics {
	var diags diag.Diagnostics
	diags.Append(state.SetAttribute(ctx, path.Root("id"), group+"/"+other)...)
	diags.Append(state.SetAttribute(ctx, path.Root("group_id"), group)...)
	diags.Append(state.SetAttribute(ctx, path.Root(r.otherAttr), other)...)
	return diags
}

func (r *pairResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	group, other, d := r.get(ctx, req.Plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.add(r.client, ctx, group, other); err != nil {
		resp.Diagnostics.AddError("Error creating "+strings.TrimPrefix(r.typeName, "_"), err.Error())
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, group, other)...)
}

func (r *pairResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	group, other, d := r.get(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ids, err := r.list(r.client, ctx, group)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading "+strings.TrimPrefix(r.typeName, "_"), err.Error())
		return
	}
	if !slices.Contains(ids, other) {
		resp.State.RemoveResource(ctx)
	}
}

// Update is never called: every argument requires replacement.
func (r *pairResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	group, other, d := r.get(ctx, req.Plan)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(r.set(ctx, &resp.State, group, other)...)
}

func (r *pairResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	group, other, d := r.get(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.remove(r.client, ctx, group, other); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting "+strings.TrimPrefix(r.typeName, "_"), err.Error())
	}
}

func (r *pairResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	group, other, ok := strings.Cut(req.ID, "/")
	if !ok || !numericID.MatchString(group) || !numericID.MatchString(other) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected <group_id>/<%s>, got %q", r.otherAttr, req.ID))
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, group, other)...)
}

// ---------------------------------------------------------------------------
// Data sources

type mdmGroupDataSource struct{ client *client.Client }

func NewMDMGroupDataSource() datasource.DataSource { return &mdmGroupDataSource{} }

func (d *mdmGroupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mdm_group"
}

func (d *mdmGroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		MarkdownDescription: "Looks up an existing MDM group by exact name.",
		Attributes: map[string]dsschema.Attribute{
			"name":        dsschema.StringAttribute{Required: true, MarkdownDescription: "Exact group name."},
			"id":          dsschema.StringAttribute{Computed: true, MarkdownDescription: "Group ID."},
			"description": dsschema.StringAttribute{Computed: true},
			"member_ids":  dsschema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Device IDs in the group."},
		},
	}
}

func (d *mdmGroupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSource(req, resp)
}

func (d *mdmGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var name types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &name)...)
	groups, err := d.client.ListMDMGroups(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error listing MDM groups", err.Error())
		return
	}
	i := slices.IndexFunc(groups, func(g client.MDMGroup) bool { return g.Name == name.ValueString() })
	if i < 0 {
		resp.Diagnostics.AddError("MDM group not found", fmt.Sprintf("no MDM group named %q", name.ValueString()))
		return
	}
	g := groups[i]
	members, err := d.client.MDMGroupMembers(ctx, g.GroupID.String())
	if err != nil {
		resp.Diagnostics.AddError("Error listing MDM group members", err.Error())
		return
	}
	set, diags := types.SetValueFrom(ctx, types.StringType, members)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), g.Name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), g.GroupID.String())...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("description"), cleanDescription(g.Description))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("member_ids"), set)...)
}

type mdmDeviceDataSource struct{ client *client.Client }

func NewMDMDeviceDataSource() datasource.DataSource { return &mdmDeviceDataSource{} }

type mdmDeviceModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	SerialNumber types.String `tfsdk:"serial_number"`
	Model        types.String `tfsdk:"model"`
	UDID         types.String `tfsdk:"udid"`
}

func (d *mdmDeviceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mdm_device"
}

func (d *mdmDeviceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	one := stringvalidator.ExactlyOneOf(path.MatchRoot("name"), path.MatchRoot("serial_number"))
	resp.Schema = dsschema.Schema{
		MarkdownDescription: "Looks up one enrolled MDM device by name or serial number, for its ID " +
			"(`endpointcentral_mdm_group_membership`). Removed devices are ignored.",
		Attributes: map[string]dsschema.Attribute{
			"name":          dsschema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Device name (case-insensitive).", Validators: []validator.String{one}},
			"serial_number": dsschema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Serial number.", Validators: []validator.String{one}},
			"id":            dsschema.StringAttribute{Computed: true, MarkdownDescription: "MDM device ID."},
			"model":         dsschema.StringAttribute{Computed: true},
			"udid":          dsschema.StringAttribute{Computed: true},
		},
	}
}

func (d *mdmDeviceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSource(req, resp)
}

func (d *mdmDeviceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m mdmDeviceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	devices, err := d.client.ListMDMDevices(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error listing MDM devices", err.Error())
		return
	}
	var found []client.MDMDevice
	for _, dev := range devices {
		if dev.Removed() {
			continue
		}
		if (!m.Name.IsNull() && strings.EqualFold(dev.Name, m.Name.ValueString())) ||
			(!m.SerialNumber.IsNull() && strings.EqualFold(dev.SerialNumber, m.SerialNumber.ValueString())) {
			found = append(found, dev)
		}
	}
	if len(found) != 1 {
		resp.Diagnostics.AddError("MDM device not found", fmt.Sprintf("expected exactly one enrolled device, found %d", len(found)))
		return
	}
	dev := found[0]
	m.ID = types.StringValue(dev.DeviceID.String())
	m.Name = types.StringValue(dev.Name)
	m.SerialNumber = types.StringValue(dev.SerialNumber)
	m.Model = types.StringValue(dev.Model)
	m.UDID = types.StringValue(dev.UDID)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
