package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func apdConfig(f *fakeEC, task string) string {
	return providerBlock(f) + `
data "endpointcentral_patch_deployment_policy" "notify" {
  name = "Mac canary window"
}
resource "endpointcentral_custom_group" "all_macs" {
  name = "All Mac machines (test)"
}
resource "endpointcentral_custom_group" "virtual" {
  name = "macos-virtual"
}
` + task
}

const apdOSTask = `
resource "endpointcentral_automated_patch_deployment" "os" {
  name                 = "macos-os-auto-patch"
  platform             = "mac"
  deployment_policy_id = data.endpointcentral_patch_deployment_policy.notify.id
  os_updates = {
    security     = ["unrated", "low", "moderate", "important", "critical"]
    non_security = ["moderate", "important", "critical"]
  }
  target = {
    workgroup_id      = "10000000000326019"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [endpointcentral_custom_group.all_macs.id]
    exclude_group_ids = [endpointcentral_custom_group.virtual.id]
  }
  %s
}`

func checkAPDTask(f *fakeEC, name string, check func(*fakeAPDTask) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		t := f.apdByName(name)
		if t == nil {
			return fmt.Errorf("APD task %q not on the server", name)
		}
		return check(t)
	}
}

func TestAutomatedPatchDeployment(t *testing.T) {
	f := newFakeEC(t)
	cfg := func(extra string) string { return apdConfig(f, fmt.Sprintf(apdOSTask, extra)) }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if len(f.apdTasks) != 0 {
				return fmt.Errorf("APD tasks left on the server")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.endpointcentral_patch_deployment_policy.notify", "id", "7002"),
					resource.TestCheckResourceAttr("endpointcentral_automated_patch_deployment.os", "status", "Active"),
					checkAPDTask(f, "macos-os-auto-patch", func(t *fakeAPDTask) error {
						b := t.Body
						got := fmt.Sprint(b["platformId"], " ", b["templateId"], " ", b["osUpdates"], " ", b["tpSeverities"])
						want := "2 7002 [map[id:0 severities:[0 1 2 3 4]] map[id:1 severities:[2 3 4]]] []"
						if got != want {
							return fmt.Errorf("body = %s, want %s", got, want)
						}
						refine := b["targetData"].(map[string]any)["allTargets"].([]any)[0].(map[string]any)["computerRefineTarget"].(map[string]any)
						labels := fmt.Sprint(refine["includeTargets"], refine["excludeTargets"])
						if !regexp.MustCompile(`All Mac machines \(test\).*macos-virtual`).MatchString(labels) {
							return fmt.Errorf("target labels: %s", labels)
						}
						return nil
					}),
				),
			},
			// Everything read back matches what was written.
			{Config: cfg(""), PlanOnly: true},
			// One OS update category left unset reads back unset (seen live:
			// "Provider produced inconsistent result" before the fix).
			{
				Config: apdConfig(f, `
resource "endpointcentral_automated_patch_deployment" "os" {
  name                 = "macos-os-auto-patch"
  platform             = "mac"
  deployment_policy_id = data.endpointcentral_patch_deployment_policy.notify.id
  os_updates = {
    security = ["critical"]
  }
  target = {
    workgroup_id      = "10000000000326019"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [endpointcentral_custom_group.all_macs.id]
    exclude_group_ids = [endpointcentral_custom_group.virtual.id]
  }
}`),
			},
			{Config: cfg(""), Check: checkAPDTask(f, "macos-os-auto-patch", func(t *fakeAPDTask) error {
				if fmt.Sprint(t.Body["osUpdates"]) != "[map[id:0 severities:[0 1 2 3 4]] map[id:1 severities:[2 3 4]]]" {
					return fmt.Errorf("osUpdates = %v", t.Body["osUpdates"])
				}
				return nil
			})},
			// An update keeps what the resource does not model (here a
			// notification setting changed in the console).
			{
				PreConfig: func() {
					f.mu.Lock()
					t := f.apdTasks[firstAPD(f)]
					t.Body["reportNotificationSettings"].(map[string]any)["enableNotify"] = true
					f.mu.Unlock()
				},
				Config: cfg(`description = "OS updates"
  third_party_severities = ["critical"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("endpointcentral_automated_patch_deployment.os", plancheck.ResourceActionUpdate),
				}},
				Check: checkAPDTask(f, "macos-os-auto-patch", func(t *fakeAPDTask) error {
					if t.Body["description"] != "OS updates" || fmt.Sprint(t.Body["tpSeverities"]) != "[4]" ||
						t.Body["reportNotificationSettings"].(map[string]any)["enableNotify"] != true {
						return fmt.Errorf("unexpected body %v", t.Body)
					}
					return nil
				}),
			},
			// Suspending does not rewrite the rules.
			{
				PreConfig: func() { f.mu.Lock(); f.apdModifies = 0; f.mu.Unlock() },
				Config: cfg(`description = "OS updates"
  third_party_severities = ["critical"]
  enabled = false`),
				Check: checkAPDTask(f, "macos-os-auto-patch", func(t *fakeAPDTask) error {
					if t.Status != "suspended" || f.apdModifies != 0 {
						return fmt.Errorf("status %s, modifies %d", t.Status, f.apdModifies)
					}
					return nil
				}),
			},
			// A rule changed in the console is drift.
			{
				PreConfig: func() {
					f.mu.Lock()
					f.apdTasks[firstAPD(f)].Body["tpSeverities"] = []any{float64(3), float64(4)}
					f.mu.Unlock()
				},
				Config: cfg(`description = "OS updates"
  third_party_severities = ["critical"]
  enabled = false`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: cfg(`description = "OS updates"
  third_party_severities = ["critical"]
  enabled = false`)},
			{
				ResourceName:      "endpointcentral_automated_patch_deployment.os",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "endpointcentral_automated_patch_deployment.os",
				ImportState:       true,
				ImportStateId:     "macos-os-auto-patch",
				ImportStateVerify: true,
			},
			// Deleted in the console: created again.
			{
				PreConfig: func() {
					f.mu.Lock()
					f.apdTasks = map[int64]*fakeAPDTask{}
					f.mu.Unlock()
				},
				Config: cfg(`description = "OS updates"
  third_party_severities = ["critical"]
  enabled = false`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("endpointcentral_automated_patch_deployment.os", plancheck.ResourceActionCreate),
				}},
			},
		},
	})
}

func firstAPD(f *fakeEC) int64 {
	for id := range f.apdTasks {
		return id
	}
	return 0
}

func TestAutomatedPatchDeployment_validation(t *testing.T) {
	f := newFakeEC(t)
	cases := map[string]struct{ cfg, err string }{
		"no updates": {`
resource "endpointcentral_automated_patch_deployment" "t" {
  name                 = "t"
  platform             = "mac"
  deployment_policy_id = "7002"
  target               = { workgroup_id = "1", workgroup_name = "WORKGROUP" }
}`, `Set os_updates or third_party_severities`},
		"bad severity": {`
resource "endpointcentral_automated_patch_deployment" "t" {
  name                   = "t"
  platform               = "mac"
  deployment_policy_id   = "7002"
  third_party_severities = ["urgent"]
  target                 = { workgroup_id = "1", workgroup_name = "WORKGROUP" }
}`, `value must be one of`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps:                    []resource.TestStep{{Config: providerBlock(f) + tc.cfg, PlanOnly: true, ExpectError: regexp.MustCompile(tc.err)}},
			})
		})
	}
	// Unknown deployment policy: the API's own error surfaces.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
resource "endpointcentral_automated_patch_deployment" "t" {
  name                   = "t"
  platform               = "mac"
  deployment_policy_id   = "9999"
  third_party_severities = ["critical"]
  target                 = { workgroup_id = "1", workgroup_name = "WORKGROUP" }
}`,
			ExpectError: regexp.MustCompile(`Invalid deployment\s+policy\s+9999`),
		}},
	})
}
