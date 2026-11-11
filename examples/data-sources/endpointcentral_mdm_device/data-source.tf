data "endpointcentral_mdm_device" "by_name" {
  name = "macos-vm-02"
}

data "endpointcentral_mdm_device" "by_serial" {
  serial_number = "VMSERIAL002"
}
