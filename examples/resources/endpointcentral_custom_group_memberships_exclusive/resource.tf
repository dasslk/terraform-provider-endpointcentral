resource "endpointcentral_custom_group" "pilot" {
  name = "pilot-macs"
}

# The complete member list: members added in the console are removed on the
# next apply.
resource "endpointcentral_custom_group_memberships_exclusive" "pilot" {
  group_id     = endpointcentral_custom_group.pilot.id
  resource_ids = ["2001", "2002"]
}
