package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var testProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"endpointcentral": providerserver.NewProtocol6WithError(New("test")()),
}

func providerBlock(f *fakeEC) string {
	return fmt.Sprintf(`
provider "endpointcentral" {
  client_id     = "1000.CLIENTID"
  client_secret = "secret"
  refresh_token = "good-refresh"
  endpoints = {
    api      = %[1]q
    accounts = %[1]q
    mdm      = %[2]q
  }
}
`, f.URL(), f.mdm.server.URL)
}

func checkFake(f *fakeEC, name string, check func(*fakeGroup) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		g := f.groupByName(name)
		if g == nil {
			return fmt.Errorf("group %q not found on fake server", name)
		}
		return check(g)
	}
}

func TestCustomGroup_static(t *testing.T) {
	f := newFakeEC(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "servers" {
  name        = "Windows Servers"
  description = "Production servers"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_custom_group.servers", "id", "301"),
					resource.TestCheckResourceAttr("endpointcentral_custom_group.servers", "type", "computer"),
					resource.TestCheckResourceAttr("endpointcentral_custom_group.servers", "category", "static"),
					resource.TestCheckNoResourceAttr("endpointcentral_custom_group.servers", "criteria_pattern"),
					checkFake(f, "Windows Servers", func(g *fakeGroup) error {
						if g.Type != 1 || g.Category != 1 || len(g.Members) != 0 || g.Description != "Production servers" {
							return fmt.Errorf("unexpected server state: %+v", g)
						}
						return nil
					}),
				),
			},
			// Members added elsewhere survive a rename: the group resource
			// never owns membership.
			{
				PreConfig: func() { f.setMembers("Windows Servers", 101, 102) },
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "servers" {
  name        = "All Windows Servers"
  description = "Production servers"
}`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("endpointcentral_custom_group.servers", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_custom_group.servers", "id", "301"),
					checkFake(f, "All Windows Servers", func(g *fakeGroup) error {
						if len(g.Members) != 2 {
							return fmt.Errorf("rename changed members: %v", g.Members)
						}
						return nil
					}),
				),
			},
			// Changing category forces replacement.
			{
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "servers" {
  name     = "All Windows Servers"
  category = "static_unique"
}`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("endpointcentral_custom_group.servers", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("endpointcentral_custom_group.servers", "category", "static_unique"),
			},
			// Import by ID and by name. description is write-only in the API.
			{
				ResourceName:            "endpointcentral_custom_group.servers",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"description"},
			},
			{
				ResourceName:            "endpointcentral_custom_group.servers",
				ImportState:             true,
				ImportStateId:           "All Windows Servers",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"description"},
			},
		},
	})
}

func TestCustomGroup_dynamic(t *testing.T) {
	f := newFakeEC(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "win11" {
  name     = "Windows 11 PCs"
  category = "dynamic"
  criteria = [
    { column_id = 1, comparator = "contains", values = ["Windows 11"] },
    { column_id = 3, comparator = "equal",    values = ["64-bit"] },
  ]
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_custom_group.win11", "criteria_pattern", "1 AND 2"),
					resource.TestCheckResourceAttr("endpointcentral_custom_group.win11", "criteria.#", "2"),
					checkFake(f, "Windows 11 PCs", func(g *fakeGroup) error {
						if g.Category != 2 || g.CriteriaPattern != "1 AND 2" || len(g.CriteriaList) != 2 {
							return fmt.Errorf("unexpected server state: %+v", g)
						}
						return nil
					}),
				),
			},
			{
				Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "win11" {
  name             = "Windows 11 PCs"
  category         = "dynamic"
  criteria_pattern = "1 OR 2"
  criteria = [
    { column_id = 1, comparator = "contains", values = ["Windows 11"] },
    { column_id = 1, comparator = "contains", values = ["Windows 10"] },
  ]
}`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("endpointcentral_custom_group.win11", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("endpointcentral_custom_group.win11", "criteria_pattern", "1 OR 2"),
			},
			{
				ResourceName:      "endpointcentral_custom_group.win11",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestCustomGroup_validation(t *testing.T) {
	f := newFakeEC(t)
	cases := map[string]struct {
		cfg string
		err string
	}{
		"dynamic without criteria": {`
resource "endpointcentral_custom_group" "g" {
  name     = "x"
  category = "dynamic"
}`, `Dynamic groups require at least one criteria`},
		"static with criteria": {`
resource "endpointcentral_custom_group" "g" {
  name     = "x"
  criteria = [{ column_id = 1, comparator = "equal", values = ["a"] }]
}`, `only apply to dynamic groups`},
		"dynamic user group": {`
resource "endpointcentral_custom_group" "g" {
  name     = "x"
  type     = "user"
  category = "dynamic"
  criteria = [{ column_id = 1, comparator = "equal", values = ["a"] }]
}`, `Dynamic groups only support`},
		"forbidden name chars": {`
resource "endpointcentral_custom_group" "g" {
  name = "a/b"
}`, `must not contain`},
		"members is gone": {`
resource "endpointcentral_custom_group" "g" {
  name    = "x"
  members = ["101"]
}`, `Unsupported argument`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config:      providerBlock(f) + tc.cfg,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.err),
				}},
			})
		})
	}
}
