package provider

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

var (
	_ resource.Resource                = &macCustomScriptResource{}
	_ resource.ResourceWithConfigure   = &macCustomScriptResource{}
	_ resource.ResourceWithImportState = &macCustomScriptResource{}
)

func NewMacCustomScriptResource() resource.Resource { return &macCustomScriptResource{} }

type macCustomScriptResource struct{ client *client.Client }

type macCustomScriptModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	ScriptID    types.String `tfsdk:"script_id"`
	Arguments   types.String `tfsdk:"arguments"`
	Frequency   types.String `tfsdk:"frequency"`
	ExitCodes   types.Set    `tfsdk:"success_exit_codes"`
	Retries     types.Int64  `tfsdk:"retries"`
	Logging     types.Bool   `tfsdk:"logging"`
	Target      types.Object `tfsdk:"target"`
	ScriptName  types.String `tfsdk:"script_name"`
	Status      types.String `tfsdk:"status"`
}

// Execution frequencies: the provider's names and the API's.
var scriptFrequencies = map[string]string{"once": "once", "every_startup": "everyStartup"}

func (r *macCustomScriptResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mac_custom_script"
}

func (r *macCustomScriptResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Mac custom script configuration: runs a script from the Script Repository on the computers " +
			"of a workgroup, narrowed to custom groups.\n\n" +
			"Endpoint Central lets an API token create, read and delete configurations, but not change them (the console's " +
			"modify call refuses OAuth tokens). **Every change therefore replaces the configuration**: a new one is created " +
			"and the old one deleted, and the script runs again on every target. Use `create_before_destroy` so the targets " +
			"are never without it.\n\n" +
			"Scripts cannot be uploaded through the API either: add them to the Script Repository in the console and find " +
			"them with the `endpointcentral_script` data source.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Configuration (collection) ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Configuration name.", PlanModifiers: replaceStr,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 250)},
			},
			"description": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Configuration description.", PlanModifiers: replaceStr,
			},
			"script_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "ID of the script in the Script Repository, e.g. from `endpointcentral_script`.",
				PlanModifiers: replaceStr, Validators: []validator.String{numericIDValidator},
			},
			"arguments": schema.StringAttribute{
				Optional: true, Sensitive: true, PlanModifiers: replaceStr,
				MarkdownDescription: "Arguments passed to the script. Sensitive: kept out of plan output, but stored in the " +
					"state and readable by anyone who can open the configuration in Endpoint Central.",
			},
			"frequency": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("once"), PlanModifiers: replaceStr,
				MarkdownDescription: "When the script runs: `once` (default) or `every_startup`.",
				Validators:          []validator.String{stringvalidator.OneOf("once", "every_startup")},
			},
			"success_exit_codes": schema.SetAttribute{
				ElementType: types.Int64Type, Optional: true, Computed: true,
				Default:             setdefault.StaticValue(types.SetValueMust(types.Int64Type, []attr.Value{types.Int64Value(0)})),
				MarkdownDescription: "Exit codes that count as success. Default `[0]`.",
				PlanModifiers:       []planmodifier.Set{setplanmodifier.RequiresReplace()},
				Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
			},
			"retries": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(2),
				MarkdownDescription: "Retries after a failure, at the next startup or refresh. `0` disables retrying. Default 2.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
				Validators:          []validator.Int64{int64validator.Between(0, 10)},
			},
			"logging": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Keep the script's output in Endpoint Central. Default `false`.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"target": groupTargetSchema("Computers in a workgroup, narrowed to custom groups, e.g. `WORKGROUP` → include "+
				"*All Mac machines*. Changing it replaces the configuration.", objectplanmodifier.RequiresReplace()),
			"script_name": schema.StringAttribute{
				Computed: true, MarkdownDescription: "File name of the script.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{Computed: true, MarkdownDescription: "Deployment status, e.g. `In Progress`."},
		},
	}
}

func (r *macCustomScriptResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

// body builds the console's create request.
func (r *macCustomScriptResource) body(ctx context.Context, m macCustomScriptModel, scriptName string) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	var codes []int64
	diags.Append(m.ExitCodes.ElementsAs(ctx, &codes, false)...)
	slices.Sort(codes)
	exit := make([]string, len(codes))
	for i, c := range codes {
		exit[i] = strconv.FormatInt(c, 10)
	}
	target, d := buildGroupTarget(ctx, r.client, m.Target)
	diags.Append(d...)
	if diags.HasError() {
		return nil, diags
	}
	retries := m.Retries.ValueInt64()
	detail := map[string]any{
		"executionType": "repository", "execFrequency": scriptFrequencies[m.Frequency.ValueString()],
		"enableLogging": m.Logging.ValueBool(), "updateStatus": false, "dependencyFiles": []any{},
		"arguments": m.Arguments.ValueString(), "command": "", "scriptId": m.ScriptID.ValueString(),
		"exitCodes": strings.Join(exit, ","), "execCount": "1", "execExpTime": "",
		"credentialDetails": map[string]any{"credentialID": "-1", "credentialName": "", "credentialUUID": "--"},
		"name":              scriptName, "scriptDescription": "--", "isMaskScriptArgs": true, "isDynamicArgsPresent": false,
	}
	return map[string]any{
		"platform": "mac", "configType": "computer", "collectionType": 1,
		"label": m.Name.ValueString(), "description": m.Description.ValueString(),
		"noOfRetries": retries, "enableRetry": retries > 0, "logonStartupMinRetry": 1, "refreshMinRetry": 1,
		"enableNotification": false, "email": "", "notificationDuration": 6, "notificationEnabledDays": 3,
		"enableMobileNotificationForUserIDs": []any{},
		"applyAlways":                        false, "applyAtStartupLogon": false, "applyAtRefresh": false,
		"configDetails": []any{map[string]any{"configName": client.MacCustomScriptConfigName, "details": []any{detail}}},
		"targetData":    target,
	}, diags
}

