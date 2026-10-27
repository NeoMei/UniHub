# Product user access and administration

`PRODUCT_USER_MANIFESTS_FILE` loads a strict bounded JSON array of product identity
manifests (see `config/examples/product-user-manifests.json`), separately from the
commercial product registry. Products with only `access:read` and identity/account
scopes do not create billing products. Each product client in `PRODUCT_CLIENTS_FILE`
uses `instance_id` (`default` when omitted), and must explicitly grant `access:read`
and refer to a configured product instance. Clients sharing that pair resolve the
same single manifest. `PUBLIC_URL` is the canonical issuer returned in access.
The role/permission list is trusted deployment configuration. Only nonprivileged
roles can be defaults; privileged roles require explicit assignment. `platform:*`
permissions are forbidden. Bump manifest version whenever roles/defaults change.

GET `/v1/me/product-access` uses the verified product client's product+instance;
query attempts to select another product/instance are forbidden. Console callers
supply `?product_id=notes&instance=default`. The response is:

```json
{"issuer":"https://unihub.example","subject":"central-uuid","product_id":"notes","instance_id":"default","user":{"id":"central-uuid","name":"Alice","email":"alice@example.test"},"roles":["member"],"permissions":["product:use"],"version":1,"active":true,"expires_at":1793100000}
```

Expiry comes from verified bearer claims (or the verified browser session expiry),
never a request field. Resolve checks global user status and product membership
online on every request. Explicit empty roles remain empty across default changes;
inactive membership denies access. Role/permission arrays are sorted and deduplicated.
`version` is a current effective revision (manifest version + assignment revision),
not an assignment precondition; no version-based allow cache is permitted.

GET `/v1/me/applications` returns available access responses plus `name`,`entry_url`.
Only safe HTTPS (or local development loopback HTTP) launch URLs are accepted.
Product callers see only their configured product instance. Console callers see all
available applications. Empty-permission or inactive assignments are not launched.
GET `/v1/capabilities` independently exposes verified `kind`,`product_id`,`client_id`,
`instance_id`,`scopes`; the kit requires exact product/client/instance agreement.

User administrator bootstrap uses only immutable central UUIDs in `USER_ADMIN_IDS`,
separate from `BILLING_OPERATOR_IDS`. Sign in using the ordinary console identity
path and read GET `/v1/me` to obtain the UUID; put that UUID in deployment config,
restart and confirm GET `/v1/user-management` returns `enabled:true,can_manage:true`.
No public registration, email, product role, operator audience or browser field
creates a user administrator. Keep at least one configured UUID available; the
server serializes status changes and refuses to disable the last available admin.

Console APIs (product/legacy product credentials are forbidden for all of these):

- GET `/v1/user-management`: `{enabled:boolean,can_manage:boolean}`. Any active
  console user can inspect their own gate; only configured administrators use the
  remaining APIs.
- GET `/v1/user-management/users?q=Alice&limit=50`: managed users `{id,name,email,
  active,version}`. Search max 100 characters, limit 1–100, default 50.
- GET `/v1/user-management/products`: normalized manifests.
- GET `/v1/user-management/users/{user}/access`: `{product_id,instance_id,roles,
  active,assignment_version,manifest_version,explicit}[]`; unassigned users show
  defaults, `assignment_version:0`, `explicit:false`.
- PUT `/v1/user-management/users/{user}/access/{product}?instance=default`:
  `{roles:[],active:true,version:0}`. `version` is **assignment_version** from
  readback. First explicit assignment expects 0. Missing/null fields reject;
  unknown role/product and stale versions reject. Response is the assignment.
- PUT `/v1/user-management/users/{user}/status`:
  `{active:false,version:1}`. `version` is the user's global status version,
  initially 1. Response is the managed user with incremented version.

All mutations and actor/target/product/instance/version snapshots audit atomically.
Reads and writes require active trusted console identity; browser writes use the
existing session CSRF+Origin gate. Conflict is HTTP 409, invalid input 400,
permission/global disable 403, unknown product/user 404. UI refreshes after save;
conflicts require readback rather than automatic resubmission. The SDK methods
mirror these routes, but server credentials must never be embedded in browser apps.

Global disable blocks old user bearers on authenticated business APIs and blocks
model credential issuance, ordinary verification, new holds and top-ups. Historical
model receipt proof and settlement/recovery of already dispatched commands remain
available to the separately authenticated service, so reconciliation is not stranded.
`/auth/session` and logout can remain available for ending a disabled session.
