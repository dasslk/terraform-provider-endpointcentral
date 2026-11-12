package provider

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

var (
	_ resource.Resource                   = &apdResource{}
	_ resource.ResourceWithConfigure      = &apdResource{}
	_ resource.ResourceWithImportState    = &apdResource{}
	_ resource.ResourceWithValidateConfig = &apdResource{}
	_ resource.ResourceWithModifyPlan     = &apdResource{}
)

// ModifyPlan keeps status from the state unless enabled changes.
func (r *apdResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state apdModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if !resp.Diagnostics.HasError() && plan.Enabled.Equal(state.Enabled) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), state.Status)...)
	}
}

func NewAutomatedPatchDeploymentResource() resource.Resource { return &apdResource{} }

type apdResource struct{ client *client.Client }

type apdModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	Description          types.String `tfsdk:"description"`
	Platform             types.String `tfsdk:"platform"`
	DeploymentPolicyID   types.String `tfsdk:"deployment_policy_id"`
	OSUpdates            types.Object `tfsdk:"os_updates"`
	ThirdPartySeverities types.Set    `tfsdk:"third_party_severities"`
	Target               types.Object `tfsdk:"target"`
	Enabled              types.Bool   `tfsdk:"enabled"`
	CollectionID         types.String `tfsdk:"collection_id"`
	Status               types.String `tfsdk:"status"`
}

// Severity codes, assumed to follow ManageEngine's usual scale. Seen on a
// live tenant: codes 0-4, the console's "all" for third-party updates being
// 1-4 and for OS security updates 0-4.
var apdSeverities = []string{"unrated", "low", "moderate", "important", "critical"}

var apdPlatformIDs = map[string]int{"windows": 1, "mac": 2, "linux": 3}

// OS update categories: osUpdates[].id.
const (
	osUpdateSecurity    = 0
	osUpdateNonSecurity = 1
)

var osUpdatesAttrTypes = map[string]attr.Type{
	"security":     types.SetType{ElemType: types.StringType},
	"non_security": types.SetType{ElemType: types.StringType},
}

type osUpdatesModel struct {
	Security    types.Set `tfsdk:"security"`
	NonSecurity types.Set `tfsdk:"non_security"`
}

func (r *apdResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_automated_patch_deployment"
}

func (r *apdResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	severities := func(desc string) schema.SetAttribute {
		return schema.SetAttribute{
			ElementType: types.StringType, Optional: true, MarkdownDescription: desc,
			Validators: []validator.Set{setvalidator.ValueStringsAre(stringvalidator.OneOf(apdSeverities...))},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "An Automated Patch Deployment (APD) task: which updates are installed automatically, on which " +
			"computers, under which deployment policy. Every rule is read back, so changes made in the console show up as drift.\n\n" +
			"Uses the calls Endpoint Central's console makes (`/dcapi/patch/autopatchdeployment`); the documented 1.4 APD calls " +
			"are not available on Endpoint Central Cloud. Settings this resource does not model (notifications, expiry, " +
			"application include/exclude lists, delays) are kept as they are on updates.\n\n" +
			"Severities are `unrated`, `low`, `moderate`, `important`, `critical` (codes 0-4).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Task ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Task name.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 250)},
			},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Task description."},
			"platform": schema.StringAttribute{
				Required: true, MarkdownDescription: "`mac`, `windows` or `linux`. Changing it creates a new task.",
				Validators:    []validator.String{stringvalidator.OneOf("mac", "windows", "linux")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"deployment_policy_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "Deployment policy ID, e.g. from `endpointcentral_patch_deployment_policy`.",
				Validators: []validator.String{numericIDValidator},
			},
			"os_updates": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Operating system updates to deploy, by category and severity. Unset: none.",
				Attributes: map[string]schema.Attribute{
					"security":     severities("Severities of security updates to deploy."),
					"non_security": severities("Severities of non-security updates to deploy."),
				},
			},
			"third_party_severities": severities("Severities of third-party application updates to deploy (all applications). Unset: none."),
			"target": groupTargetSchema("Computers in a workgroup, narrowed to custom groups. The console stores targets this way, e.g. " +
				"`WORKGROUP` → include *All Mac machines*, exclude `test-lab`."),
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "`false` suspends the task.",
			},
			"collection_id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "ID of the configuration behind the task.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{Computed: true, MarkdownDescription: "`Active` or `suspended`."},
		},
	}
}

