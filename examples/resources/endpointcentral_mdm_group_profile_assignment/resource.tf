data "endpointcentral_mdm_group" "virtual" {
  name = "lab-devices"
}

resource "endpointcentral_mac_custom_profile" "lab_screensaver" {
  name    = "Lab screen saver"
  content = file("${path.module}/profiles/macos/vm/lab-screensaver.mobileconfig")
}

# The group's Macs install the profile; destroying this removes it from them.
resource "endpointcentral_mdm_group_profile_assignment" "lab_screensaver" {
  group_id   = data.endpointcentral_mdm_group.virtual.id
  profile_id = endpointcentral_mac_custom_profile.lab_screensaver.id
}
