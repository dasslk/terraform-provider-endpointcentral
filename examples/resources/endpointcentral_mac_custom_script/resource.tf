# Scripts are added to the Script Repository in the console (the API cannot
# upload them); look them up by file name.
data "endpointcentral_script" "computer_name" {
  name = "set-computer-name.sh"
}

data "endpointcentral_custom_group" "all_macs" {
  name = "All Mac machines"
}

# Runs the script once on every Mac. Any change replaces the configuration
# (Endpoint Central refuses API tokens on its modify call), so create the new
# one before the old one is deleted.
resource "endpointcentral_mac_custom_script" "computer_name" {
  name      = "set-computer-name"
  script_id = data.endpointcentral_script.computer_name.id
  arguments = "--prefix corp"
  frequency = "once"
  target = {
    workgroup_id      = "123456789"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [data.endpointcentral_custom_group.all_macs.id]
  }

  lifecycle {
    create_before_destroy = true
  }
}
