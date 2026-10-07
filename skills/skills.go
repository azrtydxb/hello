// Package skills embeds the agent skills shipped with Hello (spec
// ai-external-access S-17, S-18): one folder per skill in the agentskills.io
// format, a SKILL.md with YAML frontmatter and a references/ folder.
package skills

import (
	"archive/zip"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FS holds every skill folder.
//
//go:embed */SKILL.md */references
var FS embed.FS

// ErrNotFound is returned for a name that is not an embedded skill.
var ErrNotFound = errors.New("skills: no such skill")

// Meta is a SKILL.md frontmatter.
type Meta struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	License     string            `yaml:"license"`
	Metadata    map[string]string `yaml:"metadata"`
}

// Skill is one embedded skill as the API lists it.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

// Parse splits a SKILL.md into its frontmatter and body.
func Parse(data []byte) (Meta, []byte, error) {
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return Meta{}, nil, errors.New("no frontmatter: the file must start with ---")
	}
	head, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return Meta{}, nil, errors.New("frontmatter is not closed by ---")
	}
	var m Meta
	if err := yaml.Unmarshal(head, &m); err != nil {
		return Meta{}, nil, fmt.Errorf("frontmatter: %w", err)
	}
	return m, body, nil
}

// List returns every embedded skill, in folder order.
func List() ([]Skill, error) {
	dirs, err := fs.ReadDir(FS, ".")
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		data, err := fs.ReadFile(FS, d.Name()+"/SKILL.md")
		if err != nil {
			return nil, err
		}
		m, _, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.Name(), err)
		}
		out = append(out, Skill{Name: m.Name, Description: m.Description, Version: m.Metadata["hello-version"]})
	}
	return out, nil
}

// Zip writes the folder of skill name to w as a zip archive whose entries
// sit under name/, ready to unpack into a client's skills directory.
func Zip(w io.Writer, name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || !fs.ValidPath(name) {
		return ErrNotFound
	}
	if _, err := fs.Stat(FS, path.Join(name, "SKILL.md")); err != nil {
		return ErrNotFound
	}
	zw := zip.NewWriter(w)
	err := fs.WalkDir(FS, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(FS, p)
		if err != nil {
			return err
		}
		f, err := zw.Create(p)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}
