package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

// normalizePlatform maps the API's platform spellings ("Mac", "mac", "2") to
// windows / mac / linux.
func normalizePlatform(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if n := client.PlatformName(p); n != "" {
		return n
	}
	if strings.HasPrefix(p, "mac") {
		return "mac"
	}
	return p
}

// ---------------------------------------------------------------------------
// endpointcentral_custom_group

type customGroupDataSource struct{ client *client.Client }

func NewCustomGroupDataSource() datasource.DataSource { return &customGroupDataSource{} }

type customGroupDataModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Category  types.String `tfsdk:"category"`
	MemberIDs types.Set    `tfsdk:"member_ids"`
}

func (d *customGroupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_group"
}

func (d *customGroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a custom group by its exact (case-sensitive) name, including built-in groups such as `All Mac machines`.",
		Attributes: map[string]schema.Attribute{
			"name":       schema.StringAttribute{Required: true, MarkdownDescription: "Exact group name."},
			"id":         schema.StringAttribute{Computed: true, MarkdownDescription: "Group ID."},
			"type":       schema.StringAttribute{Computed: true, MarkdownDescription: "`computer` or `user`."},
			"category":   schema.StringAttribute{Computed: true, MarkdownDescription: "`static`, `static_unique` or `dynamic`."},
			"member_ids": schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Resource IDs of the members of a static group; empty for dynamic groups."},
		},
	}
}

func (d *customGroupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSource(req, resp)
}

func (d *customGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m customGroupDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := d.client.GetCustomGroupByName(ctx, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading custom group", err.Error())
		return
	}
	m.ID = types.StringValue(g.GroupID.String())
	m.Type = types.StringValue(keyFor(groupTypes, g.TypeCode()))
	m.Category = types.StringValue(keyFor(groupCategories, g.CategoryCode()))
	ids := []string{}
	for _, mem := range g.GroupMembers {
		ids = append(ids, mem.ResourceID.String())
	}
	set, diags := types.SetValueFrom(ctx, types.StringType, ids)
	resp.Diagnostics.Append(diags...)
	m.MemberIDs = set
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// ---------------------------------------------------------------------------
// endpointcentral_computer

type computerDataSource struct{ client *client.Client }

func NewComputerDataSource() datasource.DataSource { return &computerDataSource{} }

type computerDataModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	FQDN         types.String `tfsdk:"fqdn"`
	Domain       types.String `tfsdk:"domain"`
	Platform     types.String `tfsdk:"platform"`
	OSName       types.String `tfsdk:"os_name"`
	OSVersion    types.String `tfsdk:"os_version"`
	IPAddress    types.String `tfsdk:"ip_address"`
	MACAddress   types.String `tfsdk:"mac_address"`
	AgentVersion types.String `tfsdk:"agent_version"`
	RemoteOffice types.String `tfsdk:"remote_office"`
}

func (d *computerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_computer"
}

func (d *computerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	exactlyOne := stringvalidator.ExactlyOneOf(path.MatchRoot("name"), path.MatchRoot("fqdn"), path.MatchRoot("id"))
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up one managed computer, for example to get the resource ID that " +
			"`endpointcentral_custom_group_membership` takes. Set exactly one of `id`, `name` or `fqdn`; " +
			"names are compared case-insensitively and must match exactly one computer.",
		Attributes: map[string]schema.Attribute{
			"id":            schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Resource ID.", Validators: []validator.String{exactlyOne, numericIDValidator}},
			"name":          schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Computer name.", Validators: []validator.String{exactlyOne}},
			"fqdn":          schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Fully qualified domain name.", Validators: []validator.String{exactlyOne}},
			"domain":        schema.StringAttribute{Computed: true, MarkdownDescription: "NetBIOS domain or workgroup."},
			"platform":      schema.StringAttribute{Computed: true, MarkdownDescription: "`windows`, `mac` or `linux`."},
			"os_name":       schema.StringAttribute{Computed: true},
			"os_version":    schema.StringAttribute{Computed: true},
			"ip_address":    schema.StringAttribute{Computed: true},
			"mac_address":   schema.StringAttribute{Computed: true},
			"agent_version": schema.StringAttribute{Computed: true},
			"remote_office": schema.StringAttribute{Computed: true, MarkdownDescription: "Name of the remote office the computer belongs to."},
		},
	}
}

func (d *computerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSource(req, resp)
}

