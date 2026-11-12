# Changelog

All notable changes to this provider are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- `endpointcentral_mac_custom_profile` accepts profiles with payload types
  the MDM also has its own forms for (firewall, login window, screen saver,
  restrictions, passcode, VPN, system extensions, ...), as the console does.
  They failed with `PAY0011: Payloads supported by MDM cannot be added`.

## [0.1.0] - 2026-11-11

First release.

### Added

- Resources: `endpointcentral_custom_group`,
  `endpointcentral_custom_group_membership`,
  `endpointcentral_custom_group_memberships_exclusive`,
  `endpointcentral_automated_patch_deployment`,
  `endpointcentral_mac_custom_profile`, `endpointcentral_mdm_group`,
  `endpointcentral_mdm_group_membership`,
  `endpointcentral_mdm_group_profile_assignment`.
- Data sources: `endpointcentral_custom_group`, `endpointcentral_computer`,
  `endpointcentral_patch_deployment_policy`, `endpointcentral_mdm_group`,
  `endpointcentral_mdm_device`.
- Zoho OAuth (Self Client refresh token), all Endpoint Central Cloud regions,
  `ENDPOINTCENTRAL_*` environment variables.

[Unreleased]: https://github.com/dasslk/terraform-provider-endpointcentral/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/dasslk/terraform-provider-endpointcentral/releases/tag/v0.1.0
