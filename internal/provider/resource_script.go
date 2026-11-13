package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

var (
	_ resource.Resource                = &scriptResource{}
	_ resource.ResourceWithConfigure   = &scriptResource{}
	_ resource.ResourceWithImportState = &scriptResource{}
	_ resource.ResourceWithModifyPlan  = &scriptResource{}
)

func NewScriptResource() resource.Resource { return &scriptResource{} }

type scriptResource struct{ client *client.Client }

type scriptModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	Platform         types.String `tfsdk:"platform"`
	Content          types.String `tfsdk:"content"`
	ContentSHA256    types.String `tfsdk:"content_sha256"`
	Description      types.String `tfsdk:"description"`
	DefaultArguments types.String `tfsdk:"default_arguments"`
	ExitCodes        types.Set    `tfsdk:"success_exit_codes"`
	Shared           types.Bool   `tfsdk:"shared"`
}

var scriptFileName = regexp.MustCompile(`^[^/\\:*?"<>|]+$`)

func successExitCodesAttribute() schema.SetAttribute {
	return schema.SetAttribute{
		ElementType: types.Int64Type, Optional: true, Computed: true,
		Default:             setdefault.StaticValue(types.SetValueMust(types.Int64Type, []attr.Value{types.Int64Value(0)})),
		MarkdownDescription: "Exit codes that count as success. Default `[0]`.",
		Validators:          []validator.Set{setvalidator.SizeAtLeast(1)},
	}
}

// exitCodesString formats exit codes the way the console sends them.
func exitCodesString(ctx context.Context, s types.Set, diags *diag.Diagnostics) string {
	var codes []int64
	diags.Append(s.ElementsAs(ctx, &codes, false)...)
	slices.Sort(codes)
	out := make([]string, len(codes))
	for i, c := range codes {
		out[i] = strconv.FormatInt(c, 10)
	}
	return strings.Join(out, ",")
}

func exitCodesSet(s string) types.Set {
	codes := []attr.Value{}
	for _, c := range strings.Split(s, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(c), 10, 64); err == nil {
			codes = append(codes, types.Int64Value(n))
		}
	}
	return types.SetValueMust(types.Int64Type, codes)
}

func (r *scriptResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_script"
}

func (r *scriptResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A script in the Script Repository, for `endpointcentral_mac_custom_script`.\n\n" +
			"Uses the calls Endpoint Central's console makes, which need the `DesktopCentralCloud.Common.CREATE` scope " +
			"besides the Configurations scopes. The API does not return a script's content, so `content` changes made in " +
			"the console are not detected; description, exit codes and default arguments are read back. " +
			"Endpoint Central refuses to delete a script that a configuration still uses.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Script ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "File name, e.g. `set-computer-name.sh`. Changing it replaces the script.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 250),
					stringvalidator.RegexMatches(scriptFileName, "must be a file name without a path")},
			},
			"platform": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("mac"), PlanModifiers: replace,
				MarkdownDescription: "`mac` (default), `linux` or `windows`. Changing it replaces the script.",
				Validators:          []validator.String{stringvalidator.OneOf("mac", "linux", "windows")},
			},
			"content": schema.StringAttribute{
				Required: true, MarkdownDescription: "The script itself, e.g. `file(\"scripts/set-computer-name.sh\")`.",
			},
			"content_sha256": schema.StringAttribute{
				Computed: true, MarkdownDescription: "SHA-256 of `content`.",
			},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Script description."},
			"default_arguments": schema.StringAttribute{
				Optional: true, Sensitive: true,
				MarkdownDescription: "Arguments the console fills in when the script is picked. Readable by anyone who can " +
					"list the Script Repository, so keep secrets out of it.",
			},
			"success_exit_codes": successExitCodesAttribute(),
			"shared": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Share the script with other technicians. Default `false`.",
			},
		},
	}
}

func (r *scriptResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

// ModifyPlan shows a content change as a hash change.
func (r *scriptResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan scriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Content.IsUnknown() {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_sha256"), sha256Hex(plan.Content.ValueString()))...)
}

// write uploads the content and returns the script settings to send.
func (r *scriptResource) write(ctx context.Context, m scriptModel) (client.ScriptInput, diag.Diagnostics) {
	var diags diag.Diagnostics
	in := client.ScriptInput{
		Arguments:   m.DefaultArguments.ValueString(),
		Description: m.Description.ValueString(),
		ExitCodes:   exitCodesString(ctx, m.ExitCodes, &diags),
		Shared:      m.Shared.ValueBool(),
		Platform:    m.Platform.ValueString(),
	}
	fid, err := r.client.UploadScriptFile(ctx, m.Name.ValueString(), []byte(m.Content.ValueString()))
	if err != nil {
		diags.AddError("Error uploading script", err.Error())
		return in, diags
	}
	in.ScriptFile.FileID = fid
	return in, diags
}

// refresh reads the script into the model; found is false when it is gone.
func (r *scriptResource) refresh(ctx context.Context, m *scriptModel) (found bool, diags diag.Diagnostics) {
	s, err := r.client.GetScript(ctx, m.ID.ValueString())
	if err != nil {
		// What a missing script answers is not known; the list decides.
		platform := m.Platform.ValueString()
		if platform == "" {
			platform = "mac"
		}
		all, lerr := r.client.ListScripts(ctx, platform)
		if lerr == nil && !slices.ContainsFunc(all, func(s client.Script) bool { return s.ID.String() == m.ID.ValueString() }) {
			return false, diags
		}
		diags.AddError("Error reading script", err.Error())
		return true, diags
	}
	if s.ScriptFile.FileName != "" {
		m.Name = types.StringValue(s.ScriptFile.FileName)
	}
	if s.Platform != "" {
		m.Platform = types.StringValue(s.Platform)
	}
	if d := cleanDescription(s.Description); d != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(d)
	}
	if s.Arguments != "" || !m.DefaultArguments.IsNull() {
		m.DefaultArguments = types.StringValue(s.Arguments)
	}
	m.ExitCodes = exitCodesSet(s.ExitCodes)
	m.Shared = types.BoolValue(s.Shared)
	if !m.Content.IsNull() {
		m.ContentSHA256 = types.StringValue(sha256Hex(m.Content.ValueString()))
	}
	return true, diags
}

func (r *scriptResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan scriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, diags := r.write(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := r.client.CreateScript(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Error adding script to the Script Repository", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	_, diags = r.refresh(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
}

func (r *scriptResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state scriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, diags := r.refresh(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}

// Update uploads the content again and changes the script in place.
func (r *scriptResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state scriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	in, diags := r.write(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.UpdateScript(ctx, plan.ID.ValueString(), in); err != nil {
		resp.Diagnostics.AddError("Error changing script", err.Error())
		return
	}
	_, diags = r.refresh(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
}

func (r *scriptResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state scriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteScript(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting script", err.Error())
	}
}

// ImportState takes the script ID. The content cannot be read, so the first
// apply after an import uploads the configured content again.
func (r *scriptResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !numericID.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import takes the script ID, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
