package prov

import (
	"embed"
	"slices"
)

// builtinFS holds the built-in templates and the boot-path bodies of the
// five first-class vendors (spec S-7, S-10).
//
//go:embed builtin
var builtinFS embed.FS

func builtinBody(name string) string {
	b, err := builtinFS.ReadFile("builtin/" + name)
	if err != nil {
		panic("prov: missing embedded template " + name) // a build defect, caught by every test
	}
	return string(b)
}

const (
	ctText = "text/plain; charset=utf-8"
	ctXML  = "application/xml; charset=utf-8"
)

var builtins = func() []Template {
	t := func(v Vendor, files ...TemplateFile) Template {
		return Template{Vendor: v, ModelGlob: "*", Name: "Built-in " + string(v), Files: files, BuiltinRef: string(v), Version: 1}
	}
	f := func(pattern, ct, body string) TemplateFile {
		return TemplateFile{Pattern: pattern, ContentType: ct, Body: builtinBody(body)}
	}
	return []Template{
		t(Yealink, f("{mac}.cfg", ctText, "yealink/device.cfg")),
		t(Poly,
			f("{mac}.cfg", ctXML, "poly/master.cfg"),
			f("000000000000.cfg", ctXML, "poly/master.cfg"),
			f("{mac}-hello.cfg", ctXML, "poly/device.cfg")),
		t(Grandstream, f("cfg{mac}.xml", ctXML, "grandstream/device.xml")),
		t(Snom,
			f("{MAC}", ctXML, "snom/device.xml"),
			f("snom{model}-{MAC}.htm", ctXML, "snom/device.xml"),
			f("snom{model}.htm", ctXML, "snom/common.xml")),
		t(Fanvil, f("{mac}.cfg", ctText, "fanvil/device.cfg")),
	}
}()

// Builtins returns the embedded built-in templates: one per first-class
// vendor, ID 0, model glob "*", priority 0, BuiltinRef the vendor's name.
// The result is a copy the caller may change.
func Builtins() []Template {
	out := slices.Clone(builtins)
	for i := range out {
		out[i].Files = slices.Clone(out[i].Files)
	}
	return out
}

// bootBodies are the boot-path bodies per vendor (spec S-10). They see
// only Phone.MAC, Phone.Model and Prov: the common bodies carry the boot
// URL, the hand-off bodies the phone's own HTTPS URL, and neither ever a
// SIP secret or an admin password, because the data they render has none.
//
// A phone fetches the common names and its per-MAC name in one boot cycle,
// in an order the vendor picks, so a common body never sets the server
// URL or the DHCP-option override: it would undo the hand-off. The
// hand-off body sets the phone's own URL and turns the DHCP option off, so
// the next boot does not go back to the boot URL. Snom's common body must
// name the server (it is how the phone finds its per-MAC name), and Poly's
// per-MAC name comes from the master, so both share one body.
type bootBody struct {
	master  string // Poly's master on the boot path (no claim)
	common  string // the common names
	handoff string // the per-MAC hand-off; common when empty
	ctype   string
}

func (b bootBody) handoffBody() string {
	if b.handoff != "" {
		return b.handoff
	}
	return b.common
}

var bootBodies = map[Vendor]bootBody{
	Yealink:     {common: builtinBody("yealink/boot.cfg"), handoff: builtinBody("yealink/handoff.cfg"), ctype: ctText},
	Poly:        {master: builtinBody("poly/boot-master.cfg"), common: builtinBody("poly/boot.cfg"), ctype: ctXML},
	Grandstream: {common: builtinBody("grandstream/boot.xml"), handoff: builtinBody("grandstream/handoff.xml"), ctype: ctXML},
	Snom:        {common: builtinBody("snom/boot.xml"), ctype: ctXML},
	Fanvil:      {common: builtinBody("fanvil/boot.cfg"), handoff: builtinBody("fanvil/handoff.cfg"), ctype: ctText},
}
