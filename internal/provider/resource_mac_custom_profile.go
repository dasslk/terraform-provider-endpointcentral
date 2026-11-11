package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

var (
	_ resource.Resource                = &macCustomProfileResource{}
	_ resource.ResourceWithConfigure   = &macCustomProfileResource{}
	_ resource.ResourceWithImportState = &macCustomProfileResource{}
	_ resource.ResourceWithModifyPlan  = &macCustomProfileResource{}
)

func NewMacCustomProfileResource() resource.Resource { return &macCustomProfileResource{} }

type macCustomProfileResource struct{ client *client.Client }

type macCustomProfileModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Description   types.String `tfsdk:"description"`
	Content       types.String `tfsdk:"content"`
	ContentSHA256 types.String `tfsdk:"content_sha256"`
	FileName      types.String `tfsdk:"file_name"`
	Version       types.Int64  `tfsdk:"version"`
	Status        types.String `tfsdk:"status"`
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (r *macCustomProfileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mac_custom_profile"
}

func (r *macCustomProfileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A macOS profile built from a `.mobileconfig`, managed through the MDM API " +
			"(`mdm.manageengine.<region>`). Assign it to MDM groups with `endpointcentral_mdm_group_profile_assignment`; " +
			"this resource never assigns it anywhere by itself.\n\n" +
			"Everything changes in place. A new `content` is uploaded and published as the next version of the same profile, " +
			"which is pushed to the groups it is assigned to, so the profile is replaced on the Macs rather than removed and " +
			"added. The API cannot return a profile's content, so changes made to it in the console are not detected; renames " +
			"and description changes are.\n\n" +
			"Needs the `MDMOnDemand.MDMDeviceMgmt.*` scopes. Endpoint Central's own calls for Mac custom profile " +
			"*configurations* only accept a browser session, which is why this uses the MDM API (see DESIGN.md).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Profile ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Profile name. Unique among MDM profiles.",
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 250)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Profile description.",
			},
			"content": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				MarkdownDescription: "The `.mobileconfig` itself, e.g. `file(\"profiles/macos/role-base.mobileconfig\")` or a " +
					"`templatefile(...)`. Sensitive, because profiles can carry secrets such as enrollment keys; it is still stored " +
					"in the state, so keep the state encrypted.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"content_sha256": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SHA-256 of `content`, so a plan shows when the profile changes without showing the profile.",
			},
			"file_name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "File name the profile is uploaded under. Defaults to the name with `.mobileconfig`.",
			},
			"version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Published version; it goes up with every content change.",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Status as reported by the MDM API, e.g. `Published`.",
			},
		},
	}
}

func (r *macCustomProfileResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ModifyPlan shows a content change as a hash change, and keeps version and
// status from the state unless the content changes.
func (r *macCustomProfileResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan, state macCustomProfileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Content.IsUnknown() {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_sha256"), sha256Hex(plan.Content.ValueString()))...)
	if req.State.Raw.IsNull() {
		return
	}
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if plan.Content.Equal(state.Content) && plan.FileName.Equal(state.FileName) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("version"), state.Version)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), state.Status)...)
	}
}

func (m *macCustomProfileModel) fileName() string {
	if f := m.FileName.ValueString(); f != "" {
		return f
	}
	return unsafeFileChars.ReplaceAllString(m.Name.ValueString(), "-") + ".mobileconfig"
}

// publish uploads the content, makes it the profile's payload and publishes
// it. When the profile is already assigned somewhere, the new version is
// pushed to those groups and devices too.
func (r *macCustomProfileResource) publish(ctx context.Context, m *macCustomProfileModel, pushToAssigned bool) error {
	fileID, err := r.client.UploadMacProfileFile(ctx, m.fileName(), []byte(m.Content.ValueString()))
	if err != nil {
		return err
	}
	if err := r.client.SetMacProfileContent(ctx, m.ID.ValueString(), fileID); err != nil {
		return fmt.Errorf("setting the profile's content: %w", err)
	}
	if err := r.client.PublishMDMProfile(ctx, m.ID.ValueString()); err != nil {
		return fmt.Errorf("publishing the profile: %w", err)
	}
	if !pushToAssigned {
		return nil
	}
	p, err := r.client.GetMDMProfile(ctx, m.ID.ValueString())
	if err != nil {
		return err
	}
	if p.AssociatedGrps.Int64() > 0 || p.AssociatedDevs.Int64() > 0 {
		if err := r.client.UpdateMDMProfileEverywhere(ctx, m.ID.ValueString()); err != nil {
			return fmt.Errorf("pushing the new version to the assigned groups and devices: %w", err)
		}
	}
	return nil
}

func (r *macCustomProfileResource) refresh(ctx context.Context, m *macCustomProfileModel) error {
	p, err := r.client.GetMDMProfile(ctx, m.ID.ValueString())
	if err != nil {
		return err
	}
	m.Name = types.StringValue(p.Name)
	if d := cleanDescription(p.Description); d != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(d)
	}
	m.Version = types.Int64Value(p.Version.Int64())
	m.Status = types.StringValue(p.Status)
	if !m.Content.IsNull() {
		m.ContentSHA256 = types.StringValue(sha256Hex(m.Content.ValueString()))
	}
	return nil
}

func (r *macCustomProfileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan macCustomProfileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.CreateMacProfile(ctx, plan.Name.ValueString(), plan.Description.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error creating Mac custom profile", err.Error())
		return
	}
	plan.ID = types.StringValue(p.ProfileID.String())
	// Saved first, so a failure below leaves a tainted resource, not an orphan.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	if err := r.publish(ctx, &plan, false); err != nil {
		resp.Diagnostics.AddError("Error publishing Mac custom profile", err.Error())
		return
	}
	if err := r.refresh(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error reading Mac custom profile", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *macCustomProfileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state macCustomProfileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.refresh(ctx, &state); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading Mac custom profile", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *macCustomProfileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state macCustomProfileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) {
		if err := r.client.UpdateMDMProfile(ctx, plan.ID.ValueString(), plan.Name.ValueString(), plan.Description.ValueString()); err != nil {
			resp.Diagnostics.AddError("Error renaming Mac custom profile", err.Error())
			return
		}
	}
	if !plan.Content.Equal(state.Content) || !plan.FileName.Equal(state.FileName) {
		if err := r.publish(ctx, &plan, true); err != nil {
			resp.Diagnostics.AddError("Error publishing Mac custom profile", err.Error())
			return
		}
	}
	if err := r.refresh(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error reading Mac custom profile", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *macCustomProfileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state macCustomProfileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteMDMProfile(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting Mac custom profile", err.Error())
	}
}

// ImportState takes the profile ID. The content cannot be read back, so the
// first apply after an import publishes the configured content once.
func (r *macCustomProfileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !numericID.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected a numeric profile ID, got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// cleanDescription maps the API's "--" for an empty description to "".
func cleanDescription(s string) string {
	if s == "--" {
		return ""
	}
	return s
}
