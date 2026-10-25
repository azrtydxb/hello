package provclient

// This file holds the directory parsers (spec phonebooks-ptt, P-6): each
// reads one vendor's remote phonebook format, as Hello serves it at
// /p/<token>/pb/<n>.xml, back into neutral contacts, so a test can compare
// what a phone would show with what is stored. A parser accepts only its
// own vendor's root element and refuses an entry a phone could not use (no
// name or no number), so a wrong or mangled file is an error and not a
// silently shorter list.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

// Contact is one directory entry as a phone shows it.
type Contact struct {
	// Name is the display name where the format has one name field
	// (Yealink, Snom, Fanvil); First and Last where it has two (Poly,
	// Grandstream). Both are set for neither.
	Name  string
	First string
	Last  string
	// Numbers are the entry's numbers; Label is "" where the format has
	// none for them and for a number the format does not label.
	Numbers []ContactNumber
}

// ContactNumber is one number of a contact.
type ContactNumber struct {
	Label  string // work, mobile, home or other as the vendor spells it, lowercased
	Number string
}

// DirectoryNumbers returns every number of cs, sorted: what two renderings
// of one book must agree on whatever the format does with the names.
func DirectoryNumbers(cs []Contact) []string {
	var out []string
	for _, c := range cs {
		for _, n := range c.Numbers {
			out = append(out, n.Number)
		}
	}
	slices.Sort(out)
	return out
}

// ParseDirectory reads vendor's directory format (Hello's vendor name).
func ParseDirectory(vendor string, b []byte) ([]Contact, error) {
	switch vendor {
	case "yealink":
		return ParseYealinkDirectory(b)
	case "poly":
		return ParsePolyDirectory(b)
	case "grandstream":
		return ParseGrandstreamDirectory(b)
	case "snom":
		return ParseSnomDirectory(b)
	case "fanvil":
		return ParseFanvilDirectory(b)
	}
	return nil, fmt.Errorf("no directory format for vendor %q", vendor)
}

// unmarshal decodes b into v and says which vendor's root it expected when
// the document has another.
func unmarshal(vendor string, b []byte, v any) error {
	if err := xml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s directory: %w", vendor, err)
	}
	return nil
}

// flatEntries is the Name and Telephone entry list Yealink, Snom and
// Fanvil share.
type flatEntries struct {
	Entries []struct {
		Name      string   `xml:"Name"`
		Telephone []string `xml:"Telephone"`
	} `xml:"DirectoryEntry"`
}

func (f flatEntries) contacts(vendor string, multi bool) ([]Contact, error) {
	out := make([]Contact, 0, len(f.Entries))
	for i, e := range f.Entries {
		c := Contact{Name: strings.TrimSpace(e.Name)}
		if c.Name == "" {
			return nil, fmt.Errorf("%s directory: entry %d has no Name", vendor, i+1)
		}
		if len(e.Telephone) == 0 {
			return nil, fmt.Errorf("%s directory: entry %d (%s) has no Telephone", vendor, i+1, c.Name)
		}
		if len(e.Telephone) > 1 && !multi {
			return nil, fmt.Errorf("%s directory: entry %d (%s) has %d numbers, the format takes one", vendor, i+1, c.Name, len(e.Telephone))
		}
		for _, t := range e.Telephone {
			n := strings.TrimSpace(t)
			if n == "" {
				return nil, fmt.Errorf("%s directory: entry %d (%s) has an empty Telephone", vendor, i+1, c.Name)
			}
			c.Numbers = append(c.Numbers, ContactNumber{Number: n})
		}
		out = append(out, c)
	}
	return out, nil
}

// ParseYealinkDirectory reads a Yealink remote phonebook: YealinkIPPhoneDirectory
// with one DirectoryEntry per contact, a Name and one or more Telephone.
func ParseYealinkDirectory(b []byte) ([]Contact, error) {
	var d struct {
		XMLName xml.Name `xml:"YealinkIPPhoneDirectory"`
		flatEntries
	}
	if err := unmarshal("yealink", b, &d); err != nil {
		return nil, err
	}
	return d.contacts("yealink", true)
}

