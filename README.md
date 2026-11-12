# OpenTofu / Terraform provider for ManageEngine Endpoint Central Cloud

Manage [Endpoint Central Cloud](https://www.manageengine.com/products/desktop-central/) through its
[REST API](https://www.manageengine.com/products/desktop-central/help/api/cloud/api-index.html).

> Community provider, not affiliated with or endorsed by ManageEngine or Zoho.

**Status: early (v0.x).** Every resource has tests against an in-memory fake of the API, and every resource
family has been run against a real EU tenant. Expect breaking changes before 1.0; see
[CHANGELOG.md](CHANGELOG.md).

| Resource | Description |
|---|---|
| `endpointcentral_custom_group` | Machine (computer) or user custom groups: static, static unique and dynamic |
| `endpointcentral_custom_group_membership` | One member of a static group (non-authoritative) |
| `endpointcentral_custom_group_memberships_exclusive` | The complete member list of a static group (authoritative) |
| `endpointcentral_mac_custom_profile` | A `.mobileconfig` from Git, as an MDM profile (MDM API) |
| `endpointcentral_mdm_group` | An MDM device group, the target of Mac profiles |
| `endpointcentral_mdm_group_membership` | One device in an MDM group |
| `endpointcentral_mdm_group_profile_assignment` | A profile assigned to an MDM group |
| `endpointcentral_automated_patch_deployment` | An Automated Patch Deployment (APD) task |
| `endpointcentral_mac_custom_script` | A Mac custom script configuration: a Script Repository script run on custom groups |

| Data source | Description |
|---|---|
| `endpointcentral_custom_group` | A group by name, including built-in ones such as `All Mac machines` |
| `endpointcentral_computer` | A computer by name, FQDN or ID (for its resource ID) |
| `endpointcentral_patch_deployment_policy` | A patch deployment policy by name |
| `endpointcentral_script` | A script in the Script Repository by file name |
| `endpointcentral_mdm_group` | An MDM group by name |
| `endpointcentral_mdm_device` | An enrolled MDM device by name or serial number |

Resources model entities and their relationships separately, the way the AWS provider does
(`aws_iam_role` + `aws_iam_role_policy_attachment`). [DESIGN.md](DESIGN.md) explains the model and lists
what Endpoint Central's API cannot do. In short: scripts cannot be **uploaded** to the Script Repository
(add them in the console, then reference them by name), configurations cannot be **changed** through the
API (so every change to `endpointcentral_mac_custom_script` replaces it), and there is no API for iOS
enrollment. Mac profiles go through MDM (`endpointcentral_mac_custom_profile`).

The provider is built from ManageEngine's OpenAPI 3.1 spec of the cloud API, downloadable from the
[API index](https://www.manageengine.com/products/desktop-central/help/api/cloud/api-index.html).

## Installation

```hcl
terraform {
  required_providers {
    endpointcentral = {
      source  = "dasslk/endpointcentral"
      version = "~> 0.1"
    }
  }
}
```

## Authentication (Zoho OAuth 2.0)

Endpoint Central Cloud uses [Zoho OAuth](https://www.zoho.com/accounts/protocol/oauth.html). The provider
holds a long-lived **refresh token** and exchanges it for one-hour access tokens as needed.

1. Open the Zoho API console **for your data center**, e.g. <https://api-console.zoho.com> (US),
   `api-console.zoho.eu`, `api-console.zoho.in`, … — see the
   [data center table](https://www.manageengine.com/products/desktop-central/oauth-authentication-endpoint-domain.html).
2. Add a client of type **Self Client** and note the *Client ID* and *Client Secret*.
3. In the Self Client's **Generate Code** tab, enter the scopes and generate a grant code. For everything the
   provider does:

   ```
   DesktopCentralCloud.Common.READ,DesktopCentralCloud.Common.UPDATE,DesktopCentralCloud.Configurations.READ,DesktopCentralCloud.Configurations.CREATE,DesktopCentralCloud.Configurations.UPDATE,DesktopCentralCloud.Configurations.DELETE,DesktopCentralCloud.PatchMgmt.READ,DesktopCentralCloud.PatchMgmt.CREATE,DesktopCentralCloud.PatchMgmt.UPDATE,DesktopCentralCloud.PatchMgmt.DELETE,MDMOnDemand.MDMDeviceMgmt.READ,MDMOnDemand.MDMDeviceMgmt.CREATE,MDMOnDemand.MDMDeviceMgmt.UPDATE,MDMOnDemand.MDMDeviceMgmt.DELETE,MDMOnDemand.MDMInventory.READ
   ```

   Custom groups alone need only `DesktopCentralCloud.Common.READ,DesktopCentralCloud.Common.UPDATE`; the
   `MDMOnDemand` scopes are for the Mac profile and MDM resources (the MDM API at
   `mdm.manageengine.<region>`). See [Scopes per resource](#scopes-per-resource).

4. Within the code's validity window, exchange it for a refresh token (use your data center's accounts host):

   ```sh
   curl -X POST https://accounts.zoho.com/oauth/v2/token \
     -d grant_type=authorization_code \
     -d client_id="$ENDPOINTCENTRAL_CLIENT_ID" \
     -d client_secret="$ENDPOINTCENTRAL_CLIENT_SECRET" \
     -d code="<grant code>"
   ```

   Keep the `refresh_token` from the response.

5. Configure the provider, preferably through environment variables:

   ```sh
   export ENDPOINTCENTRAL_REGION=eu            # us, eu, in, au, cn, jp, ca, uk, sa
   export ENDPOINTCENTRAL_CLIENT_ID=1000.XXXX
   export ENDPOINTCENTRAL_CLIENT_SECRET=xxxx
   export ENDPOINTCENTRAL_REFRESH_TOKEN=1000.xxxx.xxxx
   ```

**Zoho rate-limits access tokens** per refresh token (roughly ten in ten minutes). Every `tofu plan` or
`apply` asks for one, so many runs in a row can be refused with "too many requests"; wait a few minutes.

The provider follows the AWS provider's conventions for its own settings: every argument has an
`ENDPOINTCENTRAL_*` environment variable, an argument in the configuration wins over the variable, and
credentials are checked when the provider starts.

| Argument | Environment variable | Description |
|---|---|---|
| `region` | `ENDPOINTCENTRAL_REGION` | Region of the tenant, the ending of the console's domain. Default `us`. |
| `client_id`, `client_secret`, `refresh_token` | `ENDPOINTCENTRAL_CLIENT_ID`, `..._CLIENT_SECRET`, `..._REFRESH_TOKEN` | Self Client credentials. |
| `access_token` | `ENDPOINTCENTRAL_ACCESS_TOKEN` | Use a pre-obtained access token instead (expires after 1h). |
| `max_retries` | `ENDPOINTCENTRAL_MAX_RETRIES` | Retries after throttling, server errors and network failures. Default 4, `0` disables. |
| `skip_credentials_validation` | | Do not obtain a token while the provider starts. |
| `endpoints = { api, accounts }` | `ENDPOINTCENTRAL_ENDPOINT_URL_API`, `..._ENDPOINT_URL_ACCOUNTS` | Override the hosts the region selects. |

## Scopes per resource

What each resource needs, from ManageEngine's documentation and live tests (2026-11-11). The 15 scopes in
the list above cover everything; grant less to limit what a token can do.

| Resource / data source | Scopes |
|---|---|
| `custom_group`, `custom_group_membership(s_exclusive)`, data `custom_group`, data `computer` | `DesktopCentralCloud.Common.READ`, `.Common.UPDATE` |
| data `patch_deployment_policy` | `DesktopCentralCloud.PatchMgmt.READ` |
| `automated_patch_deployment` | `PatchMgmt.READ`, `.CREATE`, `.UPDATE`, `.DELETE`, `Common.READ` |
| `mac_custom_profile`, `mdm_group`, `mdm_group_membership`, `mdm_group_profile_assignment`, data `mdm_group` | `MDMOnDemand.MDMDeviceMgmt.READ`, `.CREATE`, `.UPDATE`, `.DELETE` |
| data `mdm_device` | `MDMOnDemand.MDMInventory.READ` |
| `mac_custom_script`, data `script` | `DesktopCentralCloud.Configurations.READ`, `.CREATE`, `.UPDATE`, `.DELETE`, `Common.READ` (tested with all four; the minimum is not narrowed down yet) |

## Example

A Mac canary group with one member, a custom profile from Git, and patching.

```hcl
terraform {
  required_providers {
    endpointcentral = { source = "dasslk/endpointcentral" }
  }
}

provider "endpointcentral" {}

# Endpoint Central (agent) side: a group for patching.
resource "endpointcentral_custom_group" "pilot" {
  name = "pilot-macs"
}

data "endpointcentral_computer" "test_mac" {
  name = "mac-test-01"
}

resource "endpointcentral_custom_group_membership" "test_mac" {
  group_id    = endpointcentral_custom_group.pilot.id
  resource_id = data.endpointcentral_computer.test_mac.id
}

data "endpointcentral_patch_deployment_policy" "notify" {
  name = "Mac - Notify end users to close running applications"
}

resource "endpointcentral_automated_patch_deployment" "pilot" {
  name                 = "pilot-macs"
  platform             = "mac"
  deployment_policy_id = data.endpointcentral_patch_deployment_policy.notify.id
  os_updates = {
    security = ["unrated", "low", "moderate", "important", "critical"]
  }
  third_party_severities = ["moderate", "important", "critical"]
  target = {
    workgroup_id      = "123456789"
    workgroup_name    = "WORKGROUP"
    include_group_ids = [endpointcentral_custom_group.pilot.id]
  }
}

# MDM side: a profile assigned to a device group.
resource "endpointcentral_mac_custom_profile" "screensaver" {
  name    = "macos-screensaver"
  content = file("${path.module}/profiles/screensaver.mobileconfig")
}

resource "endpointcentral_mdm_group" "pilot" {
  name = "pilot-macs"
}

data "endpointcentral_mdm_device" "test_mac" {
  serial_number = "C02XXXXXXXXX"
}

resource "endpointcentral_mdm_group_membership" "test_mac" {
  group_id  = endpointcentral_mdm_group.pilot.id
  device_id = data.endpointcentral_mdm_device.test_mac.id
}

resource "endpointcentral_mdm_group_profile_assignment" "screensaver" {
  group_id   = endpointcentral_mdm_group.pilot.id
  profile_id = endpointcentral_mac_custom_profile.screensaver.id
}
```

See [`docs/`](docs/) for the full reference and [DESIGN.md](DESIGN.md) for the limitations of each resource.

## Development

Requires Go, OpenTofu and golangci-lint.

```sh
make build   # build the provider binary
make lint    # golangci-lint
make test    # unit tests + provider tests (OpenTofu against an in-memory fake API)
make docs    # regenerate docs/ from the schema and examples/
```

To try a local build with `tofu`, point a dev override at your `GOBIN`:

```hcl
# ~/.tofurc
provider_installation {
  dev_overrides {
    "dasslk/endpointcentral" = "/home/you/go/bin"
  }
  direct {}
}
```

then `make install` and run `tofu plan` (skip `tofu init` for dev overrides).

## Licence

[MPL-2.0](LICENSE), like most OpenTofu and Terraform providers.
