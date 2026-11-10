package directory

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
)

// fakeEntry is an entry of the fake server.
type fakeEntry struct {
	DN    string
	Attrs map[string][][]byte
}

func strs(v ...string) [][]byte {
	out := make([][]byte, len(v))
	for i, s := range v {
		out[i] = []byte(s)
	}
	return out
}

// fakeLDAP is an in-process LDAP server: bind, paged search and StartTLS. It
// is only as much of the protocol as the connector uses.
type fakeLDAP struct {
	t        *testing.T
	ln       net.Listener
	cert     tls.Certificate
	CAPEM    string
	implicit bool
	startTLS bool

	mu       sync.Mutex
	BindDN   string
	BindPW   string
	Users    []fakeEntry
	Groups   []fakeEntry
	Members  map[string][]string // group DN -> user entry ids (for in-chain)
	Filters  []string
	Bound    int
	PageSize int // forced page size, 0 = the client's
	Referral bool
	FailCode int // result code for searches

	// RootDSE is the server's root entry; NTDS is the object dsServiceName
	// points at (it holds the invocation ID).
	RootDSE map[string][][]byte
	NTDS    map[string][][]byte
}

const fakeNTDSDN = "CN=NTDS Settings,CN=DC1,CN=Servers,CN=Site,CN=Sites,CN=Configuration,DC=corp"

// enableAD makes the fake answer like a domain controller: a rootDSE with
// highestCommittedUSN and an invocation ID.
func (f *fakeLDAP) enableAD(usn int, invocation []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RootDSE = map[string][][]byte{"highestCommittedUSN": strs(fmt.Sprint(usn)), "dsServiceName": strs(fakeNTDSDN),
		"supportedCapabilities": strs("1.2.840.113556.1.4.800")}
	f.NTDS = map[string][][]byte{"invocationId": {invocation}}
}

func (f *fakeLDAP) setUSN(usn int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RootDSE["highestCommittedUSN"] = strs(fmt.Sprint(usn))
}

var changedRe = regexp.MustCompile(`\((uSNChanged|whenChanged|modifyTimestamp)>=([^)]*)\)`)

// changedSince keeps the entries a (uSNChanged|whenChanged|modifyTimestamp)>=
// clause of the filter selects.
func changedSince(filter string, in []fakeEntry) []fakeEntry {
	m := changedRe.FindStringSubmatch(filter)
	if m == nil {
		return in
	}
	var out []fakeEntry
	for _, e := range in {
		vals := e.Attrs[m[1]]
		if len(vals) == 0 {
			continue
		}
		v := string(vals[0])
		if m[1] == "uSNChanged" {
			a, _ := strconv.Atoi(v)
			b, _ := strconv.Atoi(m[2])
			if a >= b {
				out = append(out, e)
			}
		} else if v >= m[2] {
			out = append(out, e)
		}
	}
	return out
}

func selfSigned(t *testing.T, names ...string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake-dc"}, DNSNames: names,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:   time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func startFakeLDAP(t *testing.T, implicit bool) *fakeLDAP {
	t.Helper()
	f := &fakeLDAP{t: t, implicit: implicit, startTLS: !implicit, BindDN: "cn=svc,dc=corp", BindPW: "pw", Members: map[string][]string{}}
	f.cert, f.CAPEM = selfSigned(t, "dc.corp.test")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeLDAP) addr() string { return f.ln.Addr().String() }

func (f *fakeLDAP) tlsConf() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12}
}

func octet(s string) *ber.Packet {
	return ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, s, "")
}

func envelopeOf(id int64, op *ber.Packet, ctl *ber.Packet) []byte {
	p := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "msg")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, "id"))
	p.AppendChild(op)
	if ctl != nil {
		cs := ber.Encode(ber.ClassContext, ber.TypeConstructed, 0, nil, "controls")
		cs.AppendChild(ctl)
		p.AppendChild(cs)
	}
	return p.Bytes()
}

func resultOp(tag ber.Tag, code int, diag string) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "result")
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, int64(code), "code"))
	op.AppendChild(octet(""))
	op.AppendChild(octet(diag))
	return op
}

