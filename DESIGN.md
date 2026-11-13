# Design

How the provider maps Endpoint Central Cloud onto OpenTofu resources, and why.
The API reference is ManageEngine's OpenAPI 3.1 spec of the cloud API
(downloadable from its [API index](https://www.manageengine.com/products/desktop-central/help/api/cloud/api-index.html));
every limitation below comes from it, ManageEngine's published docs, or a live
tenant.

## Provider configuration

The provider's own settings follow the AWS provider rather than the Zoho
ecosystem's habits:

| Setting | AWS precedent |
| --- | --- |
| `region` names the tenant's deployment (`eu`, `us`, ...) | `region` |
| `ENDPOINTCENTRAL_*` variables, named after the provider; the configuration wins over the environment | `AWS_*` |
| `endpoints = { api, accounts }` and `ENDPOINTCENTRAL_ENDPOINT_URL_<NAME>` override hosts | `endpoints {}`, `AWS_ENDPOINT_URL_<SERVICE>` |
| `max_retries`, with exponential backoff that honours `Retry-After` | `max_retries` |
| Credentials are checked while the provider starts; `skip_credentials_validation` defers that | `skip_credentials_validation` |

## Entities and relationships

The provider follows the AWS provider's lessons on modelling. AWS started out
managing relationships inside entities (inline `ingress` rules on
`aws_security_group`, `managed_policy_arns` on `aws_iam_role`), found that this
fights with anything else touching the same object, and moved to one resource
per relationship (`aws_vpc_security_group_ingress_rule`,
`aws_iam_role_policy_attachment`), plus `*_exclusive` resources
(`aws_iam_role_policy_attachments_exclusive`) for when one configuration
must own the complete set.

| Concept | Kind | Resource | AWS analogue |
| --- | --- | --- | --- |
| Custom group | entity | `endpointcentral_custom_group` | `aws_iam_group` |
| Group ↔ computer, one member | relationship, non-authoritative | `endpointcentral_custom_group_membership` | `aws_iam_user_group_membership`, `aws_vpc_security_group_ingress_rule` |
| Group ↔ computers, all members | relationship, authoritative | `endpointcentral_custom_group_memberships_exclusive` | `aws_iam_role_policy_attachments_exclusive` |
| Dynamic group criteria | property of the group | inline `criteria` | inline because they are not relationships to other objects |
| APD task | entity | `endpointcentral_automated_patch_deployment` (console calls, every rule readable) | — |
| APD task ↔ target groups | relationship, written with the task | inline `target` | `aws_lb_listener.default_action` |
| Deployment policy | entity the API cannot update or delete | data source only | — |

Rules that follow from this:

- **An entity never manages its relationships.** Updating
  `endpointcentral_custom_group` re-sends a static group's *current* member
  list (the API requires it on every update), so a rename or a new description
  never adds or drops members.
- **Do not mix `membership` and `memberships_exclusive` on one group.** The
  exclusive resource would remove the other's members on every apply.
- **Destroying `*_exclusive` only stops managing**, as in the AWS provider;
  the members stay in the group.
- **Relationships stay inline only where the API leaves no choice.** An APD
  task's targets are part of the task: the API reads and writes them in the
  same body as the rules, with no call for one target.

## Mac custom scripts and the Script Repository

Endpoint Central's documented `deploy/template` call refuses custom group
targets, and an earlier version's template-based configuration resources were
removed for that reason. `endpointcentral_mac_custom_script` and
`endpointcentral_script` use the calls the console makes instead. Tested on a
live tenant (2026-11-12/13), the resources themselves included, against an
empty target group:

| Step | Call |
| --- | --- |
| List scripts | `GET /dcapi/configuration/scripts?platform=mac` (returns default arguments in clear text) |
| Read a script | `GET /dcapi/configuration/scriptRepository/{id}` (no content) |
| Upload a script | `POST /emsapi/files`, header `Module: Script_Repo`, returns a `fileID` that expires within hours |
| Add a script | `POST /dcapi/configuration/scriptRepository/script`, `application/scriptDetails.v1+json` |
| Change a script | **`POST`** `/dcapi/configuration/scriptRepository/{id}` with a new upload (`PUT` is 405) |
| Delete a script | `DELETE /dcapi/configuration/scriptRepository/{id}`; refused while in use, the answer says where |
| Create a configuration | `POST /dcapi/configuration/mac/customScript/deploy`, `application/deployMacCustomScript.v1+json` |
| Read a configuration | `GET /dcapi/configuration/{id}`; a deleted one answers `401` errorCode `1001` "Access denied" |
| Change a configuration | `POST /dcapi/configuration/{id}/deploy`, `application/deployConfig.v1+json`, with `collectionId` and the script's `detailId` |
| Delete a configuration | `POST /dcapi/configuration/moveToTrash`, then `DELETE /dcapi/configuration/trash/delete` |

**Scope.** Changing a configuration and every Script Repository write need
`DesktopCentralCloud.Common.CREATE`, which the published spec never mentions.
Without it they answer `401 INVALID_OAUTHSCOPE` before looking at the request,
even with every documented Configurations, SWDeployment, Tools and Admin scope;
creating a configuration works with the Configurations scopes alone. Found by
granting all 84 documented scopes plus `Common.CREATE`, then only the latter.

Consequences:

- **Changes are made in place**; a configuration keeps its ID.
- **A script's content cannot be read back**, so content edits made in the
  console are not detected; `content_sha256` shows content changes made here.
  Description, exit codes and default arguments are read back.
- **Script arguments are not secret in Endpoint Central.** Both resources mark
  them sensitive for plan output, but they sit in the state, and anyone who can
  open the configuration or list the Script Repository sees them. Prefer
  scripts that fetch secrets themselves.
- The client does not fetch a new token on a `401` that refuses the request
  (`INVALID_OAUTHSCOPE`, `1001`): a new token changes nothing there, and Zoho
  rate-limits new tokens.

## Mac custom profiles: the MDM API

Endpoint Central's console creates Mac custom profile *configurations* with
calls that only accept a browser session: on a live tenant the update call
(`/dcapi/configuration/{id}/deploy`) answers `401 INVALID_OAUTHSCOPE` to an
OAuth token holding every relevant scope, while documented calls accept the
same token. Behind each such configuration Endpoint Central keeps an MDM
profile. So Mac profiles are managed through ManageEngine's documented MDM
API (`mdm.manageengine.<region>`, `MDMOnDemand.MDMDeviceMgmt.*` scopes), with
MDM groups as targets:

| Concept | Resource | AWS analogue |
| --- | --- | --- |
| Profile (`.mobileconfig`) | `endpointcentral_mac_custom_profile` | `aws_iam_policy` |
| MDM device group | `endpointcentral_mdm_group` | `aws_iam_group` |
| Group ↔ device | `endpointcentral_mdm_group_membership` | `aws_iam_user_group_membership` |
| Group ↔ profile | `endpointcentral_mdm_group_profile_assignment` | `aws_iam_group_policy_attachment` |

Calls, verified end to end on a live tenant (2026-11-11) with a test profile
and an empty group:

| Step | Call | Notes |
| --- | --- | --- |
| Upload | `POST /emsapi/files` on the MDM host, `Module: MDM_CUSTOM_PROFILE`, multipart `file` | Answers 406 to `Accept: application/json`; the Endpoint Central host's `/emsapi/files` refuses OAuth |
| Create | `POST /api/v1/mdm/profiles`, `platform_type: 6` | macOS is 6 (the docs list 1-4); names are unique (409) |
| Content | `POST …/payloads/maccustomprofiles`, later `PUT …/payloaditems/{id}`, `custom_profile_file_id` plus `allow_existing_payload: true` | The item gets a new ID on every edit, so it is looked up each time. Without `allow_existing_payload` (which the console sends) the API refuses payload types the MDM has its own forms for (firewall, login window, screen saver, restrictions, VPN, ...) with `PAY0011` |
| Publish | `POST /api/v1/mdm/profiles/{id}/publish` | Version + 1 |
| Push a new version | `PUT /api/v1/mdm/profiles/{id}/update_all` | Only when the profile is assigned somewhere; not yet seen on a device |
| Rename | `PUT /api/v1/mdm/profiles/{id}` | |
| Delete | `DELETE /api/v1/mdm/profiles/trash`, then `DELETE /api/v1/mdm/profiles` | The docs' "first DELETE trashes" is wrong (412); the trash call came from the console |
| Read | `GET /api/v1/mdm/profiles/{id}` | A trashed or deleted profile answers 500; the list then decides |
| Group | `POST`/`GET`/`DELETE /api/v1/mdm/groups[/{id}]`, `group_type: 6` | No rename call: name changes replace the group |
| Members | `GET /groups/{id}/members`, `POST`/`DELETE /groups/{id}/members/{device}` | One member at a time: no read-modify-write needed |
| Assignment | `POST`/`DELETE /groups/{id}/profiles` with `profile_ids`; `GET /groups/{id}/profiles` | The GET is undocumented |

Consequences:

- **Everything about a profile changes in place**: content becomes the next
  published version of the same profile, so Macs get it replaced, not
  removed and added. Uploads expire within hours, so each change uploads again.
- **`content` is sensitive** (passwords, tokens), and `content_sha256` makes
  content changes visible in a plan. The API cannot return the content, so
  console edits to it are not detected; renames are.
- **MDM groups are not Endpoint Central custom groups.** Profiles target MDM
  groups; console-managed configurations and APD tasks target custom groups.
- **MDM groups are static.** The MDM API documents only device groups
  (`group_type` 6) with explicit members, and Macs cannot join dynamic MDM
  groups. The provider manages groups and members; rules that compute members
  belong in the configuration that uses it, not in the provider. An
  enrollment invitation assigns exactly one group, which suits an "every Mac"
  group.

## Found on a live tenant (2026-11-11)

Probing a real EU tenant showed where the published spec and reality differ:

- **Deploying a template to a custom group is refused.** `POST
  /dcapi/configuration/deploy/template` rejects `targets.type` =
  `custom-group` (the documented value) and every variant tried as "an invalid
  parameter format" (IAM0025); `computer` passes that check. Deploying a Mac
  custom profile template to a computer failed with "Problem in populating
  template details" (not pursued further: a success would reach a real Mac).
  This is why the provider does not manage configurations.
- **Endpoint Central keeps an MDM profile behind each Mac custom profile.**
  The MDM API (`mdm.manageengine.eu/api/v1/mdm/profiles`, which accepts the
  same OAuth token with `MDMOnDemand.*` scopes) lists it as
  `<name>_<timestamp>`, payload `maccustomprofiles`, platform type 1, with no
  group or device associations: Endpoint Central targets and pushes it with
  its own groups. Profiles the provider creates through the MDM API are
  separate from those and are targeted with MDM groups.
- **OAuth on the upload and deploy calls.** `/emsapi/files` and
  `/dcapi/configuration/{id}/deploy` answer `401 INVALID_OAUTHSCOPE` to a token
  with only read scopes for MDM: they take OAuth and want write scopes. The
  console's `/mdmemsapi/files` and the MDM host's `/emsapi/files` answer an
  empty `401`.

## Automated patch deployment

Captured from the console and verified end to end on a live tenant
(2026-11-11) with a throwaway task targeting an empty group:

| Step | Call | Notes |
| --- | --- | --- |
| Create | `POST /dcapi/patch/autopatchdeployment`, `application/addTask.v1+json` | Answers with the collection ID only; the task ID comes from the task report |
| Read | `GET /dcapi/patch/autopatchdeployment/{task_id}`, `Accept: */*` | Every rule; an unknown ID is `500 "Invalid TaskID"`. `application/json` is refused (406) on all patch endpoints |
| Update | `PUT /dcapi/patch/autopatchdeployment/{task_id}`, `application/addModifyTask.v1+json` | Fields are validated before scopes; read-only fields (`isTargetEditable`, ...) are rejected; needs `PatchMgmt.UPDATE` |
| Suspend, resume, delete | `…/suspend` (POST), `…/resume` (POST), `…/delete` (DELETE), media types `suspend`/`resume`/`deletetask.v1+json` | Body is a JSON array of task IDs; answer `{"<id>": true}` |

- **Every rule is readable**, so console edits show up as drift, and an
  import of an existing task plans no changes when the configuration
  matches. Both tasks of the live tenant imported with an empty plan.
- **Targets need labels.** Without a `label` next to each `idValue` the
  create fails with an internal error. Custom group names are looked up; the
  workgroup's name is an argument, because reading it needs `SOM.READ`.
- **Updates start from the task's current settings**, so what the resource
  does not model (notifications, expiry, application lists, delays) is kept.
  Only toggling `enabled` sends no update (the read reports `platformId` as
  a number, the console writes a string; they are compared alike).
- Severity codes 0-4 are mapped to `unrated`, `low`, `moderate`,
  `important`, `critical`, ManageEngine's usual scale (not yet confirmed
  against the console's labels).

## Read-modify-write and concurrency

The group API only replaces a whole member list (`updateCg` with
`resourceIds`). Every membership change therefore reads the list, edits it and
writes it back, under a lock per group inside the provider process
(`client.lockGroup`). `TestModifyCustomGroupMembers_concurrentAddsAreNotLost`
shows 8 parallel adds all landing, and losing 7 of them without the lock.
The lock does not reach across processes: two `tofu apply` runs at the same
time can still overwrite each other, which is one reason to keep one admin
applying at a time.

## What the API cannot do

| Wanted | Status | Why / workaround |
| --- | --- | --- |
| Read a script's content | **not possible** | The Script Repository returns everything but the content; `endpointcentral_script` keeps a `content_sha256`. |
| Create a Mac/Linux configuration from its settings (custom profile, custom script, FileVault, PPPC, ...) | **not in the published API** | Mac custom scripts through the console's calls: `endpointcentral_mac_custom_script`. Mac custom profiles (`.mobileconfig`, which also covers PPPC, login items and more) through the MDM API: `endpointcentral_mac_custom_profile`. Other types stay in the console. |
| Deploy a configuration template to a custom group | **refused** | `deploy/template` rejects `custom-group` targets on a live tenant (IAM0025). |
| MDM enrollment of iOS devices | **not possible** | No enrollment API in Endpoint Central Cloud. ManageEngine's MDM API (`mdm.manageengine.com`, `MDMOnDemand.*` scopes) belongs to MDM Plus and has no enrollment-request call either. |
| MDM enrollment of Macs | **action only** | `POST /dcapi/som/computers/actionRequest/mdm-profile/install` pushes the MDM profile to Macs that already run the agent. It is a one-shot action with no read-back of a single computer's state and no undo, so it is not a resource yet. |
| Patch deployment policies | **read only** | Create and list exist; update and delete do not. |
| APD through the documented API | **not on Cloud** | 1.4 `createAPDTask` refuses OAuth, `modifyAPDTask` redirects to `URL_RULE_NOT_CONFIGURED`. The console's `/dcapi/patch/autopatchdeployment` calls work instead (see below). |
| Custom group descriptions | **write only** | `getCGDetails` does not return the description. |

## Identity

| Resource | ID | Why |
| --- | --- | --- |
| custom group | group ID | Lookups by ID go through the group list, so a rename in the console is followed. |
| membership | `<group_id>/<resource_id>` | Composite, like `aws_iam_role_policy_attachment`. |
| APD task | task ID | Found after create through the task list by collection ID; renames change in place. Import takes the ID or the name. |
| Mac custom profile, MDM group | MDM ID | Returned by the create calls. |
| Mac custom script | collection ID | Returned by the create call. |
| Script | script ID | Returned by the add call. |
| MDM membership, profile assignment | `<group_id>/<device_id>`, `<group_id>/<profile_id>` | Composite, like `aws_iam_role_policy_attachment`. |

## Not verified against a live tenant yet

These are covered by tests against an in-memory fake only:

- `update_all` pushing a new profile version to an assigned Mac.
- Whether `updateCg` keeps a description that is left out of the request.
- Mac custom scripts: execution frequencies other than `once` and
  `every_startup`; several success exit codes (sent comma-separated);
  exclude groups.
- Scripts: renaming through the change call (the resource replaces the
  script instead); `shared`; Linux and Windows scripts.
