# Static group. Its members are separate resources
# (endpointcentral_custom_group_membership or ..._memberships_exclusive).
resource "endpointcentral_custom_group" "engineering" {
  name        = "engineering-macs"
  description = "Macs with the dev role"
}

# Static unique group: a computer can only be in one static unique group.
resource "endpointcentral_custom_group" "privileged" {
  name     = "privileged-servers"
  category = "static_unique"
}

# Dynamic group: membership is computed from criteria. Column IDs are tenant
# specific; look them up with GET /api/1.4/customgroup/dynamic/criteriaPattern.
resource "endpointcentral_custom_group" "test_lab" {
  name             = "test-lab"
  category         = "dynamic"
  criteria_pattern = "1 AND 2"
  criteria = [
    { column_id = 7, comparator = "equal", values = ["Mac"] },
    { column_id = 12, comparator = "contains", values = ["VirtualMac"] },
  ]
}
