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

func TestMatchGeneratedFiles(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		patterns []string
		want     []string
	}{
		{
			name:     "no patterns",
			files:    []string{"test.pb.go"},
			patterns: nil,
			want:     nil,
		},
		{
			name:     "all match",
			files:    []string{"test.pb.go", "api.pb.go"},
			patterns: []string{"*.pb.go"},
			want:     []string{"test.pb.go", "api.pb.go"},
		},
		{
			name:     "partial match",
			files:    []string{"test.pb.go", "handler.go"},
			patterns: []string{"*.pb.go"},
			want:     []string{"test.pb.go"},
		},
		{
			name:     "no match",
			files:    []string{"handler.go", "service.go"},
			patterns: []string{"*.pb.go"},
			want:     nil,
		},
		{
			name:     "multiple patterns",
			files:    []string{"test.pb.go", "mock_db.go", "handler.go"},
			patterns: []string{"*.pb.go", "mock_*.go"},
			want:     []string{"test.pb.go", "mock_db.go"},
		},
		{
			name:     "basename glob matches nested files",
			files:    []string{"internal/api/test.pb.go", "internal/api/handler.go"},
			patterns: []string{"*.pb.go"},
			want:     []string{"internal/api/test.pb.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchGeneratedFiles(tt.files, tt.patterns)
			if !slicesEqual(got, tt.want) {
				t.Errorf("MatchGeneratedFiles() = %v, want %v", got, tt.want)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
