# hello on kw with Kuvryn Sync

`main:deploy/kuvryn-sync/kw/` is the authoritative kw deployment
configuration for the `hello` namespace. Sync manages deployments,
configuration, application credentials and runtime permissions. The
docker-compose lab (`deploy/docker-compose/compose.yaml`) stays the
development twin; this directory is its kw translation. Older manifests are
not the kw release source.

| Application / namespace | Desired directory |
| ----------------------- | ----------------- |
| hello                   | `kw`              |

The pattern (Application with automatic reconciliation, self-healing,
`conflictPolicy: fail`, rollback on failed rollout) is dhole's; see
`azrtydxb/dhole` `deploy/kuvryn-sync/README.md` for the platform-level rules
that apply here too.

## Release a change

1. Merge to `main`. `.github/workflows/ci.yaml` builds and pushes
   `192.168.10.131:5000/azrtydxb/hello-control`, `hello-sip` and `hello-ui`
   with the immutable tag `sha-<full sha>` (plus `main`; the registry keeps
   no short tags). The job proves the `:443` pull name resolves before it
   succeeds.
2. Update the intended container references in `kw/resources.yaml` to that
   `sha-` tag (later: an immutable `repository@sha256:...` digest). Merging
   application code alone does not promote an image.
3. Edit configuration in `kw/`. For credentials, the `secret-*.sops.yaml`
   files encrypt to the hello namespace age recipient via the repo-root
   `.sops.yaml` creation rule; edit them like this (never commit a decrypted
   file):

   ```sh
   export SOPS_AGE_KEY_FILE="$HOME/.config/kuvryn-sync/age/kw-hello.agekey"
   sops deploy/kuvryn-sync/kw/secret-hello-store.sops.yaml
   ```

   Each skeleton documents its keys and how to generate the values.