func (r *macCustomScriptResource) findScript(ctx context.Context, id string) (*client.Script, error) {
	scripts, err := r.client.ListScripts(ctx, "mac")
	if err != nil {
		return nil, fmt.Errorf("listing the Script Repository: %w", err)
	}
	for i := range scripts {
		if scripts[i].ID.String() == id {
			return &scripts[i], nil
		}
	}
	return nil, fmt.Errorf("script %s is not in the Script Repository (Mac)", id)
}

// refresh reads the configuration into the model.
func (r *macCustomScriptResource) refresh(ctx context.Context, m *macCustomScriptModel) diag.Diagnostics {
	var diags diag.Diagnostics
	cfg, err := r.client.GetConfiguration(ctx, m.ID.ValueString())
	if err != nil {
		diags.AddError("Error reading Mac custom script configuration", err.Error())
		return diags
	}
	str := func(v map[string]any, k string) string {
		switch x := v[k].(type) {
		case string:
			return x
		case nil:
			return ""
		default:
			return fmt.Sprint(x)
		}
	}
	var detail map[string]any
	if cds, _ := cfg["configDetails"].([]any); len(cds) == 1 {
		cd, _ := cds[0].(map[string]any)
		if str(cd, "configName") == client.MacCustomScriptConfigName {
			if ds, _ := cd["details"].([]any); len(ds) == 1 {
				detail, _ = ds[0].(map[string]any)
			}
		}
	}
	if detail == nil || str(cfg, "platform") != "mac" {
		diags.AddError("Not a Mac custom script configuration",
			fmt.Sprintf("Configuration %s is not a Mac custom script configuration with one script.", m.ID.ValueString()))
		return diags
	}

	m.Name = types.StringValue(str(cfg, "label"))
	if d := cleanDescription(str(cfg, "description")); d != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(d)
	}
	m.ScriptID = types.StringValue(str(detail, "scriptId"))
	m.ScriptName = types.StringValue(str(detail, "name"))
	if a := str(detail, "arguments"); a != "" || !m.Arguments.IsNull() {
		m.Arguments = types.StringValue(a)
	}
	freq := str(detail, "execFrequency")
	for name, api := range scriptFrequencies {
		if api == freq {
			freq = name
		}
	}
	m.Frequency = types.StringValue(freq)
	codes := []attr.Value{}
	for _, c := range strings.Split(str(detail, "exitCodes"), ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(c), 10, 64); err == nil {
			codes = append(codes, types.Int64Value(n))
		}
	}
	m.ExitCodes = types.SetValueMust(types.Int64Type, codes)
	m.Logging = types.BoolValue(detail["enableLogging"] == true)
	retries, _ := strconv.ParseInt(str(cfg, "noOfRetries"), 10, 64)
	if cfg["enableRetry"] == false {
		retries = 0
	}
	m.Retries = types.Int64Value(retries)
	m.Status = types.StringValue(str(cfg, "status"))
	if t, ok := readGroupTarget(ctx, cfg["targetData"], m.Target, &diags); ok {
		m.Target = t
	}
	return diags
}

func (r *macCustomScriptResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan macCustomScriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	script, err := r.findScript(ctx, plan.ScriptID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("script_id"), "Script not found", err.Error())
		return
	}
	body, diags := r.body(ctx, plan, script.Name)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.client.CreateMacCustomScript(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Error creating Mac custom script configuration", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	resp.Diagnostics.Append(r.refresh(ctx, &plan)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
}

func (r *macCustomScriptResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state macCustomScriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.GetConfiguration(ctx, state.ID.ValueString()); client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(r.refresh(ctx, &state)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}

// Update is never called with a real change: every argument forces
// replacement. It only carries the plan over.
func (r *macCustomScriptResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan macCustomScriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.refresh(ctx, &plan)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
}

func (r *macCustomScriptResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state macCustomScriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteConfiguration(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting Mac custom script configuration", err.Error())
	}
}

// ImportState takes the configuration (collection) ID.
func (r *macCustomScriptResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !numericID.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", "Import takes the configuration (collection) ID, e.g. 52501000000401322.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
