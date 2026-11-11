data "endpointcentral_computer" "by_name" {
  name = "mac-studio-01"
}

data "endpointcentral_computer" "by_fqdn" {
  fqdn = "mac-studio-01.corp.example.com"
}
