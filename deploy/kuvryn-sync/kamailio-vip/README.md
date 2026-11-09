# Kamailio VIP go-live (opt-in)

`kw/resources.yaml` is what Sync deploys, and it keeps the Kamailio pair on
their own node addresses: phones stay on `192.168.10.101:30508`, no kube-vip
runs and the VIP `192.168.10.109` is unused. Everything that turns the VIP on
is `go-live.patch` in this directory, a unified diff against that file. The
config (`kamailio.cfg`, `EDGE_VIP`, `LEGACY_IP`) already ships in the
ConfigMap and is inert until the pod environment sets those variables, so the
patch changes the pod spec and adds kube-vip's ServiceAccount, Role and
RoleBinding, nothing else (the config checksum does not change).

`test/deploy` applies the patch to a copy of `kw/resources.yaml` and checks the
result (VIP stage, deployer RBAC, config syntax for the VIP environments);
`test/kindha` proves a failover on the patched manifests. The patch must
therefore always apply: if a change to `kw/resources.yaml` breaks it, the
tests fail.

## Go-live (only when the user asks)

1. As cluster admin, once: `kubectl --context kw apply -f deploy/kuvryn-sync/rbac-kamailio-ha.yaml`
   (the deployer cannot grant itself the Lease rules kube-vip's Role needs).
2. In a branch off main: `git apply deploy/kuvryn-sync/kamailio-vip/go-live.patch`,
   then `git rm deploy/kuvryn-sync/kamailio-vip/go-live.patch` and delete
   `TestKwKamailioVIPIsOff` (it asserts the off state; the tests and the kind
   lab read the patched file directly once the patch file is gone). Commit,
   open the PR.
3. Merge it; Sync rolls the Kamailio pair one instance at a time. The VIP
   `192.168.10.109` now follows the healthy instance; `.101` stays served
   (`KAMAILIO_LEGACY_IP`) for phones not yet moved.
4. Re-point phones: see the provisioning step in this README's last section
   (added by the provisioning PR), then check-sync them.
5. When the last phone has moved, drop `KAMAILIO_LEGACY_IP`.

To undo before step 4, revert the commit of step 2.

## Refreshing the patch

If `kw/resources.yaml` changes near a hunk and the patch stops applying:
copy the file, apply the intended VIP edits to the copy, and regenerate with
`git diff --no-index -U3 deploy/kuvryn-sync/kw/resources.yaml <copy>`, then
rewrite the two path lines to `a/` and `b/deploy/kuvryn-sync/kw/resources.yaml`
and drop the `index` line.
