package plugincontract

import "testing"

func TestFileViewerManifestBoundaries(t *testing.T) {
	for _, bad := range []string{"", ".md", "MD", "*", "m-d", "ｍｄ"} {
		raw := mutate(t, func(doc map[string]any) {
			doc["scopes"] = []any{ScopeFilesRead, "net:example.com"}
			surface := doc["contributes"].(map[string]any)["surfaces"].([]any)[0].(map[string]any)
			surface["type"] = SurfaceFileViewer
			surface["extensions"] = []any{bad}
		})
		if _, _, err := ParseManifest(raw); err == nil {
			t.Fatalf("accepted invalid extension %q", bad)
		}
	}
	raw := mutate(t, func(doc map[string]any) {
		doc["scopes"] = []any{ScopeFilesRead, "net:example.com"}
		surface := doc["contributes"].(map[string]any)["surfaces"].([]any)[0].(map[string]any)
		surface["type"] = SurfaceFileViewer
		surface["extensions"] = []any{"md", "txt"}
	})
	manifest, _, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	viewer := manifest.Contributes.Surfaces[0]
	for name, want := range map[string]bool{".env": false, ".env.local": false, "README.MD": true, "readme.": false, "code": false, "private/note.txt": true, "a.ｍｄ": false} {
		if viewer.MatchesFile(name, "web") != want {
			t.Fatalf("unexpected match for %s", name)
		}
	}
	raw = mutate(t, func(doc map[string]any) {
		surface := doc["contributes"].(map[string]any)["surfaces"].([]any)[0].(map[string]any)
		surface["type"] = SurfaceFileViewer
		surface["extensions"] = []any{"md"}
	})
	if _, _, err = ParseManifest(raw); err == nil {
		t.Fatal("viewer without files scope accepted")
	}
	raw = mutate(t, func(doc map[string]any) {
		doc["scopes"] = []any{ScopeFilesRead, "net:example.com"}
		surfaces := doc["contributes"].(map[string]any)
		surfaces["surfaces"] = []any{map[string]any{"key": "first", "type": SurfaceFileViewer, "name": "First", "entry": "ui/main.js", "extensions": []any{"md"}}, map[string]any{"key": "second", "type": SurfaceFileViewer, "name": "Second", "entry": "ui/second.js", "extensions": []any{"md"}}}
	})
	if _, _, err = ParseManifest(raw); err == nil {
		t.Fatal("overlapping installation viewers accepted")
	}
}
