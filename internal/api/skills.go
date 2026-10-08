package api

import (
	"bytes"
	"errors"
	"net/http"

	"github.com/azrtydxb/hello/skills"
)

// listSkills lists the embedded agent skills (spec ai-external-access S-18).
func (s *server) listSkills(w http.ResponseWriter, _ *http.Request) {
	list, err := skills.List()
	if err != nil {
		s.internal(w, "list skills", err)
		return
	}
	writeJSON(w, http.StatusOK, items(list))
}

// downloadSkill answers one skill's folder as a zip archive.
func (s *server) downloadSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var buf bytes.Buffer
	if err := skills.Zip(&buf, name); err != nil {
		if errors.Is(err, skills.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "skill: not found")
			return
		}
		s.internal(w, "zip skill", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
	_, _ = w.Write(buf.Bytes())
}