func (d *computerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m computerDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var f client.ComputerFilter
	var match func(client.Computer) bool
	switch {
	case !m.ID.IsNull():
		f.ResourceID = m.ID.ValueString()
		match = func(c client.Computer) bool { return c.ResourceID.String() == m.ID.ValueString() }
	case !m.FQDN.IsNull():
		f.FQDN = m.FQDN.ValueString()
		match = func(c client.Computer) bool { return strings.EqualFold(c.FQDNName, m.FQDN.ValueString()) }
	default:
		f.ComputerName = m.Name.ValueString()
		match = func(c client.Computer) bool { return strings.EqualFold(c.ResourceName, m.Name.ValueString()) }
	}
	all, err := d.client.ListComputers(ctx, f)
	if err != nil {
		resp.Diagnostics.AddError("Error listing computers", err.Error())
		return
	}
	var found []client.Computer
	for _, c := range all {
		if match(c) {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		resp.Diagnostics.AddError("Computer not found", fmt.Sprintf("expected exactly one matching computer, found %d", len(found)))
		return
	}
	c := found[0]
	m.ID = types.StringValue(c.ResourceID.String())
	m.Name = types.StringValue(c.ResourceName)
	m.FQDN = types.StringValue(c.FQDNName)
	m.Domain = types.StringValue(c.DomainNetbiosName)
	m.Platform = types.StringValue(normalizePlatform(c.OSPlatformName))
	m.OSName = types.StringValue(c.OSName)
	m.OSVersion = types.StringValue(c.OSVersion)
	m.IPAddress = types.StringValue(c.IPAddress)
	m.MACAddress = types.StringValue(c.MACAddress)
	m.AgentVersion = types.StringValue(c.AgentVersion)
	m.RemoteOffice = types.StringValue(c.BranchOfficeName)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// ---------------------------------------------------------------------------
// endpointcentral_patch_deployment_policy

type deploymentPolicyDataSource struct{ client *client.Client }

func NewDeploymentPolicyDataSource() datasource.DataSource { return &deploymentPolicyDataSource{} }

type deploymentPolicyDataModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Platform    types.String `tfsdk:"platform"`
	System      types.Bool   `tfsdk:"system_created"`
}

func (d *deploymentPolicyDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_patch_deployment_policy"
}

func (d *deploymentPolicyDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a patch deployment policy (deployment window, reboot and notification settings) by exact name. " +
			"The API can create policies but not update or delete them, so they are not a managed resource; create them in the console.",
		Attributes: map[string]schema.Attribute{
			"name":           schema.StringAttribute{Required: true, MarkdownDescription: "Exact policy name."},
			"id":             schema.StringAttribute{Computed: true},
			"description":    schema.StringAttribute{Computed: true},
			"platform":       schema.StringAttribute{Computed: true, MarkdownDescription: "`windows`, `mac` or `linux`."},
			"system_created": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the policy is one ManageEngine ships."},
		},
	}
}

func (d *deploymentPolicyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSource(req, resp)
}

func (d *deploymentPolicyDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m deploymentPolicyDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.client.ListDeploymentPolicies(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error listing deployment policies", err.Error())
		return
	}
	for _, p := range all {
		if p.TemplateName == m.Name.ValueString() {
			m.ID = types.StringValue(p.TemplateID.String())
			m.Description = types.StringValue(p.TemplateDesc)
			m.Platform = types.StringValue(client.PlatformName(p.PlatformID.String()))
			m.System = types.BoolValue(p.SystemCreated)
			resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
			return
		}
	}
	resp.Diagnostics.AddError("Deployment policy not found", fmt.Sprintf("no deployment policy named %q", m.Name.ValueString()))
}

// ---------------------------------------------------------------------------
// endpointcentral_script

type scriptDataSource struct{ client *client.Client }

func NewScriptDataSource() datasource.DataSource { return &scriptDataSource{} }

type scriptDataModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Platform    types.String `tfsdk:"platform"`
	Description types.String `tfsdk:"description"`
	ExitCodes   types.String `tfsdk:"exit_codes"`
}

func (d *scriptDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_script"
}

func (d *scriptDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a script in the Script Repository by file name, e.g. one added in the console, for " +
			"`endpointcentral_mac_custom_script`. To manage a script itself, use the `endpointcentral_script` resource. " +
			"A script's default arguments are not exposed: they may hold secrets.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Exact file name, e.g. `set-computer-name.sh`."},
			"platform": schema.StringAttribute{
				Optional: true, MarkdownDescription: "`mac` (default), `linux` or `windows`.",
				Validators: []validator.String{stringvalidator.OneOf("mac", "linux", "windows")},
			},
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Script ID."},
			"description": schema.StringAttribute{Computed: true},
			"exit_codes":  schema.StringAttribute{Computed: true, MarkdownDescription: "Exit codes that count as success, as entered in the repository."},
		},
	}
}

func (d *scriptDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSource(req, resp)
}

func (d *scriptDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m scriptDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	platform := m.Platform.ValueString()
	if platform == "" {
		platform = "mac"
	}
	all, err := d.client.ListScripts(ctx, platform)
	if err != nil {
		resp.Diagnostics.AddError("Error listing the Script Repository", err.Error())
		return
	}
	var found []client.Script
	for _, s := range all {
		if s.Name == m.Name.ValueString() {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		resp.Diagnostics.AddError("Script not found", fmt.Sprintf("no %s script named %q in the Script Repository", platform, m.Name.ValueString()))
		return
	case 1:
	default:
		resp.Diagnostics.AddError("Script name not unique", fmt.Sprintf("%d %s scripts are named %q", len(found), platform, m.Name.ValueString()))
		return
	}
	m.ID = types.StringValue(found[0].ID.String())
	m.Platform = types.StringValue(platform)
	m.Description = types.StringValue(cleanDescription(found[0].Description))
	m.ExitCodes = types.StringValue(found[0].ExitCodes)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
