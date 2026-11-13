# Changelog

All notable changes to this provider are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.3.0] - 2026-11-13

### Added

- `endpointcentral_script`: a script in the Script Repository, with its
  content, description, default arguments and exit codes, changed in place.

### Changed

- `endpointcentral_mac_custom_script` changes configurations in place instead
  of replacing them.
- Both need the scope `DesktopCentralCloud.Common.CREATE`, which ManageEngine's
  documentation does not mention; without it Endpoint Central answers
  `INVALID_OAUTHSCOPE`. Add it to your Self Client's grant.
- When Zoho rate-limits access tokens, the provider says so instead of
  reporting invalid credentials.

## [0.2.0] - 2026-11-13

### Added

- `endpointcentral_mac_custom_script`: a Mac custom script configuration
  running a Script Repository script on custom groups. Endpoint Central
  refuses API tokens on its modify call, so every change replaces the
  configuration.
- Data source `endpointcentral_script`: a Script Repository script by file
  name. Scripts cannot be uploaded through the API.

### Changed

- The client no longer requests a new access token when a call is refused for
  a missing scope or "Access denied"; that only used up Zoho's token rate
  limit. Error messages from `/dcapi` calls now include their `errorMsg`.

## [0.1.1] - 2026-11-12

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

[Unreleased]: https://github.com/dasslk/terraform-provider-endpointcentral/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/dasslk/terraform-provider-endpointcentral/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/dasslk/terraform-provider-endpointcentral/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/dasslk/terraform-provider-endpointcentral/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/dasslk/terraform-provider-endpointcentral/releases/tag/v0.1.0
