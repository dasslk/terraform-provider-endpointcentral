package provider

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func expectMembers(f *fakeEC, name string, want ...int64) resource.TestCheckFunc {
	return checkFake(f, name, func(g *fakeGroup) error {
		got := slices.Clone(g.Members)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			return fmt.Errorf("group %q members = %v, want %v", name, got, want)
		}
		return nil
	})
}

func TestCustomGroupMembership(t *testing.T) {
	f := newFakeEC(t)
	group := `
resource "endpointcentral_custom_group" "dev" {
  name        = "macos-dev"
  description = "Macs with the dev role"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			// Two members, one looked up by name: both are added even though
			// they are written concurrently (the API replaces the whole list).
			{
				Config: providerBlock(f) + group + `
data "endpointcentral_computer" "ws" {
  name = "ws-01"
}
resource "endpointcentral_custom_group_membership" "dc" {
  group_id    = endpointcentral_custom_group.dev.id
  resource_id = "101"
}
resource "endpointcentral_custom_group_membership" "ws" {
  group_id    = endpointcentral_custom_group.dev.id
  resource_id = data.endpointcentral_computer.ws.id
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_custom_group_membership.dc", "id", "301/101"),
					resource.TestCheckResourceAttr("data.endpointcentral_computer.ws", "id", "104"),
					resource.TestCheckResourceAttr("data.endpointcentral_computer.ws", "platform", "mac"),
					expectMembers(f, "macos-dev", 101, 104),
				),
			},
			// Non-authoritative: a member added in the console is left alone,
			// and changing the group's description keeps every member.
			{
				PreConfig: func() { f.setMembers("macos-dev", 101, 104, 103) },
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "dev" {
  name        = "macos-dev"
  description = "Changed"
}
resource "endpointcentral_custom_group_membership" "dc" {
  group_id    = endpointcentral_custom_group.dev.id
  resource_id = "101"
}
resource "endpointcentral_custom_group_membership" "ws" {
  group_id    = endpointcentral_custom_group.dev.id
  resource_id = "104"
}`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("endpointcentral_custom_group_membership.dc", plancheck.ResourceActionNoop),
						plancheck.ExpectResourceAction("endpointcentral_custom_group.dev", plancheck.ResourceActionUpdate),
					},
				},
				Check: expectMembers(f, "macos-dev", 101, 103, 104),
			},
			// Removing one membership removes only that member.
			{
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "dev" {
  name        = "macos-dev"
  description = "Changed"
}
resource "endpointcentral_custom_group_membership" "dc" {
  group_id    = endpointcentral_custom_group.dev.id
  resource_id = "101"
}`,
				Check: expectMembers(f, "macos-dev", 101, 103),
			},
			// A member removed in the console is re-added.
			{
				PreConfig: func() { f.setMembers("macos-dev", 103) },
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "dev" {
  name        = "macos-dev"
  description = "Changed"
}
resource "endpointcentral_custom_group_membership" "dc" {
  group_id    = endpointcentral_custom_group.dev.id
  resource_id = "101"
}`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("endpointcentral_custom_group_membership.dc", plancheck.ResourceActionCreate),
					},
				},
				Check: expectMembers(f, "macos-dev", 101, 103),
			},
			{
				ResourceName:      "endpointcentral_custom_group_membership.dc",
				ImportState:       true,
				ImportStateId:     "301/101",
				ImportStateVerify: true,
			},
		},
	})
}

func TestCustomGroupMembership_errors(t *testing.T) {
	f := newFakeEC(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				// Unknown computer: the API reports it as rejected.
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "g" {
  name = "g"
}
resource "endpointcentral_custom_group_membership" "m" {
  group_id    = endpointcentral_custom_group.g.id
  resource_id = "999"
}`,
				ExpectError: regexp.MustCompile(`(?s)rejected.*999`),
			},
			{
				// Dynamic groups have no member list to edit.
				Config: providerBlock(f) + `
data "endpointcentral_custom_group" "all_macs" {
  name = "All Mac machines"
}
resource "endpointcentral_custom_group_membership" "m" {
  group_id    = data.endpointcentral_custom_group.all_macs.id
  resource_id = "101"
}`,
				ExpectError: regexp.MustCompile(`is dynamic`),
			},
		},
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
resource "endpointcentral_custom_group_membership" "m" {
  group_id    = "abc"
  resource_id = "101"
}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`must be a numeric ID`),
		}},
	})
}

func TestCustomGroupMembershipsExclusive(t *testing.T) {
	f := newFakeEC(t)
	cfg := func(ids string) string {
		return providerBlock(f) + `
resource "endpointcentral_custom_group" "canary" {
  name = "macos-canary"
}
resource "endpointcentral_custom_group_memberships_exclusive" "canary" {
  group_id     = endpointcentral_custom_group.canary.id
  resource_ids = ` + ids + `
}`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		// Destroying the exclusive resource leaves the members in place.
		CheckDestroy: func(*terraform.State) error { return nil },
		Steps: []resource.TestStep{
			{
				Config: cfg(`["101", "102"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_custom_group_memberships_exclusive.canary", "resource_ids.#", "2"),
					expectMembers(f, "macos-canary", 101, 102),
				),
			},
			// Authoritative: a member added in the console shows as drift...
			{
				PreConfig:          func() { f.setMembers("macos-canary", 101, 102, 103) },
				Config:             cfg(`["101", "102"]`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			// ...and is removed by the next apply.
			{
				Config: cfg(`["101", "102"]`),
				Check:  expectMembers(f, "macos-canary", 101, 102),
			},
			{
				Config: cfg(`["102", "103"]`),
				Check:  expectMembers(f, "macos-canary", 102, 103),
			},
			// A rejected ID is reported and left out of state.
			{
				Config:      cfg(`["102", "999"]`),
				ExpectError: regexp.MustCompile(`(?s)rejected.*999`),
			},
			{
				Config: cfg(`[]`),
				Check:  expectMembers(f, "macos-canary"),
			},
			{
				Config: cfg(`["101"]`),
			},
			{
				ResourceName:                         "endpointcentral_custom_group_memberships_exclusive.canary",
				ImportState:                          true,
				ImportStateId:                        "301",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "group_id",
			},
		},
	})
}

func TestCustomGroupMembershipsExclusive_destroyKeepsMembers(t *testing.T) {
	f := newFakeEC(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "g" {
  name = "keep"
}
resource "endpointcentral_custom_group_memberships_exclusive" "g" {
  group_id     = endpointcentral_custom_group.g.id
  resource_ids = ["101"]
}`,
			},
			// Drop only the exclusive resource: the members stay.
			{
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "g" {
  name = "keep"
}`,
				Check: expectMembers(f, "keep", 101),
			},
		},
	})
}
