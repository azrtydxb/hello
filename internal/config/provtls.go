package config

import (
	"errors"
	"regexp"
	"strings"
)

var tlsModelRe = regexp.MustCompile(`^[a-z]+(:[A-Za-z0-9._ -]+)?$`)

// tlsModels reads a comma separated list of vendors or vendor:model-prefix
// entries (HELLO_PROV_SIP_TLS_MODELS). Unset or empty gives def; "none" gives
// no model.
func (r *reader) tlsModels(key, def string) []string {
	v := r.optional(key, def)
	var out []string
	for _, e := range strings.Split(v, ",") {
		e = strings.TrimSpace(e)
		if e == "" || e == "none" {
			continue
		}
		if !tlsModelRe.MatchString(e) {
			r.fail(key, errors.New("entries are a lowercase vendor or vendor:model-prefix, such as grandstream:WP8, or none"))
			return nil
		}
		out = append(out, e)
	}
	return out
}