func (r *apdResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m apdModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.OSUpdates.IsUnknown() || m.ThirdPartySeverities.IsUnknown() {
		return
	}
	n := len(m.ThirdPartySeverities.Elements())
	if !m.OSUpdates.IsNull() {
		var o osUpdatesModel
		resp.Diagnostics.Append(m.OSUpdates.As(ctx, &o, objectAsOptions)...)
		n += len(o.Security.Elements()) + len(o.NonSecurity.Elements())
	}
	if n == 0 {
		resp.Diagnostics.AddError("No updates selected", "Set os_updates or third_party_severities.")
	}
}

func (r *apdResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

func severityCodes(ctx context.Context, s types.Set, diags *diag.Diagnostics) []any {
	codes := []any{}
	if s.IsNull() || s.IsUnknown() {
		return codes
	}
	var names []string
	diags.Append(s.ElementsAs(ctx, &names, false)...)
	for i, name := range apdSeverities { // in code order
		if slices.Contains(names, name) {
			codes = append(codes, i)
		}
	}
	return codes
}

func severityNames(v any) []string {
	list, _ := v.([]any)
	var codes []int
	for _, c := range list {
		if n, ok := c.(float64); ok {
			codes = append(codes, int(n))
		}
	}
	slices.Sort(codes)
	names := []string{}
	for _, c := range codes {
		if c >= 0 && c < len(apdSeverities) {
			names = append(names, apdSeverities[c])
		}
	}
	return names
}

func strSet(ctx context.Context, vals []string, diags *diag.Diagnostics) types.Set {
	if vals == nil {
		vals = []string{}
	}
	s, d := types.SetValueFrom(ctx, types.StringType, vals)
	diags.Append(d...)
	return s
}

// apply writes the managed fields onto body (the task's current writable
// settings, or the defaults for a new task).
func (r *apdResource) apply(ctx context.Context, m apdModel, body map[string]any) diag.Diagnostics {
	var diags diag.Diagnostics
	body["label"] = m.Name.ValueString()
	body["description"] = m.Description.ValueString()
	body["platformId"] = strconv.Itoa(apdPlatformIDs[m.Platform.ValueString()])
	body["templateId"] = m.DeploymentPolicyID.ValueString()

	os := []any{}
	if !m.OSUpdates.IsNull() {
		var o osUpdatesModel
		diags.Append(m.OSUpdates.As(ctx, &o, objectAsOptions)...)
		if sev := severityCodes(ctx, o.Security, &diags); len(sev) > 0 {
			os = append(os, map[string]any{"id": osUpdateSecurity, "severities": sev})
		}
		if sev := severityCodes(ctx, o.NonSecurity, &diags); len(sev) > 0 {
			os = append(os, map[string]any{"id": osUpdateNonSecurity, "severities": sev})
		}
	}
	body["osUpdates"] = os
	body["tpSeverities"] = severityCodes(ctx, m.ThirdPartySeverities, &diags)

	td, d := buildGroupTarget(ctx, r.client, m.Target)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}
	body["targetData"] = td
	return diags
}

// refresh reads the task into the model. Targets that do not have the
// workgroup-plus-groups shape are left as they are in the state.
func (r *apdResource) refresh(ctx context.Context, m *apdModel) diag.Diagnostics {
	var diags diag.Diagnostics
	task, err := r.client.GetAPDTask(ctx, m.ID.ValueString())
	if err != nil {
		diags.AddError("Error reading automated patch deployment task", err.Error())
		return diags
	}
	str := func(k string) string { s, _ := task[k].(string); return s }
	m.Name = types.StringValue(str("label"))
	if d := cleanDescription(str("description")); d != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(d)
	}
	if pid, ok := task["platformId"].(float64); ok {
		for name, id := range apdPlatformIDs {
			if id == int(pid) {
				m.Platform = types.StringValue(name)
			}
		}
	}
	m.DeploymentPolicyID = types.StringValue(str("templateId"))
	m.CollectionID = types.StringValue(str("collectionId"))
	m.Status = types.StringValue(str("taskStatus"))
	m.Enabled = types.BoolValue(str("taskStatus") != "suspended")

	sec, nonSec := []string{}, []string{}
	ups, _ := task["osUpdates"].([]any)
	for _, u := range ups {
		um, _ := u.(map[string]any)
		switch id, _ := um["id"].(float64); int(id) {
		case osUpdateSecurity:
			sec = severityNames(um["severities"])
		case osUpdateNonSecurity:
			nonSec = severityNames(um["severities"])
		}
	}
	if len(sec)+len(nonSec) > 0 || !m.OSUpdates.IsNull() {
		// An empty category stays unset when the configuration leaves it unset.
		var prior osUpdatesModel
		if !m.OSUpdates.IsNull() && !m.OSUpdates.IsUnknown() {
			diags.Append(m.OSUpdates.As(ctx, &prior, objectAsOptions)...)
		}
		category := func(vals []string, was types.Set) types.Set {
			if len(vals) == 0 && (m.OSUpdates.IsNull() || was.IsNull()) {
				return types.SetNull(types.StringType)
			}
			return strSet(ctx, vals, &diags)
		}
		m.OSUpdates = types.ObjectValueMust(osUpdatesAttrTypes, map[string]attr.Value{
			"security": category(sec, prior.Security), "non_security": category(nonSec, prior.NonSecurity),
		})
	}
	if tp := severityNames(task["tpSeverities"]); len(tp) > 0 || !m.ThirdPartySeverities.IsNull() {
		m.ThirdPartySeverities = strSet(ctx, tp, &diags)
	}

	if t, ok := readGroupTarget(ctx, task["targetData"], m.Target, &diags); ok {
		m.Target = t
	}
	return diags
}

