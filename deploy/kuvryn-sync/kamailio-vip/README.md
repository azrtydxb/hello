# Kamailio VIP go-live (opt-in)

`kw/resources.yaml` is what Sync deploys, and it keeps the Kamailio pair on
their own node addresses: phones stay on `192.168.10.101:30508`, no kube-vip
runs and the VIP `192.168.10.109` is unused. Everything that turns the VIP on
is `go-live.patch` in this directory, a unified diff against that file. The
config (`kamailio.cfg`, `EDGE_VIP`, `LEGACY_IP`) already ships in the
ConfigMap and is inert until the pod environment sets those variables, so the
patch changes the pod spec, adds kube-vip's ServiceAccount, Role and
RoleBinding, and points `HELLO_PROV_SIP_SERVER` at the VIP, nothing else (the config checksum does not change).

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
3. Merge it; Sync rolls the Kamailio pair one instance at a time and
   hello-control restarts with `HELLO_PROV_SIP_SERVER=192.168.10.109:30508`.
   The VIP follows the healthy instance; `.101` stays served
   (`KAMAILIO_LEGACY_IP`) for phones not yet moved. Check that exactly one node
   holds the VIP: `arping -I <nic> 192.168.10.109` answers from one MAC.
4. This is the step that moves phones: each phone takes the VIP at its next
   provisioning fetch. Speed it up with Resync (check-sync) on the Phones page.
   Watch `kamailio_edge_requests_total{socket=~".*legacy.*"}`.
5. When the legacy sockets have seen no traffic for longer than a registration
   lasts, every phone has moved: drop `KAMAILIO_LEGACY_IP` (and the
   `LEGACY_IP` family if you want it gone) and retire `.101` as a phone address.

To undo, revert the commit of step 2 and Resync the phones; the legacy sockets
keep serving every phone that has not moved.

## Refreshing the patch

If `kw/resources.yaml` changes near a hunk and the patch stops applying:
copy the file, apply the intended VIP edits to the copy, and regenerate with
`git diff --no-index -U3 deploy/kuvryn-sync/kw/resources.yaml <copy>`, then
rewrite the two path lines to `a/` and `b/deploy/kuvryn-sync/kw/resources.yaml`
and drop the `index` line.
