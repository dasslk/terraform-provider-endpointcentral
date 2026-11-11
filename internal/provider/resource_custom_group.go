package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

var (
	_ resource.Resource                   = &customGroupResource{}
	_ resource.ResourceWithConfigure      = &customGroupResource{}
	_ resource.ResourceWithImportState    = &customGroupResource{}
	_ resource.ResourceWithValidateConfig = &customGroupResource{}
)

func NewCustomGroupResource() resource.Resource { return &customGroupResource{} }

type customGroupResource struct{ client *client.Client }

type customGroupModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Type            types.String `tfsdk:"type"`
	Category        types.String `tfsdk:"category"`
	Description     types.String `tfsdk:"description"`
	Criteria        types.List   `tfsdk:"criteria"`
	CriteriaPattern types.String `tfsdk:"criteria_pattern"`
}

type criterionModel struct {
	ColumnID   types.Int64  `tfsdk:"column_id"`
	Comparator types.String `tfsdk:"comparator"`
	Values     types.List   `tfsdk:"values"`
}

var criterionAttrTypes = map[string]attr.Type{
	"column_id":  types.Int64Type,
	"comparator": types.StringType,
	"values":     types.ListType{ElemType: types.StringType},
}

// Terraform-facing enum values and their API codes.
var (
	groupTypes = map[string]int{
		"computer": client.CustomGroupTypeComputer,
		"user":     client.CustomGroupTypeUser,
	}
	groupCategories = map[string]int{
		"static":        client.CustomGroupCategoryStatic,
		"static_unique": client.CustomGroupCategoryStaticUnique,
		"dynamic":       client.CustomGroupCategoryDynamic,
	}
	comparators = []string{
		"equal", "not equal", "contains", "not contains", "starts with", "not starts with",
		"ends with", "greater than", "greater or equal", "less than", "less or equal",
		"between", "not between", "on", "after", "before",
	}
	numericID = regexp.MustCompile(`^[0-9]+$`)
	// Characters the API forbids in group names.
	forbiddenNameChars = regexp.MustCompile("^[^`\\\\:;<>?*\"|&#%/]+$")
)

func keyFor(m map[string]int, code int) string {
	for k, v := range m {
		if v == code {
			return k
		}
	}
	return ""
}

func (r *customGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_group"
}

func (r *customGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Endpoint Central custom group (a machine or user group).\n\n" +
			"* **static** and **static_unique** groups start empty. Add members with " +
			"`endpointcentral_custom_group_membership` (one member each, leaves other members alone) or " +
			"`endpointcentral_custom_group_memberships_exclusive` (the complete member list). " +
			"A computer can belong to only one static unique group.\n" +
			"* **dynamic** groups (computers only) compute their members from `criteria`.\n\n" +
			"Updating this resource never changes a static group's members.\n\n" +
			"`type` and `category` cannot be changed after creation; changing them replaces the group.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Custom group resource ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Group name. Must be unique and is case-sensitive (max 100 characters; must not contain `` ` \\ : ; < > ? * \" | & # % / ``).",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 100),
					stringvalidator.RegexMatches(forbiddenNameChars, "must not contain any of ` \\ : ; < > ? * \" | & # % /"),
				},
			},
			"type": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("computer"),
				MarkdownDescription: "Member type: `computer` (default) or `user`.",
				Validators:          []validator.String{stringvalidator.OneOf("computer", "user")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"category": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("static"),
				MarkdownDescription: "Group category: `static` (default), `static_unique` or `dynamic`.",
				Validators:          []validator.String{stringvalidator.OneOf("static", "static_unique", "dynamic")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Group description (max 250 characters). The API does not return the description, " +
					"so changes made outside OpenTofu/Terraform are not detected.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 250)},
			},
			"criteria": schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "Membership rules of a `dynamic` group. Column IDs, comparators and values are tenant specific; " +
					"discover them with the `GET /api/1.4/customgroup/dynamic/criteriaPattern` and `.../dynamic/columnValues` APIs.",
				Validators: []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"column_id": schema.Int64Attribute{
							Required:            true,
							MarkdownDescription: "Criteria column ID.",
						},
						"comparator": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Comparison operator, e.g. `equal`, `contains`, `starts with`.",
							Validators:          []validator.String{stringvalidator.OneOf(comparators...)},
						},
						"values": schema.ListAttribute{
							ElementType:         types.StringType,
							Required:            true,
							MarkdownDescription: "Values to compare against. Multiple values are OR-ed.",
							Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
						},
					},
				},
			},
			"criteria_pattern": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "How `criteria` entries are combined, using 1-based indices, e.g. `(1 OR 2) AND 3`. " +
					"Defaults to joining all criteria with `AND`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *customGroupResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg customGroupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	category := cfg.Category.ValueString()
	if cfg.Category.IsNull() {
		category = "static"
	}
	groupType := cfg.Type.ValueString()
	if cfg.Type.IsNull() {
		groupType = "computer"
	}
	if cfg.Category.IsUnknown() || cfg.Type.IsUnknown() {
		return
	}

	switch category {
	case "dynamic":
		if !cfg.Criteria.IsUnknown() && cfg.Criteria.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("criteria"), "Missing criteria", "Dynamic groups require at least one criteria entry.")
		}
		if groupType != "computer" {
			resp.Diagnostics.AddAttributeError(path.Root("type"), "Unsupported combination", "Dynamic groups only support type = \"computer\".")
		}
	default:
		if !cfg.Criteria.IsNull() || !cfg.CriteriaPattern.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("criteria"), "Invalid attribute", "criteria and criteria_pattern only apply to dynamic groups.")
		}
		if category == "static_unique" && groupType != "computer" {
			resp.Diagnostics.AddAttributeError(path.Root("type"), "Unsupported combination", "Static unique groups only support type = \"computer\".")
		}
	}
}

