package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func scriptConfig(f *fakeEC, script string) string {
	return providerBlock(f) + `
data "endpointcentral_script" "computer_name" {
  name = "set-computer-name.sh"
}
resource "endpointcentral_custom_group" "macs" {
  name = "macos-fleet"
}
resource "endpointcentral_custom_group" "virtual" {
  name = "test-lab"
}
` + script
}

const computerNameScript = `
resource "endpointcentral_mac_custom_script" "computer_name" {
  name      = "set-computer-name"
  script_id = data.endpointcentral_script.computer_name.id
  arguments = %q
  frequency = "every_startup"
  target = {
    workgroup_id      = "10000000000326019"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [endpointcentral_custom_group.macs.id]
    exclude_group_ids = [endpointcentral_custom_group.virtual.id]
  }
  lifecycle {
    create_before_destroy = true
  }
}`

// onlyConfig returns the one live configuration on the fake server.
func onlyConfig(f *fakeEC) (*fakeConfig, error) {
	live := f.liveConfigs()
	if len(live) != 1 {
		return nil, fmt.Errorf("%d live configurations on the server, want 1", len(live))
	}
	return live[0], nil
}

func scriptDetail(c *fakeConfig) map[string]any {
	cd := c.Body["configDetails"].([]any)[0].(map[string]any)
	return cd["details"].([]any)[0].(map[string]any)
}

func TestMacCustomScript(t *testing.T) {
	f := newFakeEC(t)
	var firstID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if n := len(f.liveConfigs()); n != 0 {
				return fmt.Errorf("%d configurations left (trash and delete expected)", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: scriptConfig(f, fmt.Sprintf(computerNameScript, "--prefix v1")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_script.computer_name", "script_name", "set-computer-name.sh"),
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_script.computer_name", "frequency", "every_startup"),
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_script.computer_name", "retries", "2"),
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_script.computer_name", "status", "In Progress"),
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_script.computer_name", "success_exit_codes.#", "1"),
					resource.TestCheckResourceAttrWith("endpointcentral_mac_custom_script.computer_name", "id", func(v string) error {
						firstID = v
						return nil
					}),
					func(*terraform.State) error {
						c, err := onlyConfig(f)
						if err != nil {
							return err
						}
						d := scriptDetail(c)
						if d["scriptId"] != "52501000000401316" || d["name"] != "set-computer-name.sh" ||
							d["arguments"] != "--prefix v1" || d["execFrequency"] != "everyStartup" || d["exitCodes"] != "0" {
							return fmt.Errorf("unexpected script entry %v", d)
						}
						return nil
					},
				),
			},
			// Everything is read back: no drift.
			{Config: scriptConfig(f, fmt.Sprintf(computerNameScript, "--prefix v1")), PlanOnly: true},
			// A change replaces the configuration, the new one first.
			{
				Config: scriptConfig(f, fmt.Sprintf(computerNameScript, "--prefix v2")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("endpointcentral_mac_custom_script.computer_name", plancheck.ResourceActionCreateBeforeDestroy),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("endpointcentral_mac_custom_script.computer_name", "id", func(v string) error {
						if v == firstID {
							return fmt.Errorf("configuration kept ID %s, want a new one", v)
						}
						return nil
					}),
					func(*terraform.State) error {
						c, err := onlyConfig(f)
						if err != nil {
							return err
						}
						if a := scriptDetail(c)["arguments"]; a != "--prefix v2" {
							return fmt.Errorf("arguments = %v", a)
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "endpointcentral_mac_custom_script.computer_name",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"description"},
			},
		},
	})
}

// A configuration deleted outside OpenTofu (the API then answers 401 "Access
// denied") is planned for creation again.
func TestMacCustomScript_deletedOutside(t *testing.T) {
	f := newFakeEC(t)
	cfg := scriptConfig(f, fmt.Sprintf(computerNameScript, "--prefix v1"))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					for _, c := range f.configs {
						c.Trashed, c.Deleted = true, true
					}
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestMacCustomScript_unknownScript(t *testing.T) {
	f := newFakeEC(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
resource "endpointcentral_custom_group" "macs" {
  name = "macos-fleet"
}
resource "endpointcentral_mac_custom_script" "x" {
  name      = "x"
  script_id = "1"
  target = {
    workgroup_id      = "10000000000326019"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [endpointcentral_custom_group.macs.id]
  }
}`,
			ExpectError: regexp.MustCompile(`script 1 is not in the Script Repository`),
		}},
	})
}

func TestScriptDataSource(t *testing.T) {
	f := newFakeEC(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(f) + `
data "endpointcentral_script" "mac" {
  name = "set-computer-name.sh"
}
data "endpointcentral_script" "linux" {
  name     = "linux-setup.sh"
  platform = "linux"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.endpointcentral_script.mac", "id", "52501000000401316"),
					resource.TestCheckResourceAttr("data.endpointcentral_script.mac", "platform", "mac"),
					resource.TestCheckResourceAttr("data.endpointcentral_script.mac", "description", ""),
					resource.TestCheckResourceAttr("data.endpointcentral_script.linux", "id", "52501000000401500"),
				),
			},
			{
				Config:      providerBlock(f) + `data "endpointcentral_script" "x" { name = "dup.sh" }`,
				ExpectError: regexp.MustCompile(`2 mac scripts are named "dup.sh"`),
			},
			{
				Config:      providerBlock(f) + `data "endpointcentral_script" "x" { name = "missing.sh" }`,
				ExpectError: regexp.MustCompile(`no mac script named "missing.sh"`),
			},
		},
	})
}
