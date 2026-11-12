data "endpointcentral_script" "computer_name" {
  name = "set-computer-name.sh"
}

data "endpointcentral_script" "linux_setup" {
  name     = "linux-setup.sh"
  platform = "linux"
}