func (r *customGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResource(req, resp)
}

func (m *customGroupModel) input(ctx context.Context) (client.CustomGroupInput, diag.Diagnostics) {
	var diags diag.Diagnostics
	in := client.CustomGroupInput{
		GroupName:     m.Name.ValueString(),
		GroupType:     groupTypes[m.Type.ValueString()],
		GroupCategory: groupCategories[m.Category.ValueString()],
		Description:   m.Description.ValueString(),
	}
	if id := m.ID.ValueString(); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			diags.AddError("Invalid custom group ID", fmt.Sprintf("%q is not numeric", id))
			return in, diags
		}
		in.GroupID = n
	}

	if in.GroupCategory == client.CustomGroupCategoryDynamic {
		var crit []criterionModel
		diags.Append(m.Criteria.ElementsAs(ctx, &crit, false)...)
		parts := make([]string, len(crit))
		for i, c := range crit {
			var values []string
			diags.Append(c.Values.ElementsAs(ctx, &values, false)...)
			in.CriteriaList = append(in.CriteriaList, client.CustomGroupCriterion{
				ColumnID:   c.ColumnID.ValueInt64(),
				Comparator: c.Comparator.ValueString(),
				// Criteria are combined by criteriaPattern; logicalOperator is
				// still required by the API but ignored.
				LogicalOperator: "AND",
				CriteriaValue:   values,
			})
			parts[i] = strconv.Itoa(i + 1)
		}
		if m.CriteriaPattern.IsNull() || m.CriteriaPattern.IsUnknown() || m.CriteriaPattern.ValueString() == "" {
			m.CriteriaPattern = types.StringValue(strings.Join(parts, " AND "))
		}
		in.CriteriaPattern = m.CriteriaPattern.ValueString()
		return in, diags
	}

	// A new static group starts empty: members are managed by the membership
	// resources. resourceIds is mandatory for static groups, so send [].
	in.ResourceIDs = []int64{}
	if m.CriteriaPattern.IsUnknown() {
		m.CriteriaPattern = types.StringNull()
	}
	return in, diags
}

// apply copies the API view of the group into the model.
func (m *customGroupModel) apply(g *client.CustomGroup) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(g.GroupID.String())
	m.Name = types.StringValue(g.GroupName)
	m.Type = types.StringValue(keyFor(groupTypes, g.TypeCode()))
	m.Category = types.StringValue(keyFor(groupCategories, g.CategoryCode()))

	if g.CategoryCode() == client.CustomGroupCategoryDynamic {
		objs := make([]attr.Value, 0, len(g.CriteriaList))
		for _, c := range g.CriteriaList {
			vals := make([]attr.Value, len(c.CriteriaValue))
			for i, v := range c.CriteriaValue {
				vals[i] = types.StringValue(v)
			}
			obj, d := types.ObjectValue(criterionAttrTypes, map[string]attr.Value{
				"column_id":  types.Int64Value(c.ColumnID.Int64()),
				"comparator": types.StringValue(c.Comparator),
				"values":     types.ListValueMust(types.StringType, vals),
			})
			diags.Append(d...)
			objs = append(objs, obj)
		}
		list, d := types.ListValue(types.ObjectType{AttrTypes: criterionAttrTypes}, objs)
		diags.Append(d...)
		m.Criteria = list
		if g.CriteriaPattern != "" {
			m.CriteriaPattern = types.StringValue(g.CriteriaPattern)
		}
		return diags
	}

	m.Criteria = types.ListNull(types.ObjectType{AttrTypes: criterionAttrTypes})
	m.CriteriaPattern = types.StringNull()
	return diags
}

func (r *customGroupResource) refresh(ctx context.Context, m *customGroupModel) (diag.Diagnostics, error) {
	g, err := r.client.GetCustomGroup(ctx, m.ID.ValueString())
	if err != nil {
		return nil, err
	}
	return m.apply(g), nil
}

func (r *customGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan customGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = types.StringNull()
	in, d := plan.input(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := r.client.CreateCustomGroup(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Error creating custom group", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	// Persist the ID right away so a failure below does not orphan the group.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)

	rd, rerr := r.refresh(ctx, &plan)
	if rerr != nil {
		resp.Diagnostics.AddError("Error reading custom group after create", rerr.Error())
		return
	}
	resp.Diagnostics.Append(rd...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *customGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state customGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.refresh(ctx, &state)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading custom group", err.Error())
		return
	}
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *customGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan customGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, d := plan.input(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.UpdateCustomGroupProperties(ctx, plan.ID.ValueString(), in); err != nil {
		resp.Diagnostics.AddError("Error updating custom group", err.Error())
		return
	}
	rd, rerr := r.refresh(ctx, &plan)
	if rerr != nil {
		resp.Diagnostics.AddError("Error reading custom group after update", rerr.Error())
		return
	}
	resp.Diagnostics.Append(rd...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *customGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state customGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteCustomGroup(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting custom group", err.Error())
	}
}

// ImportState accepts either the numeric group ID or the exact group name.
func (r *customGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if !numericID.MatchString(id) {
		g, err := r.client.GetCustomGroupByName(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Error importing custom group", fmt.Sprintf("looking up group named %q: %s", id, err))
			return
		}
		id = g.GroupID.String()
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
