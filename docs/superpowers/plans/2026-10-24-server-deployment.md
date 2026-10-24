# UniHub first-phase server deployment plan

> Execute with subagent-driven-development and an independent deployment review. The user explicitly authorized deployment to root@113.249.120.24 and confirmed https://unihub.quukk.com public HTTPS forwards to server 8484; HTTP forwards to server 80. The later user decision makes UniHub HTTPS-only; its HTTP vhost rejects requests.

**Goal:** Run the verified first-phase UniHub continuously on the authorized server, with HTTPS login, isolated persistence, recoverable releases and tested sandbox workflows.

**Architecture:** A dedicated /opt/unihub release and data tree, a restricted systemd Go service, and an isolated Docker Compose PostgreSQL/Formance/Casdoor stack. Existing nginx routes only this hostname to loopback services; existing applications and shared certificate files remain intact. Casdoor uses the same HTTPS issuer with its native /login, /api and discovery routes; console assets use an explicit namespace.

**Constraints:** Public URL https://unihub.quukk.com; TLS server8484 using the existing valid wildcard certificate; HTTP80 rejects only this hostname (nginx444), per the latest user instruction. Existing development-mode guards remain enforced: sandbox Go listener stays loopback, HTTPS cookies remain Secure, no real merchant or model provider. All fresh secrets generated server-side, 0600 root configuration; no copying local accounts or ledger data. No dependency ports published publicly. Signup disabled initially; one owner and one disposable acceptance identity with random private passwords. No remote force cleanup, unrelated service restart, credential logging, secret in CLI args, or direct model/finance internal API exposure. Preserve source and migration identities; no fake production completion.

## Task 1: Deployable first-phase bundle

- [x] Create deploy/server compose, idempotent bootstrap, systemd, nginx, backup/restore instructions and validation; pin existing verified dependency digests.
- [x] Generate separate PostgreSQL roles/databases, fresh Casdoor seed including branded built-in objects without replacing auth defaults, strict HTTPS callbacks, isolated server credentials.
- [x] Verify initialization preserves secrets, rejects occupied/unmanaged resources, and never prints credential values.
- [x] Build Linux amd64 Go binary and console assets; verify no credentials in bundle. Independent review of exact artifact diff.

## Task 2: Authorized remote deployment and acceptance

- [x] Inspect existing server resources/services/TLS/DNS; verify external ingress and preserve baseline probes.
- [x] Transfer only source/build/branding artifacts, initialize dedicated resources and start dependency health checks.
- [x] Install isolated nginx vhost and systemd service, validate configurations before reload, exercise restart and persistence.
- [x] Verify public TLS/HTTP rejection, discovery, real browser login, sandbox subscription/credit flow and team isolation; no real payments/models.
- [x] Create a restricted initial backup and verify recoverability on an isolated restore database or document any untested restoration boundary.
- [x] Record deployment revision, all results, operator credential file locations, rollback/upgrade steps and remaining scope. Commit deployment code/docs locally.