4. Render privately and validate against the API with the Sync field
   manager (render-private.py is dhole's, verbatim):

   ```sh
   export SOPS_AGE_KEY_FILE="$HOME/.config/kuvryn-sync/age/kw-hello.agekey"
   python3 deploy/kuvryn-sync/render-private.py kw /tmp/hello-desired.yaml
   kubectl --context kw apply --server-side --dry-run=server \
     --field-manager=kuvryn-sync -f /tmp/hello-desired.yaml
   ```

   Review the diff privately; rendered credentials must not reach logs or
   Git. Delete the private render when finished.

5. Commit and push to `main`; Sync polls within 60 seconds. Verify the
   Application is `Healthy`/`Synced` and the workloads are ready.

Do not run Helm upgrades, raw `kubectl apply` or `kubectl set image`
alongside Sync. Revert the deployment commit to roll back. A field conflict
requires resolving the competing writer; do not enable blanket force
adoption.

## One-time kw facts (confirmed 2026-10-04)

Confirmed against the cluster during the first-sync bootstrap:

- `HELLO_SIP_TRUSTED_PROXIES` (hello-sip-1/2): kw's pod CIDR is
  `10.42.0.0/16` (the nodes' `spec.podCIDR` values).
- `HELLO_DNS_RESOLVER` (hello-ui): kube-dns is `10.43.0.10`. nginx's
  resolver applies no search domains, so `HELLO_CONTROL_UPSTREAM` is the
  Service FQDN `hello-control.hello.svc.cluster.local` (guarded by
  `test/deploy/ui_upstream_test.go`); the UI container exits at startup if it
  does not resolve.
- `KAMAILIO_PUBLIC_HOST` (kamailio): `192.168.10.101` (node master-11;
  NodePort 30508/udp is reachable on every node address).
- Images pull from `192.168.10.131:5000/...` with no imagePullSecret: the
  nodes trust Nexus directly (containerd `certs.d`), verified with a
  disposable pod in this namespace before the first sync.

## Secrets

| File                                  | Secret            | Keys                          | Consumed by                                                       |
| ------------------------------------- | ----------------- | ----------------------------- | ----------------------------------------------------------------- |
| `kw/secret-hello-store.sops.yaml`     | `hello-store`     | `username`, `password`, `dsn` | postgres (`username`/`password`), hello-control/hello-sip (`dsn`) |
| `kw/secret-hello-app.sops.yaml`       | `hello-app`       | `secretKey`                   | hello-control/hello-sip (`HELLO_SECRET_KEY`)                      |
| `kw/secret-hello-nonce.sops.yaml`     | `hello-nonce`     | `nonceSecret`                 | hello-sip-1/2 (`HELLO_SIP_NONCE_SECRET`)                          |
| `kw/secret-hello-bootstrap.sops.yaml` | `hello-bootstrap` | `password`                    | hello-control (`HELLO_BOOTSTRAP_ADMIN_PASSWORD`)                  |

Optional, not yet supplied: `kw/secret-hello-prov-redirect.sops.yaml`
(`hello-prov-redirect`), the phone vendors' redirect-service credentials.
hello-control maps each key to its variable, every one optional:
`snomSrapsAccessKeyId`/`snomSrapsAccessKeySecret` to
`HELLO_PROV_SNOM_KEY_ID`/`_KEY_SECRET`; `yealinkRpsAccessKey`/`yealinkRpsAccessSecret`
to `HELLO_PROV_YEALINK_KEY`/`_SECRET`; `yealinkYmcsClientId`/`yealinkYmcsClientSecret`/`yealinkYmcsRegion`
to `HELLO_PROV_YMCS_CLIENT_ID`/`_CLIENT_SECRET`/`_REGION`; and `gdmsClientId`,
`gdmsClientSecret`, `gdmsUsername`, `gdmsPassword`, `gdmsRegion`, `gdmsSiteId`
to `HELLO_PROV_GDMS_*`. A vendor's group is all set or all absent. The
`liveTest*` keys (`liveTestSnomMac`, `liveTestYealinkMac`,
`liveTestYealinkSerial`, `liveTestGdmsMac`, `liveTestGdmsSerial`) name the
real phones `TestRedirectLive` uses. Its comment-only skeleton is
`skeletons/secret-hello-prov-redirect.sops.yaml`, kept outside `kw/`
because Sync decrypts every `kw/*.sops.yaml` and a comment-only file there
would stop the whole sync; copy it into `kw/` and encrypt it when the
credentials exist. Without it the redirect services show as unconfigured
and phones are added by DHCP or a typed URL (docs/provisioning.md).

Voicemail-to-email is off on kw: hello-control gets no `SMTP_*`
environment, so its mailer stays disabled (no send attempts, no failure
metrics) and the console shows email as not configured. To turn it on, add
a `hello-smtp` secret and `SMTP_HOST`/`SMTP_PORT`/`SMTP_USER`/`SMTP_PASS`/
`SMTP_FROM` to hello-control; messages left pending meanwhile are then sent.

Until encrypted, each file is a comment-only skeleton; `render-private.py`
fails on it (SOPS decryption failed) rather than rendering plaintext. Never
commit a decrypted file or put credentials in shell arguments.

## Bootstrap (one-time, admin)

Nothing here creates the namespace or the Sync objects; they bootstrap
reconciliation and live outside the desired directory, exactly as for dhole.
Pointers, with dhole's files as the templates (`azrtydxb/dhole`
`deploy/kuvryn-sync/`):

1. **Namespace** `hello`: create it as an admin on kw (dhole's namespace was
   bootstrapped the same way; it is not in Git).
2. **Deployer RBAC**: apply `deploy/kuvryn-sync/rbac.yaml` from dhole with
   the namespace and subjects changed to `hello` (ServiceAccount
   `kuvryn-sync-deployer`, Role, RoleBinding). Hello needs no named
   cluster-role adoption and no sandbox roles: configmap, secret, service,
   deployment and ingress coverage, plus `cert-manager.io` `certificates`
   for the provisioning host's `hello-prov-tls` (add that rule to an
   existing Role before the first sync that carries it).
3. **Repository + Application**: copy dhole's
   `deploy/kuvryn-sync/repository.yaml` and `application.yaml`, with name
   and namespace `hello`, `url: https://github.com/azrtydxb/hello.git`,
   `path: deploy/kuvryn-sync/kw`. Hello's repository is public, so no Sync
   Git credential is needed.
4. **Age key**: generate and install the namespace identity,
   `~/.config/kuvryn-sync/age/kw-hello.agekey` (mode 0600) on the operator
   machine, mirroring `kw-dhole.agekey`; put the private key in the
   `kuvryn-sync-sops` Secret in the `hello` namespace with the label
   `sync.kuvryn.io/decryption-key: "true"`. Its public recipient is what
   `sops edit --age` encrypts to. Back it up: Git alone cannot recover the
   credentials.
5. **Secrets**: create the four `secret-*.sops.yaml` files per above, then
   render and server-side-dry-run before the first real sync.

Datastores (`hello-postgres`, `valkey-1/2`, `sentinel-1/2/3`) and their
ConfigMaps carry `sync.kuvryn.io/prune: disabled`, and — like dhole's — run
`emptyDir` storage: Sync does not make that persistent, and replacing those
pods loses local data. Removing a prune-protected resource from Git requires
explicit operator deletion. There is no migration record yet; the first
adoption is the release flow above.
