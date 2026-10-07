package provclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeServer serves files by name under /p/x/ and records each request.
type fakeServer struct {
	mu    sync.Mutex
	files map[string]string
	got   []string
	uas   []string
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := strings.TrimPrefix(r.URL.Path, "/p/x/")
	f.got = append(f.got, r.Method+" "+name)
	f.uas = append(f.uas, r.UserAgent())
	if r.Method == http.MethodPut {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	body, ok := f.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(body))
}

func model(t *testing.T, vendor string) Model {
	t.Helper()
	for _, m := range Models {
		if m.Vendor == vendor {
			return m
		}
	}
	t.Fatalf("no model for %s", vendor)
	return Model{}
}

const mac = "0004f2a1b2c3"

// TestSequences fails if a vendor's request sequence, its MAC case, its
// fallbacks or its User-Agent drift from the vendor reference, or if a
// missing required file is not an error.
func TestSequences(t *testing.T) {
	yealink := "#!version:1.0.0.1\naccount.1.user_name = 101\n"
	polyMaster := `<APPLICATION APP_FILE_PATH="" CONFIG_FILES="[PHONE_MAC_ADDRESS]-hello.cfg"/>`
	polyDevice := `<polycomConfig><reg reg.1.address="101"/></polycomConfig>`
	gs := "<gs_provision><mac>" + mac + "</mac><config><P35>101</P35></config></gs_provision>"
	snom := `<settings><phone-settings><user_name idx="1" perm="R">101</user_name></phone-settings></settings>`
	fanvil := "<<VOIP CONFIG FILE>>Version:2.0002\n<SIP CONFIG MODULE>\nSIP1 PhoneNumber       :101\n<<END OF FILE>>\n"
	upper := strings.ToUpper(mac)
	cases := []struct {
		vendor string
		boot   bool
		files  map[string]string
		want   []string
		uaMAC  bool
	}{
		{"yealink", false, map[string]string{mac + ".cfg": yealink}, []string{"GET y000000000000.boot", "GET y000000000096.cfg", "GET " + mac + ".cfg", "GET " + mac + "-local.cfg", "GET " + mac + "-contact.xml"}, true},
		// Poly: the per-MAC master is missing, so it falls back to the
		// shared one, and fills in [PHONE_MAC_ADDRESS].
		{"poly", true, map[string]string{"000000000000.cfg": polyMaster, mac + "-hello.cfg": polyDevice}, []string{"GET " + mac + ".cfg", "GET 000000000000.cfg", "GET " + mac + "-hello.cfg"}, false},
		{"poly", false, map[string]string{mac + ".cfg": polyMaster, mac + "-hello.cfg": polyDevice}, []string{"GET " + mac + ".cfg", "GET " + mac + "-hello.cfg", "PUT " + mac + "-boot.log"}, false},
		{"grandstream", false, map[string]string{"cfg" + mac + ".xml": gs}, []string{"GET cfg" + mac, "GET cfg" + mac + ".bin", "GET cfg" + mac + ".xml", "GET cfggrp2614.xml", "GET cfg.xml"}, true},
		{"snom", true, map[string]string{"snomD785-" + upper + ".htm": snom}, []string{"GET snomD785.htm", "GET snomD785-" + upper + ".htm"}, false},
		{"snom", false, map[string]string{upper: snom}, []string{"GET " + upper}, false},
		{"fanvil", false, map[string]string{mac + ".cfg": fanvil}, []string{"GET F0V00X5U0000.cfg", "GET " + mac + ".cfg"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.vendor, func(t *testing.T) {
			f := &fakeServer{files: tc.files}
			srv := httptest.NewServer(f)
			defer srv.Close()
			c := Client{HTTP: srv.Client()}
			m := model(t, tc.vendor)
			pass := c.Provision
			if tc.boot {
				pass = c.Boot
			}
			res, err := pass(context.Background(), m, mac, srv.URL+"/p/x/")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.got, tc.want) {
				t.Fatalf("requests = %q, want %q", f.got, tc.want)
			}
			if res.Config.User != "101" {
				t.Fatalf("parsed user = %q, want 101", res.Config.User)
			}
			for _, ua := range f.uas {
				if !strings.Contains(ua, m.Firmware) {
					t.Fatalf("User-Agent %q lacks the firmware version", ua)
				}
				hasMAC := strings.Contains(strings.ReplaceAll(ua, ":", ""), mac)
				if hasMAC != tc.uaMAC {
					t.Fatalf("User-Agent %q: carries the MAC = %v, want %v", ua, hasMAC, tc.uaMAC)
				}
			}
		})
	}
}

// TestRequiredFileMissing fails if a phone whose device file is not
// served reports success.
func TestRequiredFileMissing(t *testing.T) {
	srv := httptest.NewServer(&fakeServer{files: map[string]string{}})
	defer srv.Close()
	for _, m := range Models {
		if _, err := (Client{HTTP: srv.Client()}).Provision(context.Background(), m, mac, srv.URL+"/p/x/"); err == nil {
			t.Errorf("%s: no error without a device file", m.Vendor)
		}
	}
}
