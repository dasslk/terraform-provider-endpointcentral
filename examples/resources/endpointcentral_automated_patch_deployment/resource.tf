data "endpointcentral_patch_deployment_policy" "notify" {
  name = "Mac - Notify end users to close running applications"
}

data "endpointcentral_custom_group" "all_macs" {
  name = "All Mac machines"
}

data "endpointcentral_custom_group" "test_lab" {
  name = "test-lab"
}

# macOS updates on every physical Mac: all security updates, and
# non-security updates from moderate up.
resource "endpointcentral_automated_patch_deployment" "macos_os" {
  name                 = "macos-os-updates"
  platform             = "mac"
  deployment_policy_id = data.endpointcentral_patch_deployment_policy.notify.id
  os_updates = {
    security     = ["unrated", "low", "moderate", "important", "critical"]
    non_security = ["moderate", "important", "critical"]
  }
  target = {
    workgroup_id      = "10000000000326019"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [data.endpointcentral_custom_group.all_macs.id]
    exclude_group_ids = [data.endpointcentral_custom_group.test_lab.id]
  }
}

# Third-party application updates on every Mac.
resource "endpointcentral_automated_patch_deployment" "macos_apps" {
  name                   = "macos-app-updates"
  platform               = "mac"
  deployment_policy_id   = data.endpointcentral_patch_deployment_policy.notify.id
  third_party_severities = ["low", "moderate", "important", "critical"]
  target = {
    workgroup_id      = "10000000000326019"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [data.endpointcentral_custom_group.all_macs.id]
  }
}