// ParseSnomDirectory reads a Snom directory: SnomIPPhoneDirectory with a
// DirectoryEntry per number, a Name and one Telephone.
func ParseSnomDirectory(b []byte) ([]Contact, error) {
	var d struct {
		XMLName xml.Name `xml:"SnomIPPhoneDirectory"`
		flatEntries
	}
	if err := unmarshal("snom", b, &d); err != nil {
		return nil, err
	}
	return d.contacts("snom", false)
}

// ParseFanvilDirectory reads a Fanvil phonebook: IPPhoneDirectory with a
// DirectoryEntry per number, a Name and one Telephone.
func ParseFanvilDirectory(b []byte) ([]Contact, error) {
	var d struct {
		XMLName xml.Name `xml:"IPPhoneDirectory"`
		flatEntries
	}
	if err := unmarshal("fanvil", b, &d); err != nil {
		return nil, err
	}
	return d.contacts("fanvil", false)
}

// ParsePolyDirectory reads a Poly directory: directory/item_list with an
// item per number, its ln, fn, ct (the number) and lb (the label).
func ParsePolyDirectory(b []byte) ([]Contact, error) {
	var d struct {
		XMLName xml.Name `xml:"directory"`
		Items   []struct {
			Last   string `xml:"ln"`
			First  string `xml:"fn"`
			Number string `xml:"ct"`
			Label  string `xml:"lb"`
		} `xml:"item_list>item"`
	}
	if err := unmarshal("poly", b, &d); err != nil {
		return nil, err
	}
	out := make([]Contact, 0, len(d.Items))
	for i, it := range d.Items {
		c := Contact{Last: strings.TrimSpace(it.Last), First: strings.TrimSpace(it.First)}
		if c.Last == "" && c.First == "" {
			return nil, fmt.Errorf("poly directory: item %d has no ln or fn", i+1)
		}
		n := strings.TrimSpace(it.Number)
		if n == "" {
			return nil, fmt.Errorf("poly directory: item %d (%s %s) has no ct", i+1, c.First, c.Last)
		}
		c.Numbers = []ContactNumber{{Label: strings.ToLower(strings.TrimSpace(it.Label)), Number: n}}
		out = append(out, c)
	}
	return out, nil
}

// ParseGrandstreamDirectory reads a Grandstream phonebook: AddressBook with
// a version and a Contact each with a LastName, a FirstName and Phone
// elements (a type attribute and a phonenumber).
func ParseGrandstreamDirectory(b []byte) ([]Contact, error) {
	var d struct {
		XMLName  xml.Name `xml:"AddressBook"`
		Version  string   `xml:"version"`
		Contacts []struct {
			Last   string `xml:"LastName"`
			First  string `xml:"FirstName"`
			Phones []struct {
				Type   string `xml:"type,attr"`
				Number string `xml:"phonenumber"`
			} `xml:"Phone"`
		} `xml:"Contact"`
	}
	if err := unmarshal("grandstream", b, &d); err != nil {
		return nil, err
	}
	if strings.TrimSpace(d.Version) == "" {
		return nil, errors.New("grandstream directory: no version")
	}
	out := make([]Contact, 0, len(d.Contacts))
	for i, e := range d.Contacts {
		c := Contact{Last: strings.TrimSpace(e.Last), First: strings.TrimSpace(e.First)}
		if c.Last == "" && c.First == "" {
			return nil, fmt.Errorf("grandstream directory: contact %d has no LastName or FirstName", i+1)
		}
		if len(e.Phones) == 0 {
			return nil, fmt.Errorf("grandstream directory: contact %d (%s %s) has no Phone", i+1, c.First, c.Last)
		}
		for _, p := range e.Phones {
			n := strings.TrimSpace(p.Number)
			if n == "" {
				return nil, fmt.Errorf("grandstream directory: contact %d (%s %s) has a Phone without phonenumber", i+1, c.First, c.Last)
			}
			c.Numbers = append(c.Numbers, ContactNumber{Label: strings.ToLower(p.Type), Number: n})
		}
		out = append(out, c)
	}
	return out, nil
}

// Directory fetches url as the phone m with MAC mac would (its User-Agent,
// a GET) and parses the answer in the vendor's format. A status other than
// 200 is an error.
func (c Client) Directory(ctx context.Context, m Model, mac, url string) ([]Contact, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", m.UserAgent(mac))
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4*maxFile))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s = %d, want 200", url, res.StatusCode)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, fmt.Errorf("GET %s: an empty directory", url)
	}
	return ParseDirectory(m.Vendor, b)
}
