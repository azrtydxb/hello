"""Add the action archive cache to a kw Arc runner scale set's values.

Why: on 2026-10-15 from 04:50 to 05:35 UTC codeload.github.com returned 404
for the archive of actions/setup-go at its pinned commit, and every job using
it failed at "Set up job". The API tarball endpoint kept serving it.

How: an init container (the runner image, so no extra image pull) downloads
each action in actions.txt through api.github.com into a per-node hostPath,
laid out as the runner's action archive cache expects
(<owner>_<repo>/<sha>.tar.gz). A file is fetched only when missing or not a
valid gzip, so a node downloads each SHA once and anonymous API rate limits
are not a concern. The runner reads the cache through
ACTIONS_RUNNER_ACTION_ARCHIVE_CACHE and downloads anything missing the
normal way, so a failed fetch only loses the cache, never the job.

Keep actions.txt in step with the SHAs pinned in .github/workflows.

Helm replaces lists, so this script merges into the release's current
values rather than layering a second values file. Apply to both releases
(arc-azrtydxb, arc-azrtydxb-amd64; chart gha-runner-scale-set 0.14.2):

  helm get values $R -n arc-runners -o yaml > cur.yaml
  python3 action-cache.py cur.yaml actions.txt new.yaml
  helm upgrade $R oci://ghcr.io/actions/actions-runner-controller-charts/gha-runner-scale-set \
    --version 0.14.2 -n arc-runners -f new.yaml

Re-running it is idempotent.
"""
import sys, yaml
cur = yaml.safe_load(open(sys.argv[1]))
actions = open(sys.argv[2]).read().split()
spec = cur["template"]["spec"]
runner = next(c for c in spec["containers"] if c["name"] == "runner")
script = r'''set -u
for a in $ACTIONS; do
  repo=${a%@*}; sha=${a#*@}
  dir=/cache/$(echo "$repo" | tr / _); f=$dir/$sha.tar.gz; t=$f.$HOSTNAME
  if [ -s "$f" ]; then gzip -t < "$f" 2>/dev/null && continue; rm -f "$f"; fi
  mkdir -p "$dir"
  if curl -fsSL --retry 3 -m 120 -o "$t" "https://api.github.com/repos/$repo/tarball/$sha" && gzip -t < "$t"; then
    chmod 0644 "$t"; mv -f "$t" "$f"; echo "cached $a"
  else
    rm -f "$t"; echo "skip $a"
  fi
done
exit 0
'''
spec["initContainers"] = [c for c in spec["initContainers"] if c["name"] != "action-cache"] + [{
  "name": "action-cache", "image": runner["image"], "imagePullPolicy": "IfNotPresent",
  "securityContext": {"runAsUser": 0},
  "env": [{"name": "ACTIONS", "value": " ".join(actions)}],
  "command": ["/bin/sh", "-c", script],
  "resources": {"requests": {"cpu": "50m", "memory": "64Mi"}, "limits": {"cpu": "1", "memory": "256Mi"}},
  "volumeMounts": [{"name": "action-cache", "mountPath": "/cache"}],
}]
runner["env"] = [e for e in runner.get("env", []) if e["name"] != "ACTIONS_RUNNER_ACTION_ARCHIVE_CACHE"] + [
  {"name": "ACTIONS_RUNNER_ACTION_ARCHIVE_CACHE", "value": "/home/runner/action-archive-cache"}]
runner["volumeMounts"] = [m for m in runner["volumeMounts"] if m["name"] != "action-cache"] + [
  {"name": "action-cache", "mountPath": "/home/runner/action-archive-cache", "readOnly": True}]
spec["volumes"] = [v for v in spec["volumes"] if v["name"] != "action-cache"] + [
  {"name": "action-cache", "hostPath": {"path": "/var/lib/arc-action-cache", "type": "DirectoryOrCreate"}}]
yaml.safe_dump(cur, open(sys.argv[3], "w"), sort_keys=False)