func (f *fakeLDAP) serve(raw net.Conn) {
	defer func() { _ = raw.Close() }()
	conn := raw
	if f.implicit {
		conn = tls.Server(raw, f.tlsConf())
	}
	br := bufio.NewReader(conn)
	type pageState struct{ rest []fakeEntry }
	var paging *pageState
	for {
		p, err := ber.ReadPacket(br)
		if err != nil {
			return
		}
		id := p.Children[0].Value.(int64)
		op := p.Children[1]
		switch op.Tag {
		case 0: // bind
			dn := op.Children[1].Data.String()
			pw := op.Children[2].Data.String()
			code := 0
			f.mu.Lock()
			f.Bound++
			if dn != f.BindDN || pw != f.BindPW {
				code = 49
			}
			f.mu.Unlock()
			_, _ = conn.Write(envelopeOf(id, resultOp(1, code, ""), nil))
		case 2: // unbind
			return
		case 23: // extended: StartTLS
			if op.Children[0].Data.String() == "1.3.6.1.4.1.1466.20037" && f.startTLS {
				r := resultOp(24, 0, "")
				r.AppendChild(ber.NewString(ber.ClassContext, ber.TypePrimitive, 10, "1.3.6.1.4.1.1466.20037", "name"))
				_, _ = conn.Write(envelopeOf(id, r, nil))
				conn = tls.Server(raw, f.tlsConf())
				br = bufio.NewReader(conn)
				continue
			}
			_, _ = conn.Write(envelopeOf(id, resultOp(24, 2, "no"), nil))
		case 3: // search
			filter, _ := ldap.DecompileFilter(op.Children[6])
			f.mu.Lock()
			f.Filters = append(f.Filters, filter)
			fail := f.FailCode
			f.mu.Unlock()
			if fail != 0 {
				_, _ = conn.Write(envelopeOf(id, resultOp(5, fail, "secret server text"), nil))
				continue
			}
			base := op.Children[0].Data.String()
			var size int64
			var cookie []byte
			if len(p.Children) > 2 {
				for _, c := range p.Children[2].Children {
					if dc, err := ldap.DecodeControl(c); err == nil {
						if pc, ok := dc.(*ldap.ControlPaging); ok {
							size, cookie = int64(pc.PagingSize), pc.Cookie
						}
					}
				}
			}
			var all []fakeEntry
			if paging != nil && len(cookie) > 0 {
				all = paging.rest
			} else {
				all = f.match(base, filter)
			}
			if f.PageSize > 0 {
				size = int64(f.PageSize)
			}
			page := all
			next := []byte(nil)
			if size > 0 && int64(len(all)) > size {
				page, paging, next = all[:size], &pageState{rest: all[size:]}, []byte("more")
			} else {
				paging = nil
			}
			for _, e := range page {
				ent := ber.Encode(ber.ClassApplication, ber.TypeConstructed, 4, nil, "entry")
				ent.AppendChild(octet(e.DN))
				attrs := ber.NewSequence("attrs")
				for name, vals := range e.Attrs {
					a := ber.NewSequence("attr")
					a.AppendChild(octet(name))
					set := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "vals")
					for _, v := range vals {
						set.AppendChild(octet(string(v)))
					}
					a.AppendChild(set)
					attrs.AppendChild(a)
				}
				ent.AppendChild(attrs)
				_, _ = conn.Write(envelopeOf(id, ent, nil))
			}
			val := ber.NewSequence("v")
			val.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, 0, "size"))
			val.AppendChild(octet(string(next)))
			ctl := ber.NewSequence("control")
			ctl.AppendChild(octet(ldap.ControlTypePaging))
			ctl.AppendChild(octet(string(val.Bytes())))
			_, _ = conn.Write(envelopeOf(id, resultOp(5, 0, ""), ctl))
		}
	}
}

// match picks the entries a search returns: the in-chain filter returns the
// members of the group it names, a group base returns the groups.
func (f *fakeLDAP) match(base, filter string) []fakeEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	if base == "" && f.RootDSE != nil {
		return []fakeEntry{{DN: "", Attrs: f.RootDSE}}
	}
	if base == "" {
		return nil
	}
	if base == fakeNTDSDN && f.NTDS != nil {
		return []fakeEntry{{DN: base, Attrs: f.NTDS}}
	}
	if base == "ou=groups,dc=corp" {
		return changedSince(filter, append([]fakeEntry(nil), f.Groups...))
	}
	for dn, ids := range f.Members {
		if containsStr(filter, "1.2.840.113556.1.4.1941:="+dn+")") {
			var out []fakeEntry
			for _, u := range f.Users {
				for _, id := range ids {
					if u.DN == id {
						out = append(out, fakeEntry{DN: u.DN, Attrs: map[string][][]byte{"objectGUID": u.Attrs["objectGUID"]}})
					}
				}
			}
			return out
		}
	}
	return changedSince(filter, append([]fakeEntry(nil), f.Users...))
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// guidBytes is the little-endian objectGUID of the canonical string parts.
func guidBytes(d1 uint32, d2, d3 uint16, tail []byte) []byte {
	b := make([]byte, 16)
	b[0], b[1], b[2], b[3] = byte(d1), byte(d1>>8), byte(d1>>16), byte(d1>>24)
	b[4], b[5] = byte(d2), byte(d2>>8)
	b[6], b[7] = byte(d3), byte(d3>>8)
	copy(b[8:], tail)
	return b
}
