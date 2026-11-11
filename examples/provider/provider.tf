terraform {
  required_providers {
    endpointcentral = {
      source = "dasslk/endpointcentral"
    }
  }
}

# Credentials usually come from the environment, e.g. through 1Password:
#   ENDPOINTCENTRAL_CLIENT_ID, ENDPOINTCENTRAL_CLIENT_SECRET, ENDPOINTCENTRAL_REFRESH_TOKEN
provider "endpointcentral" {
  region = "eu" # us, eu, in, au, cn, jp, ca, uk or sa: the ending of your console's domain
}
