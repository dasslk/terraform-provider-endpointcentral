data "endpointcentral_computer" "by_name" {
  name = "mac-001"
}

data "endpointcentral_computer" "by_fqdn" {
  fqdn = "mac-001.corp.example.com"
}
