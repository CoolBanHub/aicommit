package filter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRulesProtectsCommonSensitiveAndBinaryPaths(t *testing.T) {
	rules := NewRules(Options{MaxFileBytes: 1024})

	tests := []string{
		".env",
		".env.local",
		"node_modules/pkg/index.js",
		"dist/app.js",
		"secret.pem",
		"archive.zip",
	}

	for _, path := range tests {
		decision := rules.Decide(t.TempDir(), path)
		if decision.Allowed {
			t.Fatalf("expected %s to be protected", path)
		}
	}
}

func TestRulesAllowIncludeOverride(t *testing.T) {
	rules := NewRules(Options{Include: []string{".env.example"}})
	decision := rules.Decide(t.TempDir(), ".env.example")
	if !decision.Allowed {
		t.Fatalf("expected include override to allow path, got %q", decision.Reason)
	}
}

func TestRulesAllowManualReviewResourceExtensionsByName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"lib.so", "lib.dll", "image.jpg", "image.png", "document.pdf"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("text placeholder\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rules := NewRules(Options{MaxFileBytes: 1024})
		decision := rules.Decide(dir, name)
		if !decision.Allowed {
			t.Fatalf("expected %s to require manual review by name, got %q", name, decision.Reason)
		}
	}
}

func TestRulesDetectsBinaryContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.dat"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}

	rules := NewRules(Options{MaxFileBytes: 1024})
	decision := rules.Decide(dir, "fixture.dat")
	if decision.Allowed {
		t.Fatalf("expected binary file to be protected")
	}
}
