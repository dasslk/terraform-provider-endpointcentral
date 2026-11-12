package provider

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dasslk/terraform-provider-endpointcentral/internal/client"
)

// Group targets: computers in a workgroup, narrowed to custom groups. This is
// how the console stores targets of APD tasks and configurations, e.g.
// WORKGROUP → include "All Mac machines", exclude "test-lab".

var groupTargetAttrTypes = map[string]attr.Type{
	"workgroup_id":      types.StringType,
	"workgroup_name":    types.StringType,
	"include_group_ids": types.SetType{ElemType: types.StringType},
	"exclude_group_ids": types.SetType{ElemType: types.StringType},
}

type groupTargetModel struct {
	WorkgroupID   types.String `tfsdk:"workgroup_id"`
	WorkgroupName types.String `tfsdk:"workgroup_name"`
	Include       types.Set    `tfsdk:"include_group_ids"`
	Exclude       types.Set    `tfsdk:"exclude_group_ids"`
}

func groupTargetSchema(description string, modifiers ...planmodifier.Object) schema.SingleNestedAttribute {
	groupIDs := func(desc string) schema.SetAttribute {
		return schema.SetAttribute{
			ElementType: types.StringType, Optional: true, MarkdownDescription: desc,
			Validators: []validator.Set{setvalidator.ValueStringsAre(numericIDValidator)},
		}
	}
	return schema.SingleNestedAttribute{
		Required:            true,
		MarkdownDescription: description,
		PlanModifiers:       modifiers,
		Attributes: map[string]schema.Attribute{
			"workgroup_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "Workgroup ID (visible on an imported task or configuration).",
				Validators: []validator.String{numericIDValidator},
			},
			"workgroup_name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Workgroup name, e.g. `WORKGROUP`. The API requires the name with the ID.",
			},
			"include_group_ids": groupIDs("Custom group IDs whose computers are targeted."),
			"exclude_group_ids": groupIDs("Custom group IDs whose computers are left out, e.g. macOS VMs."),
		},
	}
}

// buildGroupTarget turns the target into the console's targetData. The API
// requires each target's name with its ID, so the group names are looked up.
func buildGroupTarget(ctx context.Context, c *client.Client, obj types.Object) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	var t groupTargetModel
	diags.Append(obj.As(ctx, &t, objectAsOptions)...)
	if diags.HasError() {
		return nil, diags
	}
	groups, err := c.ListCustomGroups(ctx, true)
	if err != nil {
		diags.AddError("Error listing custom groups for target labels", err.Error())
		return nil, diags
	}
	refine := func(s types.Set) ([]any, error) {
		var ids []string
		diags.Append(s.ElementsAs(ctx, &ids, false)...)
		if len(ids) == 0 {
			return nil, nil
		}
		slices.Sort(ids)
		vals := []any{}
		for _, id := range ids {
			i := slices.IndexFunc(groups, func(g client.CustomGroupSummary) bool { return g.GroupID.String() == id })
			if i < 0 {
				return nil, fmt.Errorf("custom group %s not found", id)
			}
			vals = append(vals, map[string]any{"idValue": id, "label": groups[i].GroupName})
		}
		return []any{map[string]any{"targetType": client.TargetCodeCustomGroupRefine, "selectedValues": vals}}, nil
	}
	refineTarget := map[string]any{}
	for _, part := range []struct {
		key string
		set types.Set
	}{{"includeTargets", t.Include}, {"excludeTargets", t.Exclude}} {
		sel, err := refine(part.set)
		if err != nil {
			diags.AddAttributeError(path.Root("target"), "Invalid target", err.Error())
			return nil, diags
		}
		if sel != nil {
			refineTarget[part.key] = sel
		}
	}
	entry := map[string]any{"mainTarget": map[string]any{"targetType": client.TargetCodeWorkgroup,
		"selectedValues": []any{map[string]any{"idValue": t.WorkgroupID.ValueString(), "label": t.WorkgroupName.ValueString()}}}}
	if len(refineTarget) > 0 {
		entry["computerRefineTarget"] = refineTarget
	}
	return map[string]any{"allTargets": []any{entry}}, diags
}

// readGroupTarget reads the console's targetData into a target. It returns
// false for targets of another shape (left as they are in the state). Empty
// group lists stay unset when they were unset before.
func readGroupTarget(ctx context.Context, targetData any, prior types.Object, diags *diag.Diagnostics) (types.Object, bool) {
	td, _ := targetData.(map[string]any)
	all, _ := td["allTargets"].([]any)
	if len(all) != 1 {
		return prior, false
	}
	e, _ := all[0].(map[string]any)
	main, _ := e["mainTarget"].(map[string]any)
	vals, _ := main["selectedValues"].([]any)
	if fmt.Sprint(main["targetType"]) != client.TargetCodeWorkgroup || len(vals) != 1 {
		return prior, false
	}
	wg, _ := vals[0].(map[string]any)
	ids := func(key string) []string {
		rt, _ := e["computerRefineTarget"].(map[string]any)
		list, _ := rt[key].([]any)
		out := []string{}
		for _, sel := range list {
			s, _ := sel.(map[string]any)
			sv, _ := s["selectedValues"].([]any)
			for _, v := range sv {
				vm, _ := v.(map[string]any)
				out = append(out, fmt.Sprint(vm["idValue"]))
			}
		}
		return out
	}
	var was groupTargetModel
	if !prior.IsNull() && !prior.IsUnknown() {
		diags.Append(prior.As(ctx, &was, objectAsOptions)...)
	}
	groupSet := func(ids []string, before types.Set) types.Set {
		if len(ids) == 0 && (before.IsNull() || prior.IsNull()) {
			return types.SetNull(types.StringType)
		}
		return strSet(ctx, ids, diags)
	}
	return types.ObjectValueMust(groupTargetAttrTypes, map[string]attr.Value{
		"workgroup_id":      types.StringValue(fmt.Sprint(wg["idValue"])),
		"workgroup_name":    types.StringValue(fmt.Sprint(wg["label"])),
		"include_group_ids": groupSet(ids("includeTargets"), was.Include),
		"exclude_group_ids": groupSet(ids("excludeTargets"), was.Exclude),
	}), true
}
