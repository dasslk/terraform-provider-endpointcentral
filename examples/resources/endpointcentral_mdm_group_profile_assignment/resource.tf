data "endpointcentral_mdm_group" "virtual" {
  name = "macos-mdm-virtual"
}

resource "endpointcentral_mac_custom_profile" "screen_lock_off" {
  name    = "VM screen lock off"
  content = file("${path.module}/profiles/macos/vm/screen-lock-off.mobileconfig")
}

# The group's Macs install the profile; destroying this removes it from them.
resource "endpointcentral_mdm_group_profile_assignment" "screen_lock_off" {
  group_id   = data.endpointcentral_mdm_group.virtual.id
  profile_id = endpointcentral_mac_custom_profile.screen_lock_off.id
}
