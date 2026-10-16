package deploy

import (
	"strings"
	"testing"
)

// TestKWImagesPinned fails when any kw workload image floats on ":latest" (or
// has no tag or digest): kw nodes cannot pull from Docker Hub, so a floating
// reference only works from a node's image cache and breaks on reschedule.
func TestKWImagesPinned(t *testing.T) {
	for _, d := range kwDocs(t) {
		spec := podSpec(d)
		if spec == nil {
			continue
		}
		for _, key := range []string{"initContainers", "containers"} {
			for _, c := range list(dig(spec, key)) {
				img := str(dig(c, "image"))
				ref := img[strings.LastIndex(img, "/")+1:]
				if strings.Contains(ref, "@sha256:") {
					continue
				}
				i := strings.LastIndex(ref, ":")
				if i < 0 || ref[i+1:] == "" || ref[i+1:] == "latest" {
					t.Errorf("%v/%v %v: image %q is not pinned (no tag, or :latest)", d["kind"], dig(d, "metadata", "name"), dig(c, "name"), img)
				}
			}
		}
	}
}
