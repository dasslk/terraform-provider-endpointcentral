resource "endpointcentral_custom_group" "macos_canary" {
  name = "macos-canary"
}

# The complete member list: members added in the console are removed on the
# next apply.
resource "endpointcentral_custom_group_memberships_exclusive" "macos_canary" {
  group_id     = endpointcentral_custom_group.macos_canary.id
  resource_ids = ["2001", "2002"]
}
