# A profile kept in Git.
resource "endpointcentral_mac_custom_profile" "screensaver" {
  name    = "Screen saver"
  content = file("${path.module}/profiles/screensaver.mobileconfig")
}

# A generated profile: when a value changes, the new profile is published as
# the next version of the same profile and pushed to the Macs in one apply.
variable "wifi_password" {
  type      = string
  sensitive = true
}

resource "endpointcentral_mac_custom_profile" "wifi" {
  name = "Office Wi-Fi"
  content = templatefile("${path.module}/templates/wifi.mobileconfig.tftpl", {
    ssid     = "Office"
    password = var.wifi_password
  })
}

# Profiles reach Macs through MDM groups.
resource "endpointcentral_mdm_group" "engineering" {
  name = "engineering-macs"
}

resource "endpointcentral_mdm_group_profile_assignment" "screensaver" {
  group_id   = endpointcentral_mdm_group.engineering.id
  profile_id = endpointcentral_mac_custom_profile.screensaver.id
}
