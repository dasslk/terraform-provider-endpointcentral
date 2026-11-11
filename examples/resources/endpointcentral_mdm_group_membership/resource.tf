resource "endpointcentral_mdm_group" "macos_dev" {
  name = "macos-dev"
}

data "endpointcentral_mdm_device" "studio" {
  serial_number = "C02XYZ123"
}

# One device per resource; other members are left alone.
resource "endpointcentral_mdm_group_membership" "studio" {
  group_id  = endpointcentral_mdm_group.macos_dev.id
  device_id = data.endpointcentral_mdm_device.studio.id
}
