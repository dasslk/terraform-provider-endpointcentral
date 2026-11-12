# A script kept in Git, added to the Script Repository. Changes to the file
# are uploaded as a new version of the same script.
resource "endpointcentral_script" "computer_name" {
  name               = "set-computer-name.sh"
  content            = file("${path.module}/scripts/set-computer-name.sh")
  description        = "Sets the computer name from a prefix and the serial number"
  success_exit_codes = [0]
}

resource "endpointcentral_script" "cleanup" {
  name     = "cleanup-tmp.sh"
  platform = "linux"
  content  = <<-EOT
    #!/bin/sh
    find /tmp -type f -mtime +7 -delete
  EOT
}
