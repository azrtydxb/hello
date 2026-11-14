// Package sidebar builds the ticket-sidebar apps of the helpdesk connectors
// (spec integrations P16): a Zendesk app and a Freshdesk app, packaged for
// the customer to upload to their own account. The app shows the requester's
// phone numbers with a Call button and the requester's latest tickets. It
// holds no Hello credential: Call opens Hello's dialer (/dial?n=) in a new
// tab, where the agent is already signed in and click-to-call applies.
package sidebar

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"sort"
	"strings"
	"time"
)

//go:embed all:files
var files embed.FS

// ErrNoApp is a connector kind with no sidebar app.
var ErrNoApp = errors.New("sidebar: this connector has no sidebar app")

// placeholder is replaced in the app's scripts with Hello's address as a
// JSON string, so no address can break out of the script.
const placeholder = "__HELLO_URL_JSON__"

// Supported reports whether a kind has a sidebar app.
func Supported(kind string) bool { return kind == "zendesk" || kind == "freshdesk" }

// Build returns the file name and the zip of a kind's app. helloURL is the
// deployment's public https address.
func Build(kind, helloURL string) (name string, data []byte, err error) {
	if !Supported(kind) {
		return "", nil, ErrNoApp
	}
	u, err := url.Parse(helloURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", nil, errors.New("sidebar: the public address of Hello must be an https URL")
	}
	base := (&url.URL{Scheme: "https", Host: u.Host, Path: strings.TrimRight(u.Path, "/")}).String()
	quoted, err := json.Marshal(base)
	if err != nil {
		return "", nil, err
	}
	// Keep the string safe inside an HTML-embedded script as well.
	js := strings.NewReplacer("<", `<`, ">", `>`, "&", `&`).Replace(string(quoted))

	var paths []string
	root := "files/" + kind
	err = fs.WalkDir(files, root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return err
	})
	if err != nil {
		return "", nil, err
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range paths {
		raw, err := files.ReadFile(p)
		if err != nil {
			return "", nil, err
		}
		raw = bytes.ReplaceAll(raw, []byte(placeholder), []byte(js))
		h := &zip.FileHeader{Name: strings.TrimPrefix(p, root+"/"), Method: zip.Deflate}
		h.Modified = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return "", nil, err
		}
		if _, err := w.Write(raw); err != nil {
			return "", nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return "", nil, err
	}
	return "hello-" + kind + "-app.zip", buf.Bytes(), nil
}