func (r *apdResource) setEnabled(ctx context.Context, id string, enabled bool) error {
	if enabled {
		return r.client.ResumeAPDTask(ctx, id)
	}
	return r.client.SuspendAPDTask(ctx, id)
}

// finish refreshes and stores the state.
func (r *apdResource) finish(ctx context.Context, plan apdModel, set func(any) diag.Diagnostics, diags *diag.Diagnostics) {
	diags.Append(r.refresh(ctx, &plan)...)
	if !diags.HasError() {
		diags.Append(set(plan)...)
	}
}

func (r *apdResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apdModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := client.APDTaskDefaults()
	resp.Diagnostics.Append(r.apply(ctx, plan, body)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.client.CreateAPDTask(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Error creating automated patch deployment task", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	if !plan.Enabled.ValueBool() {
		if err := r.setEnabled(ctx, id, false); err != nil {
			resp.Diagnostics.AddError("Error suspending automated patch deployment task", err.Error())
			return
		}
	}
	r.finish(ctx, plan, func(v any) diag.Diagnostics { return resp.State.Set(ctx, v) }, &resp.Diagnostics)
}

func (r *apdResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apdModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.GetAPDTask(ctx, state.ID.ValueString()); client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	r.finish(ctx, state, func(v any) diag.Diagnostics { return resp.State.Set(ctx, v) }, &resp.Diagnostics)
}

func (r *apdResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state apdModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	id := state.ID.ValueString()
	current, err := r.client.GetAPDTask(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Error reading automated patch deployment task", err.Error())
		return
	}
	// Start from the task's current settings, so what this resource does not
	// model (notifications, expiry, application lists, delays) is kept.
	body := client.WritableAPDBody(current)
	resp.Diagnostics.Append(r.apply(ctx, plan, body)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A read reports platformId as a number, the console writes a string:
	// compare them alike, so that only toggling enabled sends no update.
	unchanged := client.WritableAPDBody(current)
	unchanged["platformId"] = fmt.Sprint(unchanged["platformId"])
	if !client.EqualJSON(body, unchanged) {
		if err := r.client.UpdateAPDTask(ctx, id, body); err != nil {
			resp.Diagnostics.AddError("Error updating automated patch deployment task", err.Error())
			return
		}
	}
	if plan.Enabled.ValueBool() != state.Enabled.ValueBool() {
		if err := r.setEnabled(ctx, id, plan.Enabled.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Error changing enabled state", err.Error())
			return
		}
	}
	r.finish(ctx, plan, func(v any) diag.Diagnostics { return resp.State.Set(ctx, v) }, &resp.Diagnostics)
}

func (r *apdResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apdModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAPDTask(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) && !client.IsAPDTaskNotFound(err) {
		resp.Diagnostics.AddError("Error deleting automated patch deployment task", err.Error())
	}
}

// ImportState takes the task ID or the exact task name.
func (r *apdResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if !numericID.MatchString(id) {
		t, err := r.client.FindAPDTaskByName(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error importing automated patch deployment task", err.Error())
			return
		}
		id = t.TaskID.String()
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
