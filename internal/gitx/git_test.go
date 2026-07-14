package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendGitignorePatternsAnchorsDetectedConcretePaths(t *testing.T) {
	repo := initRepo(t)
	writeGitignore(t, repo, "# test\n")

	updated, err := AppendGitignorePatterns(repo, []string{"aicommit"})
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatalf("expected .gitignore to be updated")
	}

	data := readGitignore(t, repo)
	if !strings.Contains(data, "\n/aicommit\n") {
		t.Fatalf("expected anchored /aicommit pattern, got:\n%s", data)
	}

	ignored, err := IgnoredPaths(context.Background(), repo, []string{"aicommit", "cmd/aicommit/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ignored["aicommit"]; !ok {
		t.Fatalf("expected root binary to be ignored")
	}
	if _, ok := ignored["cmd/aicommit/main.go"]; ok {
		t.Fatalf("did not expect cmd/aicommit/main.go to be ignored")
	}
}

func TestRepairAicommitGitignoreAnchorsLegacyDetectedPaths(t *testing.T) {
	repo := initRepo(t)
	writeGitignore(t, repo, "# Created by aicommit\n*.zip\n\n# Added by aicommit after detecting protected files\naicommit\n")

	repaired, err := RepairAicommitGitignore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired {
		t.Fatalf("expected .gitignore to be repaired")
	}

	data := readGitignore(t, repo)
	if strings.Contains(data, "\naicommit\n") {
		t.Fatalf("legacy unanchored pattern should be removed, got:\n%s", data)
	}
	if !strings.Contains(data, "\n/aicommit\n") {
		t.Fatalf("expected anchored /aicommit pattern, got:\n%s", data)
	}
}

func TestCommentedDetectedRuleAllowsPathAndRemovesActiveDuplicate(t *testing.T) {
	repo := initRepo(t)
	writeGitignore(t, repo, `#/outside-managed-section.bin
# Added by aicommit after detecting protected files
#/src-tauri/icons/icon.png

# Added by aicommit after detecting protected files
/src-tauri/icons/icon.png
`)

	repaired, err := RepairAicommitGitignore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired {
		t.Fatalf("expected active duplicate to be removed")
	}

	data := readGitignore(t, repo)
	if strings.Contains(data, "\n/src-tauri/icons/icon.png\n") {
		t.Fatalf("active duplicate should be removed, got:\n%s", data)
	}
	if !strings.Contains(data, "\n#/src-tauri/icons/icon.png\n") {
		t.Fatalf("commented allow rule should be preserved, got:\n%s", data)
	}

	patterns, err := AicommitGitignoreAllowPatterns(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 1 || patterns[0] != "src-tauri/icons/icon.png" {
		t.Fatalf("unexpected allow patterns: %#v", patterns)
	}

	updated, err := AppendGitignorePatterns(repo, []string{"src-tauri/icons/icon.png"})
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatalf("commented allow rule should prevent the path from being re-added")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "-C", repo, "init", "-q")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init failed: %v\n%s", err, string(out))
	}
	return repo
}

func writeGitignore(t *testing.T, repo, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readGitignore(t *testing.T, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
