package skills_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/skills"
)

var (
	nameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// A camelCase identifier: the documented convention for naming a tool.
	camelRE  = regexp.MustCompile(`^[a-z][a-z0-9]*[A-Z][A-Za-z0-9]*$`)
	inlineRE = regexp.MustCompile("`([^`\n]+)`")
	linkRE   = regexp.MustCompile(`\]\(([^)\s]+)\)`)
)

// exposedTools lists the operation ids hello-control's MCP server turns into
// tools: every operation except those marked x-hello-mcp {"exclude": …} and
// those whose scope is secrets or session, which MCP never offers.
func exposedTools(t *testing.T) map[string]bool {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(api.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	tools := map[string]bool{}
	for _, methods := range doc.Paths {
		for _, raw := range methods {
			var op struct {
				ID    string          `json:"operationId"`
				Scope string          `json:"x-hello-scope"`
				MCP   json.RawMessage `json:"x-hello-mcp"`
			}
			if json.Unmarshal(raw, &op) != nil || op.ID == "" {
				continue // a path-level field such as parameters
			}
			if bytes.HasPrefix(bytes.TrimSpace(op.MCP), []byte("{")) || op.Scope == "secrets" || op.Scope == "session" {
				continue
			}
			tools[op.ID] = true
		}
	}
	return tools
}

// check returns every problem with the skills in fsys.
func check(fsys fs.FS, tools map[string]bool) []string {
	var problems []string
	bad := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	dirs, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return []string{err.Error()}
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		name := d.Name()
		data, err := fs.ReadFile(fsys, name+"/SKILL.md")
		if err != nil {
			bad("%s: no SKILL.md", name)
			continue
		}
		m, _, err := skills.Parse(data)
		if err != nil {
			bad("%s: %v", name, err)
			continue
		}
		switch {
		case m.Name != name:
			bad("%s: frontmatter name %q differs from the folder", name, m.Name)
		case len(m.Name) > 64 || !nameRE.MatchString(m.Name):
			bad("%s: name must be lowercase words joined by hyphens, at most 64 characters", name)
		}
		if strings.TrimSpace(m.Description) == "" || len(m.Description) > 1024 {
			bad("%s: description must be 1 to 1024 characters", name)
		}
		if m.License == "" {
			bad("%s: no license", name)
		}
		if m.Metadata["hello-version"] == "" {
			bad("%s: no metadata.hello-version", name)
		}
		if n := bytes.Count(data, []byte("\n")); n > 500 {
			bad("%s: SKILL.md has %d lines, more than 500", name, n)
		}
		if st, err := fs.Stat(fsys, name+"/references"); err != nil || !st.IsDir() {
			bad("%s: no references/ folder", name)
		}
	}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		data, _ := fs.ReadFile(fsys, p)
		for _, line := range prose(string(data)) {
			for _, l := range linkRE.FindAllStringSubmatch(line, -1) {
				target, _, _ := strings.Cut(l[1], "#")
				if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				if _, err := fs.Stat(fsys, path.Join(path.Dir(p), target)); err != nil {
					bad("%s: broken link %s", p, l[1])
				}
			}
			for _, c := range inlineRE.FindAllStringSubmatch(line, -1) {
				if camelRE.MatchString(c[1]) && !tools[c[1]] {
					bad("%s: `%s` is not a tool the MCP server exposes", p, c[1])
				}
			}
		}
		return nil
	})
	return problems
}

// prose returns the lines of a Markdown file outside fenced code blocks.
func prose(md string) []string {
	var out []string
	fenced := false
	for line := range strings.SplitSeq(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			out = append(out, line)
		}
	}
	return out
}

// TestSkills fails if a skill's frontmatter is missing or invalid, its name
// differs from its folder, SKILL.md exceeds 500 lines, a relative link is
// broken, or a camelCase inline-code identifier is not an exposed MCP tool
// (spec ai-external-access S-17).
func TestSkills(t *testing.T) {
	tools := exposedTools(t)
	if len(tools) < 20 {
		t.Fatalf("only %d tools derived from openapi.json", len(tools))
	}
	for _, p := range check(skills.FS, tools) {
		t.Error(p)
	}
	list, err := skills.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range list {
		names = append(names, s.Name)
	}
	for _, want := range []string{"hello-routing", "hello-setup", "hello-troubleshoot"} {
		if !slices.Contains(names, want) {
			t.Errorf("skill %s is not embedded (have %v)", want, names)
		}
	}

	// The checker itself: each kind of fault must be reported.
	long := strings.Repeat("x\n", 501)
	cases := map[string]fstest.MapFS{
		"no frontmatter": {"a-b/SKILL.md": file("# x\n"), "a-b/references/r.md": file("")},
		"name differs":   {"a-b/SKILL.md": skill("a-c", ""), "a-b/references/r.md": file("")},
		"bad name":       {"A_b/SKILL.md": skill("A_b", ""), "A_b/references/r.md": file("")},
		"no description": {"a-b/SKILL.md": file("---\nname: a-b\nlicense: x\nmetadata:\n  hello-version: v1\n---\n"), "a-b/references/r.md": file("")},
		"no license":     {"a-b/SKILL.md": file("---\nname: a-b\ndescription: d\nmetadata:\n  hello-version: v1\n---\n"), "a-b/references/r.md": file("")},
		"no version":     {"a-b/SKILL.md": file("---\nname: a-b\ndescription: d\nlicense: x\n---\n"), "a-b/references/r.md": file("")},
		"too long":       {"a-b/SKILL.md": skill("a-b", long), "a-b/references/r.md": file("")},
		"no references":  {"a-b/SKILL.md": skill("a-b", "")},
		"broken link":    {"a-b/SKILL.md": skill("a-b", "[x](references/missing.md)\n"), "a-b/references/r.md": file("")},
		"unknown tool":   {"a-b/SKILL.md": skill("a-b", "Use `createWidget`.\n"), "a-b/references/r.md": file("")},
	}
	for name, fsys := range cases {
		if got := check(fsys, tools); len(got) == 0 {
			t.Errorf("checker missed %s", name)
		}
	}
	good := fstest.MapFS{
		"a-b/SKILL.md":        skill("a-b", "Use `listExtensions`, see [r](references/r.md).\n```\n`notATool`\n```\n"),
		"a-b/references/r.md": file("`read` and [up](../SKILL.md)"),
	}
	if got := check(good, tools); len(got) != 0 {
		t.Errorf("checker flagged a valid skill: %v", got)
	}
}

func skill(name, body string) *fstest.MapFile {
	return file("---\nname: " + name + "\ndescription: d\nlicense: x\nmetadata:\n  hello-version: v1\n---\n" + body)
}

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
