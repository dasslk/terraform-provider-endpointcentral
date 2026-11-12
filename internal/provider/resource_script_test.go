package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const scriptV1 = "#!/bin/sh\nscutil --set ComputerName \"$1\"\n"
const scriptV2 = "#!/bin/sh\nset -e\nscutil --set ComputerName \"$1\"\n"

func repoScriptConfig(f *fakeEC, content, extra string) string {
	return providerBlock(f) + fmt.Sprintf(`
resource "endpointcentral_script" "hostname" {
  name        = "rename-mac.sh"
  content     = %q
  description = "Sets the computer name"
  %s
}`, content, extra)
}

// scriptNamed returns a copy of the script on the fake server (callers must
// not hold the lock).
func (f *fakeEC) scriptNamed(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.scripts {
		if s["scriptName"] == name {
			cp := map[string]any{}
			for k, v := range s {
				cp[k] = v
			}
			return cp
		}
	}
	return nil
}

func checkScript(f *fakeEC, check func(map[string]any) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		s := f.scriptNamed("rename-mac.sh")
		if s == nil {
			return fmt.Errorf("rename-mac.sh not in the Script Repository")
		}
		return check(s)
	}
}

func TestScript(t *testing.T) {
	f := newFakeEC(t)
	var id string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if f.scriptNamed("rename-mac.sh") != nil {
				return fmt.Errorf("script left in the Script Repository")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: repoScriptConfig(f, scriptV1, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_script.hostname", "platform", "mac"),
					resource.TestCheckResourceAttr("endpointcentral_script.hostname", "content_sha256", sha256Hex(scriptV1)),
					resource.TestCheckResourceAttr("endpointcentral_script.hostname", "success_exit_codes.#", "1"),
					resource.TestCheckResourceAttrWith("endpointcentral_script.hostname", "id", func(v string) error { id = v; return nil }),
					checkScript(f, func(s map[string]any) error {
						if s["content"] != scriptV1 || s["description"] != "Sets the computer name" || s["exitCodes"] != "0" {
							return fmt.Errorf("unexpected script %v", s)
						}
						return nil
					}),
				),
			},
			{Config: repoScriptConfig(f, scriptV1, ""), PlanOnly: true},
			// New content and arguments: changed in place.
			{
				Config: repoScriptConfig(f, scriptV2, `default_arguments = "corp"
  success_exit_codes = [0, 3]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("endpointcentral_script.hostname", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr("endpointcentral_script.hostname", "id", &id),
					resource.TestCheckResourceAttr("endpointcentral_script.hostname", "content_sha256", sha256Hex(scriptV2)),
					checkScript(f, func(s map[string]any) error {
						if s["content"] != scriptV2 || s["scriptArguments"] != "corp" || s["exitCodes"] != "0,3" {
							return fmt.Errorf("unexpected script %v", s)
						}
						return nil
					}),
				),
			},
			// A console change to the description is drift.
			{
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					for _, s := range f.scripts {
						if s["scriptName"] == "rename-mac.sh" {
							s["description"] = "edited in the console"
						}
					}
				},
				Config: repoScriptConfig(f, scriptV2, `default_arguments = "corp"
  success_exit_codes = [0, 3]`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: repoScriptConfig(f, scriptV2, `default_arguments = "corp"
  success_exit_codes = [0, 3]`),
			},
			{
				ResourceName:            "endpointcentral_script.hostname",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"content", "content_sha256"},
			},
		},
	})
}

// A configuration can use a managed script; deleting both deletes the
// configuration first, which Endpoint Central requires.
func TestScript_usedByConfiguration(t *testing.T) {
	f := newFakeEC(t)
	cfg := repoScriptConfig(f, scriptV1, "") + `
resource "endpointcentral_custom_group" "macs" {
  name = "macos-fleet"
}
resource "endpointcentral_mac_custom_script" "hostname" {
  name      = "rename-macs"
  script_id = endpointcentral_script.hostname.id
  arguments = "corp"
  target = {
    workgroup_id      = "10000000000326019"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [endpointcentral_custom_group.macs.id]
  }
}`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if f.scriptNamed("rename-mac.sh") != nil || len(f.liveConfigs()) != 0 {
				return fmt.Errorf("script or configuration left behind")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_script.hostname", "script_name", "rename-mac.sh"),
					func(*terraform.State) error {
						c, err := onlyConfig(f)
						if err != nil {
							return err
						}
						if n := scriptDetail(c)["name"]; n != "rename-mac.sh" {
							return fmt.Errorf("configuration runs %v", n)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestScript_invalidName(t *testing.T) {
	f := newFakeEC(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
resource "endpointcentral_script" "x" {
  name    = "scripts/x.sh"
  content = "exit 0"
}`,
			ExpectError: regexp.MustCompile(`must be a file name without a path`),
		}},
	})
}
