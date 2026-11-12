# A script managed here (or look one up with the endpointcentral_script data
# source).
resource "endpointcentral_script" "computer_name" {
  name    = "set-computer-name.sh"
  content = file("${path.module}/scripts/set-computer-name.sh")
}

data "endpointcentral_custom_group" "all_macs" {
  name = "All Mac machines"
}

# Runs the script once on every Mac. Changes are made in place and reach the
# Macs at their next refresh.
resource "endpointcentral_mac_custom_script" "computer_name" {
  name      = "set-computer-name"
  script_id = endpointcentral_script.computer_name.id
  arguments = "--prefix corp"
  frequency = "once"
  target = {
    workgroup_id      = "123456789"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [data.endpointcentral_custom_group.all_macs.id]
  }
}
