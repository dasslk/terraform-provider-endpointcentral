# A profile kept in Git.
resource "endpointcentral_mac_custom_profile" "role_dev" {
  name    = "Role: dev"
  content = file("${path.module}/profiles/macos/roles/role-dev.mobileconfig")
}

# A generated profile: when the key rotates, the new profile is published as
# the next version of the same profile and pushed to the Macs in one apply.
resource "endpointcentral_mac_custom_profile" "tailscale_enrollment" {
  name = "Tailscale enrollment"
  content = templatefile("${path.module}/templates/tailscale-enrollment.mobileconfig.tftpl", {
    auth_key = tailscale_tailnet_key.mac_enroll.key
  })
}

# Profiles reach Macs through MDM groups.
resource "endpointcentral_mdm_group" "macos_dev" {
  name = "macos-dev"
}

resource "endpointcentral_mdm_group_profile_assignment" "role_dev" {
  group_id   = endpointcentral_mdm_group.macos_dev.id
  profile_id = endpointcentral_mac_custom_profile.role_dev.id
}
