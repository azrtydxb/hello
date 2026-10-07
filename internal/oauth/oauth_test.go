package oauth

import "testing"

// TestResourcesAndMetadataURL fails if a resource identifier or its RFC
// 9728 metadata URL is not derived from the public URL, or a URL with a
// path is accepted as the public URL.
func TestResourcesAndMetadataURL(t *testing.T) {
	s, err := New(Options{PublicURL: "https://hello.example/"})
	if err != nil {
		t.Fatal(err)
	}
	api, mcp := s.Resources()
	if api != "https://hello.example/api/v1" || mcp != "https://hello.example/mcp" {
		t.Fatalf("Resources = %s, %s", api, mcp)
	}
	if got := s.MetadataURL(mcp); got != "https://hello.example/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("MetadataURL(mcp) = %s", got)
	}
	if got := s.MetadataURL(api); got != "https://hello.example/.well-known/oauth-protected-resource/api/v1" {
		t.Fatalf("MetadataURL(api) = %s", got)
	}
	for _, bad := range []string{"", "hello.example", "https://hello.example/x"} {
		if _, err := New(Options{PublicURL: bad}); err == nil {
			t.Errorf("New(%q) accepted", bad)
		}
	}
}
