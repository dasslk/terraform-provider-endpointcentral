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

const profileV1 = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>PayloadDisplayName</key><string>Screensaver</string><key>ServerToken</key><string>secret-v1</string></dict></plist>`

const profileV2 = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>PayloadDisplayName</key><string>Screensaver</string><key>ServerToken</key><string>secret-v2</string></dict></plist>`

func profileConfig(f *fakeEC, name, content, extra string) string {
	return providerBlock(f) + fmt.Sprintf(`
resource "endpointcentral_mac_custom_profile" "screensaver" {
  name    = %q
  content = %q
  %s
}`, name, content, extra)
}

func checkProfile(f *fakeEC, name string, check func(*fakeMDMProfile) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		p := f.mdm.profileByName(name)
		if p == nil {
			return fmt.Errorf("no profile %q on the MDM server", name)
		}
		return check(p)
	}
}

func TestMacCustomProfile(t *testing.T) {
	f := newFakeEC(t)
	var firstID int64
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.mdm.profiles) != 0 {
				return fmt.Errorf("%d profiles left on the MDM server (trash and delete expected)", len(f.mdm.profiles))
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: profileConfig(f, "Screensaver", profileV1, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_profile.screensaver", "content_sha256", sha256Hex(profileV1)),
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_profile.screensaver", "version", "1"),
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_profile.screensaver", "status", "Published"),
					checkProfile(f, "Screensaver", func(p *fakeMDMProfile) error {
						firstID = p.ID
						if p.Platform != 6 || p.Published != profileV1 || p.Version != 1 {
							return fmt.Errorf("unexpected profile %+v", p)
						}
						return nil
					}),
				),
			},
			// A new key: published as version 2 of the same profile. It is not
			// assigned anywhere, so nothing is pushed.
			{
				Config: profileConfig(f, "Screensaver", profileV2, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("endpointcentral_mac_custom_profile.screensaver", plancheck.ResourceActionUpdate),
					plancheck.ExpectKnownValue("endpointcentral_mac_custom_profile.screensaver", tfjsonPath("content_sha256"), knownString(sha256Hex(profileV2))),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("endpointcentral_mac_custom_profile.screensaver", "version", "2"),
					checkProfile(f, "Screensaver", func(p *fakeMDMProfile) error {
						if p.ID != firstID || p.Published != profileV2 || f.mdm.updateAlls != 0 {
							return fmt.Errorf("id %d (was %d), published %q, update_all %d", p.ID, firstID, p.Published, f.mdm.updateAlls)
						}
						return nil
					}),
				),
			},
			// Rename and describe in place: no new version.
			{
				Config: profileConfig(f, "Screensaver (all Macs)", profileV2, `description = "VPN"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("endpointcentral_mac_custom_profile.screensaver", plancheck.ResourceActionUpdate),
				}},
				Check: checkProfile(f, "Screensaver (all Macs)", func(p *fakeMDMProfile) error {
					if p.ID != firstID || p.Description != "VPN" || p.Version != 2 {
						return fmt.Errorf("unexpected profile %+v", p)
					}
					return nil
				}),
			},
			// A rename in the console shows up as drift.
			{
				PreConfig: func() {
					f.mu.Lock()
					f.mdm.profiles[firstID].Name = "renamed in console"
					f.mu.Unlock()
				},
				Config:             profileConfig(f, "Screensaver (all Macs)", profileV2, `description = "VPN"`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: profileConfig(f, "Screensaver (all Macs)", profileV2, `description = "VPN"`),
			},
			{
				ResourceName:            "endpointcentral_mac_custom_profile.screensaver",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"content", "content_sha256", "file_name"},
			},
		},
	})
}

// A profile deleted in the console (trashed: the API then answers 500) is
// created again.
func TestMacCustomProfile_trashedInConsole(t *testing.T) {
	f := newFakeEC(t)
	cfg := profileConfig(f, "Screensaver", profileV1, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					f.mu.Lock()
					for _, p := range f.mdm.profiles {
						p.Trashed = true
						p.Name = "trashed"
					}
					f.mu.Unlock()
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("endpointcentral_mac_custom_profile.screensaver", plancheck.ResourceActionCreate),
				}},
			},
		},
	})
}

// Groups, devices, membership and assignment, the way a fleet configuration would use them.
func TestMDMGroupProfileAssignment(t *testing.T) {
	f := newFakeEC(t)
	base := func(content string) string {
		return profileConfig(f, "Engineering", content, "") + `
resource "endpointcentral_mdm_group" "dev" {
  name        = "engineering-macs"
  description = "Macs with the dev role"
}
data "endpointcentral_mdm_device" "vm" {
  name = "MAC-LAB-02"
}
resource "endpointcentral_mdm_group_membership" "vm" {
  group_id  = endpointcentral_mdm_group.dev.id
  device_id = data.endpointcentral_mdm_device.vm.id
}
`
	}
	assignment := `
resource "endpointcentral_mdm_group_profile_assignment" "dev" {
  group_id   = endpointcentral_mdm_group.dev.id
  profile_id = endpointcentral_mac_custom_profile.screensaver.id
}`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.mdm.groups) != 0 || len(f.mdm.profiles) != 0 {
				return fmt.Errorf("left on the MDM server: %d groups, %d profiles", len(f.mdm.groups), len(f.mdm.profiles))
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: base(profileV1) + assignment,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.endpointcentral_mdm_device.vm", "id", "200000000112017"),
					resource.TestCheckResourceAttr("data.endpointcentral_mdm_device.vm", "serial_number", "VMSERIAL002"),
					func(*terraform.State) error {
						g, p := f.mdm.groupByName("engineering-macs"), f.mdm.profileByName("Engineering")
						if g == nil || p == nil || !slices.Equal(g.Members, []int64{200000000112017}) || !slices.Equal(g.Profiles, []int64{p.ID}) {
							return fmt.Errorf("group %+v, profile %+v", g, p)
						}
						return nil
					},
				),
			},
			// A content change on an assigned profile is pushed to its groups.
			{
				Config: base(profileV2) + assignment,
				Check: func(*terraform.State) error {
					if f.mdm.updateAlls != 1 {
						return fmt.Errorf("update_all called %d times, want 1", f.mdm.updateAlls)
					}
					return nil
				},
			},
			// An assignment removed in the console is restored.
			{
				PreConfig: func() {
					f.mu.Lock()
					for _, g := range f.mdm.groups {
						g.Profiles = nil
					}
					f.mu.Unlock()
				},
				Config: base(profileV2) + assignment,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("endpointcentral_mdm_group_profile_assignment.dev", plancheck.ResourceActionCreate),
				}},
			},
			{
				ResourceName:      "endpointcentral_mdm_group_profile_assignment.dev",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "endpointcentral_mdm_group_membership.vm",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "endpointcentral_mdm_group.dev",
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Dropping the assignment unassigns the profile and keeps both objects.
			{
				Config: base(profileV2),
				Check: func(*terraform.State) error {
					g, p := f.mdm.groupByName("engineering-macs"), f.mdm.profileByName("Engineering")
					if g == nil || p == nil || len(g.Profiles) != 0 || len(g.Members) != 1 {
						return fmt.Errorf("group %+v, profile %+v", g, p)
					}
					return nil
				},
			},
		},
	})
}

func TestMDMLookups(t *testing.T) {
	f := newFakeEC(t)
	cases := map[string]struct{ cfg, err string }{
		"removed device is ignored": {`
data "endpointcentral_mdm_device" "d" {
  name = "mac-lab-01"
}`, `found 0`},
		"device by serial": {`
data "endpointcentral_mdm_device" "d" {
  serial_number = "nope"
}`, `found 0`},
		"name and serial": {`
data "endpointcentral_mdm_device" "d" {
  name          = "mac-001"
  serial_number = "C02XYZ123"
}`, `Invalid Attribute Combination`},
		"unknown group": {`
data "endpointcentral_mdm_group" "g" {
  name = "nope"
}`, `no MDM group named "nope"`},
		"empty profile": {`
resource "endpointcentral_mac_custom_profile" "p" {
  name    = "p"
  content = ""
}`, `string length must be at least 1`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps:                    []resource.TestStep{{Config: providerBlock(f) + tc.cfg, ExpectError: regexp.MustCompile(tc.err)}},
			})
		})
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerBlock(f) + `
data "endpointcentral_mdm_device" "d" {
  serial_number = "c02xyz123"
}`,
			Check: resource.TestCheckResourceAttr("data.endpointcentral_mdm_device.d", "name", "mac-001"),
		}},
	})
}

// A profile with a payload type the MDM also has a form for (here the
// firewall) is accepted, as in the console, on create and on update.
func TestMacCustomProfile_mdmSupportedPayload(t *testing.T) {
	f := newFakeEC(t)
	firewall := func(stealth string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>PayloadContent</key><array><dict>
<key>PayloadType</key><string>com.apple.security.firewall</string>
<key>EnableFirewall</key><true/><key>EnableStealthMode</key><` + stealth + `/>
</dict></array><key>PayloadType</key><string>Configuration</string></dict></plist>`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: profileConfig(f, "Firewall", firewall("true"), ""),
				Check:  resource.TestCheckResourceAttr("endpointcentral_mac_custom_profile.screensaver", "version", "1"),
			},
			{
				Config: profileConfig(f, "Firewall", firewall("false"), ""),
				Check:  resource.TestCheckResourceAttr("endpointcentral_mac_custom_profile.screensaver", "version", "2"),
			},
		},
	})
}
