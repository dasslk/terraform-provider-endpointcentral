package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
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
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Mac custom script configuration: runs a script from the Script Repository on the computers " +
			"of a workgroup, narrowed to custom groups.\n\n" +
			"Changes are made in place with the console's modify call and reach computers at their next refresh. That call " +
			"needs the `DesktopCentralCloud.Common.CREATE` scope besides the Configurations scopes; without it, creating " +
			"works but changes fail with `INVALID_OAUTHSCOPE`.\n\n" +
			"The script comes from the Script Repository: manage it with the `endpointcentral_script` resource, or find one " +
			"added in the console with the `endpointcentral_script` data source.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Configuration (collection) ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Configuration name.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 250)},
			},
			"description": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Configuration description.",
			},
			"script_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "ID of the script in the Script Repository, e.g. from `endpointcentral_script`.",
				Validators: []validator.String{numericIDValidator},
			},
			"arguments": schema.StringAttribute{
				Optional: true, Sensitive: true,
				MarkdownDescription: "Arguments passed to the script. Sensitive: kept out of plan output, but stored in the " +
					"state and readable by anyone who can open the configuration in Endpoint Central.",
			},
			"frequency": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("once"),
				MarkdownDescription: "When the script runs: `once` (default) or `every_startup`.",
				Validators:          []validator.String{stringvalidator.OneOf("once", "every_startup")},
			},
			"success_exit_codes": successExitCodesAttribute(),
			"retries": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(2),
				MarkdownDescription: "Retries after a failure, at the next startup or refresh. `0` disables retrying. Default 2.",
				Validators:          []validator.Int64{int64validator.Between(0, 10)},
			},
			"logging": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Keep the script's output in Endpoint Central. Default `false`.",
			},
			"target": groupTargetSchema("Computers in a workgroup, narrowed to custom groups, e.g. `WORKGROUP` → include " +
				"*All Mac machines*."),
			"script_name": schema.StringAttribute{Computed: true, MarkdownDescription: "File name of the script."},
			"status":      schema.StringAttribute{Computed: true, MarkdownDescription: "Deployment status, e.g. `In Progress`."},
		},
	}
}

func (r *macCustomScriptResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

// body builds the console's create request and returns it with its script
// entry.
func (r *macCustomScriptResource) body(ctx context.Context, m macCustomScriptModel, scriptName string) (map[string]any, map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	exit := exitCodesString(ctx, m.ExitCodes, &diags)
	target, d := buildGroupTarget(ctx, r.client, m.Target)
	diags.Append(d...)
	if diags.HasError() {
		return nil, nil, diags
	}
	retries := m.Retries.ValueInt64()
	detail := map[string]any{
		"executionType": "repository", "execFrequency": scriptFrequencies[m.Frequency.ValueString()],
		"enableLogging": m.Logging.ValueBool(), "updateStatus": false, "dependencyFiles": []any{},
		"arguments": m.Arguments.ValueString(), "command": "", "scriptId": m.ScriptID.ValueString(),
		"exitCodes": exit, "execCount": "1", "execExpTime": "",
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
	}, detail, diags
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
	m.ExitCodes = exitCodesSet(str(detail, "exitCodes"))
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
	body, _, diags := r.body(ctx, plan, script.Name)
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

// Update changes the configuration in place with the console's modify call.
func (r *macCustomScriptResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state macCustomScriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	id := state.ID.ValueString()
	current, err := r.client.GetConfiguration(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Mac custom script configuration", err.Error())
		return
	}
	script, err := r.findScript(ctx, plan.ScriptID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("script_id"), "Script not found", err.Error())
		return
	}
	body, detail, diags := r.body(ctx, plan, script.Name)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The modify call identifies the configuration and its script entry.
	body["collectionId"] = id
	body["configType"] = "Computer"
	body["schedulerSettings"] = map[string]any{"enableInstallAfter": false, "enableExpiry": false}
	if cds, _ := current["configDetails"].([]any); len(cds) == 1 {
		cd, _ := cds[0].(map[string]any)
		if ds, _ := cd["details"].([]any); len(ds) == 1 {
			d, _ := ds[0].(map[string]any)
			if v, ok := d["detailId"]; ok {
				detail["detailId"] = fmt.Sprint(v)
			}
		}
	}
	detail["useCredential"] = 1
	if err := r.client.UpdateConfiguration(ctx, id, body); err != nil {
		resp.Diagnostics.AddError("Error changing Mac custom script configuration", err.Error())
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
