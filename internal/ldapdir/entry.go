package ldapdir

import (
	"crypto/sha256"
	"encoding/base32"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/phonebook"
	provbook "github.com/azrtydxb/hello/internal/prov/phonebook"
)

// attribute is one returned attribute; the order of an entry's attributes
// is fixed so a response is deterministic.
type attribute struct {
	name string
	vals []string
}

// entry is one directory entry as a phone sees it.
type entry struct {
	dn    string
	attrs []attribute
	norm  map[string][]string // lower-case attribute name to folded values
}

// The attributes an entry can carry and a filter can name (spec S-25).
// Anything else, userPassword included, is unknown: never returned and an
// undefined filter term.
var knownAttrs = map[string]string{
	"objectclass":     "objectClass",
	"cn":              "cn",
	"givenname":       "givenName",
	"sn":              "sn",
	"displayname":     "displayName",
	"o":               "o",
	"telephonenumber": "telephoneNumber",
	"mobile":          "mobile",
	"homephone":       "homePhone",
	"ou":              "ou",
	"dc":              "dc",
}

func knownAttr(lower string) bool { _, ok := knownAttrs[lower]; return ok }

var objectClasses = []string{"top", "person", "organizationalPerson", "inetOrgPerson"}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// buildEntries turns a phone's delivered directory into entries under
// ou=phonebook. The entry id is a hash of the contact's content, so the DN
// does not carry the name and does not change while the contact does not;
// identical contacts get a counter.
func buildEntries(base string, contacts []provbook.Contact) []*entry {
	out := make([]*entry, 0, len(contacts))
	seen := map[string]int{}
	suffix := ",ou=phonebook," + base
	for _, c := range contacts {
		id := entryID(c)
		if n := seen[id]; n > 0 {
			seen[id]++
			id += strconv.Itoa(n)
		} else {
			seen[id] = 1
		}
		e := &entry{dn: "cn=" + id + suffix, norm: map[string][]string{}}
		e.add("objectClass", objectClasses...)
		sn := c.Last
		if sn == "" {
			sn = c.First
		}
		cn := strings.TrimSpace(c.First + " " + c.Last)
		e.add("cn", cn)
		e.add("givenName", c.First)
		e.add("sn", sn)
		e.add("displayName", cn)
		e.add("o", c.Company)
		var work, mobile, home []string
		for _, n := range c.Numbers {
			switch n.Label {
			case phonebook.LabelMobile:
				mobile = append(mobile, n.Number)
			case phonebook.LabelHome:
				home = append(home, n.Number)
			default:
				work = append(work, n.Number)
			}
		}
		e.add("telephoneNumber", work...)
		e.add("mobile", mobile...)
		e.add("homePhone", home...)
		out = append(out, e)
	}
	return out
}

func entryID(c provbook.Contact) string {
	h := sha256.New()
	for _, s := range []string{c.First, c.Last, c.Company} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, n := range c.Numbers {
		h.Write([]byte(n.Label + "\x01" + n.Number))
		h.Write([]byte{0})
	}
	return strings.ToLower(idEncoding.EncodeToString(h.Sum(nil)[:10]))
}

// add appends an attribute, skipping empty values and an attribute left
// with none.
func (e *entry) add(name string, vals ...string) {
	var keep []string
	for _, v := range vals {
		if v != "" {
			keep = append(keep, v)
		}
	}
	if len(keep) == 0 {
		return
	}
	lower := strings.ToLower(name)
	e.attrs = append(e.attrs, attribute{name: name, vals: keep})
	for _, v := range keep {
		e.norm[lower] = append(e.norm[lower], normValue(lower, v))
	}
}

// project returns the attributes the search asked for: none given or "*"
// is every attribute, "1.1" none, and a name the entry type does not have
// is skipped.
func (e *entry) project(want []string, typesOnly bool) []attribute {
	all := len(want) == 0
	none := false
	set := map[string]bool{}
	for _, w := range want {
		switch w {
		case "*":
			all = true
		case "1.1":
			none = true
		default:
			set[strings.ToLower(w)] = true
		}
	}
	if none && !all && len(set) == 0 {
		return nil
	}
	var out []attribute
	for _, a := range e.attrs {
		if !all && !set[strings.ToLower(a.name)] {
			continue
		}
		if typesOnly {
			a = attribute{name: a.name}
		}
		out = append(out, a)
	}
	return out
}
