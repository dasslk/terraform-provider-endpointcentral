package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

var (
	_ resource.Resource                = &customGroupMembershipResource{}
	_ resource.ResourceWithConfigure   = &customGroupMembershipResource{}
	_ resource.ResourceWithImportState = &customGroupMembershipResource{}
)

// NewCustomGroupMembershipResource manages one member of a static custom
// group, leaving every other member alone (non-authoritative).
func NewCustomGroupMembershipResource() resource.Resource { return &customGroupMembershipResource{} }

type customGroupMembershipResource struct{ client *client.Client }

type customGroupMembershipModel struct {
	ID         types.String `tfsdk:"id"`
	GroupID    types.String `tfsdk:"group_id"`
	ResourceID types.String `tfsdk:"resource_id"`
}

var numericIDValidator = stringvalidator.RegexMatches(numericID, "must be a numeric ID")

func (r *customGroupMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_group_membership"
}

func (r *customGroupMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds one computer (or user) to a static or static unique custom group.\n\n" +
			"This resource is **non-authoritative**: it manages only its own member, so several configurations, " +
			"or members added in the console, can share a group. To own a group's complete member list instead, use " +
			"`endpointcentral_custom_group_memberships_exclusive`; do not use both for the same group.\n\n" +
			"The API can only replace a group's whole member list, so each change reads the list, edits it and writes it back. " +
			"Changes within one `tofu apply` are serialized; two applies running at the same time can overwrite each other's changes.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<group_id>/<resource_id>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of a static or static unique custom group.",
				Validators:          []validator.String{numericIDValidator},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"resource_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Resource ID of the computer (or, for a user group, the user) to add.",
				Validators:          []validator.String{numericIDValidator},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *customGroupMembershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

func (r *customGroupMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan customGroupMembershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	member, _ := strconv.ParseInt(plan.ResourceID.ValueString(), 10, 64)
	err := r.client.ModifyCustomGroupMembers(ctx, plan.GroupID.ValueString(), func(cur []int64) ([]int64, bool) {
		if slices.Contains(cur, member) {
			return cur, false
		}
		return append(cur, member), true
	})
	if err != nil {
		// An InvalidMembersError means the API dropped the member: nothing to save.
		resp.Diagnostics.AddError("Error adding custom group member", err.Error())
		return
	}
	plan.ID = types.StringValue(plan.GroupID.ValueString() + "/" + plan.ResourceID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *customGroupMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state customGroupMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.client.GetCustomGroup(ctx, state.GroupID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading custom group", err.Error())
		return
	}
	member, _ := strconv.ParseInt(state.ResourceID.ValueString(), 10, 64)
	if !slices.Contains(g.MemberIDs(), member) {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update is never called: every attribute requires replacement.
func (r *customGroupMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan customGroupMembershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *customGroupMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state customGroupMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	member, _ := strconv.ParseInt(state.ResourceID.ValueString(), 10, 64)
	err := r.client.ModifyCustomGroupMembers(ctx, state.GroupID.ValueString(), func(cur []int64) ([]int64, bool) {
		out := slices.DeleteFunc(slices.Clone(cur), func(id int64) bool { return id == member })
		return out, len(out) != len(cur)
	})
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error removing custom group member", err.Error())
	}
}

func (r *customGroupMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	group, member, ok := strings.Cut(req.ID, "/")
	if !ok || !numericID.MatchString(group) || !numericID.MatchString(member) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected <group_id>/<resource_id>, got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), group)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_id"), member)...)
}

// ---------------------------------------------------------------------------

var (
	_ resource.Resource                   = &customGroupMembershipsExclusiveResource{}
	_ resource.ResourceWithConfigure      = &customGroupMembershipsExclusiveResource{}
	_ resource.ResourceWithImportState    = &customGroupMembershipsExclusiveResource{}
	_ resource.ResourceWithValidateConfig = &customGroupMembershipsExclusiveResource{}
)

// NewCustomGroupMembershipsExclusiveResource owns a static group's complete
// member list (authoritative), in the style of the AWS provider's
// aws_iam_role_policy_attachments_exclusive.
func NewCustomGroupMembershipsExclusiveResource() resource.Resource {
	return &customGroupMembershipsExclusiveResource{}
}

type customGroupMembershipsExclusiveResource struct{ client *client.Client }

type customGroupMembershipsExclusiveModel struct {
	GroupID     types.String `tfsdk:"group_id"`
	ResourceIDs types.Set    `tfsdk:"resource_ids"`
}

func (r *customGroupMembershipsExclusiveResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_group_memberships_exclusive"
}

func (r *customGroupMembershipsExclusiveResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Owns the **complete** member list of a static or static unique custom group. Members added " +
			"any other way (the console, `endpointcentral_custom_group_membership`) are removed on the next apply, " +
			"and show up in the plan as drift first.\n\n" +
			"Destroying this resource stops managing the list but **leaves the members in the group**, like the AWS " +
			"provider's `*_exclusive` resources. To empty the group, apply with `resource_ids = []` first.",
		Attributes: map[string]schema.Attribute{
			"group_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of a static or static unique custom group.",
				Validators:          []validator.String{numericIDValidator},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"resource_ids": schema.SetAttribute{
				ElementType:         types.StringType,
				Required:            true,
				MarkdownDescription: "Resource IDs of every computer (or user) that should be in the group. `[]` empties it.",
			},
		},
	}
}

func (r *customGroupMembershipsExclusiveResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

func (r *customGroupMembershipsExclusiveResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var ids []types.String
	var cfg customGroupMembershipsExclusiveModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.ResourceIDs.IsUnknown() || cfg.ResourceIDs.IsNull() {
		return
	}
	resp.Diagnostics.Append(cfg.ResourceIDs.ElementsAs(ctx, &ids, false)...)
	for _, id := range ids {
		if !id.IsUnknown() && !numericID.MatchString(id.ValueString()) {
			resp.Diagnostics.AddAttributeError(path.Root("resource_ids"), "Invalid resource ID", fmt.Sprintf("%q is not numeric", id.ValueString()))
		}
	}
}

func (r *customGroupMembershipsExclusiveResource) apply(ctx context.Context, m *customGroupMembershipsExclusiveModel) error {
	var ids []string
	if d := m.ResourceIDs.ElementsAs(ctx, &ids, false); d.HasError() {
		return fmt.Errorf("reading resource_ids")
	}
	desired := make([]int64, 0, len(ids))
	for _, s := range ids {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid resource ID %q", s)
		}
		desired = append(desired, n)
	}
	slices.Sort(desired)
	return r.client.ModifyCustomGroupMembers(ctx, m.GroupID.ValueString(), func(cur []int64) ([]int64, bool) {
		c := slices.Clone(cur)
		slices.Sort(c)
		return desired, !slices.Equal(c, desired)
	})
}

func (r *customGroupMembershipsExclusiveResource) refresh(ctx context.Context, m *customGroupMembershipsExclusiveModel) error {
	g, err := r.client.GetCustomGroup(ctx, m.GroupID.ValueString())
	if err != nil {
		return err
	}
	ids := g.MemberIDs()
	vals := make([]string, len(ids))
	for i, id := range ids {
		vals[i] = strconv.FormatInt(id, 10)
	}
	set, d := types.SetValueFrom(ctx, types.StringType, vals)
	if d.HasError() {
		return fmt.Errorf("building resource_ids")
	}
	m.ResourceIDs = set
	return nil
}

// save writes the planned member list, then stores what the group really
// holds. Members the API rejected are reported as an error and stay out of
// state, so the next plan proposes adding them again.
func (r *customGroupMembershipsExclusiveResource) save(ctx context.Context, m *customGroupMembershipsExclusiveModel, state *tfsdk.State, diags *diag.Diagnostics, verb string) {
	err := r.apply(ctx, m)
	var invalid *client.InvalidMembersError
	if err != nil && !errors.As(err, &invalid) {
		diags.AddError("Error "+verb+" custom group members", err.Error())
		return
	}
	if err := r.refresh(ctx, m); err != nil {
		diags.AddError("Error reading custom group members", err.Error())
		return
	}
	diags.Append(state.Set(ctx, m)...)
	if invalid != nil {
		diags.AddAttributeError(path.Root("resource_ids"), "Some members were rejected", invalid.Error())
	}
}

func (r *customGroupMembershipsExclusiveResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan customGroupMembershipsExclusiveModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.save(ctx, &plan, &resp.State, &resp.Diagnostics, "setting")
}

func (r *customGroupMembershipsExclusiveResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan customGroupMembershipsExclusiveModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.save(ctx, &plan, &resp.State, &resp.Diagnostics, "updating")
}

func (r *customGroupMembershipsExclusiveResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state customGroupMembershipsExclusiveModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.refresh(ctx, &state); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading custom group members", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete only forgets the list; see the resource description.
func (r *customGroupMembershipsExclusiveResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *customGroupMembershipsExclusiveResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !numericID.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected a numeric custom group ID, got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_ids"), types.SetValueMust(types.StringType, nil))...)
}
