resource "endpointcentral_custom_group" "engineering" {
  name = "engineering-macs"
}

data "endpointcentral_computer" "studio" {
  name = "mac-001"
}

# One member per resource; other members of the group are left alone.
resource "endpointcentral_custom_group_membership" "studio" {
  group_id    = endpointcentral_custom_group.engineering.id
  resource_id = data.endpointcentral_computer.studio.id
}

# Many members from a list of computer names.
variable "dev_macs" {
  type    = set(string)
  default = ["mac-vm-01", "mac-vm-02"]
}

data "endpointcentral_computer" "dev" {
  for_each = var.dev_macs
  name     = each.value
}

resource "endpointcentral_custom_group_membership" "dev" {
  for_each    = data.endpointcentral_computer.dev
  group_id    = endpointcentral_custom_group.engineering.id
  resource_id = each.value.id
}
