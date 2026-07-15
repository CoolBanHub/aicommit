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

func TestCompactDetectedGitignorePatternsUsesHighestFullyCoveredDirectory(t *testing.T) {
	repo := initRepo(t)
	writeGitignore(t, repo, "# test\n")
	writeRepoFile(t, repo, "docs/guide.md", "# Guide\n")
	writeRepoFile(t, repo, "docs/assets/aomiao-admin/one.png", "one\n")
	writeRepoFile(t, repo, "docs/assets/aomiao-admin/two.png", "two\n")

	got := CompactDetectedGitignorePatterns(context.Background(), repo, []string{
		"docs/assets/aomiao-admin/one.png",
		"docs/assets/aomiao-admin/two.png",
	}, nil)
	if joined := strings.Join(got, "\n"); joined != "docs/assets/*" {
		t.Fatalf("unexpected compacted patterns: %#v", got)
	}
}

func TestCompactDetectedGitignorePatternsKeepsNarrowerSafeDirectory(t *testing.T) {
	repo := initRepo(t)
	writeGitignore(t, repo, "# test\n")
	writeRepoFile(t, repo, "docs/guide.md", "# Guide\n")
	writeRepoFile(t, repo, "docs/assets/aomiao-admin/one.png", "one\n")
	writeRepoFile(t, repo, "docs/assets/aomiao-admin/two.png", "two\n")
	if err := os.MkdirAll(filepath.Join(repo, "docs", "assets", "reserved"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := CompactDetectedGitignorePatterns(context.Background(), repo, []string{
		"docs/assets/aomiao-admin/one.png",
		"docs/assets/aomiao-admin/two.png",
	}, nil)
	if joined := strings.Join(got, "\n"); joined != "docs/assets/aomiao-admin/*" {
		t.Fatalf("unexpected compacted patterns: %#v", got)
	}
}

func TestCompactDetectedGitignorePatternsDoesNotCoverOtherFiles(t *testing.T) {
	repo := initRepo(t)
	writeGitignore(t, repo, "# test\n")
	writeRepoFile(t, repo, "docs/assets/one.png", "one\n")
	writeRepoFile(t, repo, "docs/assets/two.png", "two\n")
	writeRepoFile(t, repo, "docs/assets/manifest.txt", "keep\n")
	patterns := []string{"docs/assets/one.png", "docs/assets/two.png"}

	got := CompactDetectedGitignorePatterns(context.Background(), repo, patterns, nil)
	if joined := strings.Join(got, "\n"); joined != strings.Join(patterns, "\n") {
		t.Fatalf("unexpected compacted patterns: %#v", got)
	}
}

func TestCompactDetectedGitignorePatternsRespectsExplicitAllows(t *testing.T) {
	patterns := []string{"docs/assets/one.png", "docs/assets/two.png"}

	for name, test := range map[string]struct {
		gitignore     string
		allowPatterns []string
	}{
		"configured allow": {
			gitignore:     "# test\n",
			allowPatterns: []string{"docs/assets/allowed.txt"},
		},
		"gitignore negation": {
			gitignore: "# test\n!/docs/assets/future.txt\n",
		},
		"globbed directory allow": {
			gitignore:     "# test\n",
			allowPatterns: []string{"docs/asset?/future.txt"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := initRepo(t)
			writeGitignore(t, repo, test.gitignore)
			writeRepoFile(t, repo, "docs/assets/one.png", "one\n")
			writeRepoFile(t, repo, "docs/assets/two.png", "two\n")

			got := CompactDetectedGitignorePatterns(context.Background(), repo, patterns, test.allowPatterns)
			if joined := strings.Join(got, "\n"); joined != strings.Join(patterns, "\n") {
				t.Fatalf("unexpected compacted patterns: %#v", got)
			}
		})
	}
}

func TestPreservePatternWithEscapedDirectoryBlocksCompaction(t *testing.T) {
	if !preservePatternMayAffectDirectory(`!/docs/my\ assets/future.txt`, "docs/my assets") {
		t.Fatalf("escaped directory negation should conservatively block compaction")
	}
}

func TestInvalidateCandidateSubtree(t *testing.T) {
	candidates := map[string]bool{
		"docs":                  true,
		"docs/assets":           true,
		"docs/assets/generated": true,
		"other":                 true,
	}
	invalidateCandidateSubtree("docs", candidates)
	for _, dir := range []string{"docs", "docs/assets", "docs/assets/generated"} {
		if candidates[dir] {
			t.Fatalf("expected %q to be invalidated", dir)
		}
	}
	if !candidates["other"] {
		t.Fatalf("unrelated candidate should remain valid")
	}
}

func TestCompactDetectedGitignorePatternsChecksTrackedFilesMissingFromWorktree(t *testing.T) {
	repo := initRepo(t)
	writeGitignore(t, repo, "# test\n")
	writeRepoFile(t, repo, "docs/assets/manifest.txt", "tracked\n")
	cmd := exec.Command("git", "-C", repo, "add", "docs/assets/manifest.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, string(out))
	}
	if err := os.Remove(filepath.Join(repo, "docs", "assets", "manifest.txt")); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, repo, "docs/assets/one.png", "one\n")
	writeRepoFile(t, repo, "docs/assets/two.png", "two\n")
	patterns := []string{"docs/assets/one.png", "docs/assets/two.png"}

	got := CompactDetectedGitignorePatterns(context.Background(), repo, patterns, nil)
	if joined := strings.Join(got, "\n"); joined != strings.Join(patterns, "\n") {
		t.Fatalf("tracked path missing from worktree should prevent compaction: %#v", got)
	}
}

func TestCompactDetectedGitignorePatternsDoesNotCreateWildcardFromMetacharacterDirectory(t *testing.T) {
	repo := initRepo(t)
	writeGitignore(t, repo, "# test\n")
	writeRepoFile(t, repo, "docs/assets/manifest.txt", "keep\n")
	writeRepoFile(t, repo, "docs/assets/[raw]/one.png", "one\n")
	writeRepoFile(t, repo, "docs/assets/[raw]/two.png", "two\n")
	patterns := []string{
		"docs/assets/[raw]/one.png",
		"docs/assets/[raw]/two.png",
	}

	got := CompactDetectedGitignorePatterns(context.Background(), repo, patterns, nil)
	if joined := strings.Join(got, "\n"); joined != strings.Join(patterns, "\n") {
		t.Fatalf("metacharacter directory should keep concrete patterns: %#v", got)
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

func TestCommentedDetectedDirectoryRuleBecomesRecursiveAllow(t *testing.T) {
	repo := initRepo(t)
	writeGitignore(t, repo, `# Added by aicommit after detecting protected files
#/docs/assets/*
`)

	patterns, err := AicommitGitignoreAllowPatterns(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 1 || patterns[0] != "docs/assets/**" {
		t.Fatalf("unexpected allow patterns: %#v", patterns)
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

func writeRepoFile(t *testing.T, repo, name, contents string) {
	t.Helper()
	fullPath := filepath.Join(repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
