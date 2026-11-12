resource "endpointcentral_mdm_group" "engineering" {
  name = "engineering-macs"
}

data "endpointcentral_mdm_device" "studio" {
  serial_number = "C02XYZ123"
}

# One device per resource; other members are left alone.
resource "endpointcentral_mdm_group_membership" "studio" {
  group_id  = endpointcentral_mdm_group.engineering.id
  device_id = data.endpointcentral_mdm_device.studio.id
}
