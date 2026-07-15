package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/CoolBanHub/aicommit/internal/filter"
)

func TestRunCommitBlocksForcedStagedFileIgnoredByGitignore(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	writeFile(t, repo, ".gitignore", "*.png\n")
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "image.png", "text placeholder\n")
	writeFile(t, repo, "app.txt", "code\n")
	runGit(t, repo, "add", "-f", "image.png")
	runGit(t, repo, "add", "app.txt")

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Message:    "Add app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NoChanges {
		t.Fatalf("expected allowed changes to be committed")
	}
	if !containsDecision(result.StagedProtected, "image.png", "ignored by .gitignore") {
		t.Fatalf("expected image.png to be unstaged as ignored, got %#v", result.StagedProtected)
	}
	if contains(result.Files, "image.png") {
		t.Fatalf("image.png should not be part of the generated commit: %#v", result.Files)
	}
	if !contains(result.Files, "app.txt") {
		t.Fatalf("app.txt should be committed: %#v", result.Files)
	}

	headFiles := gitOutput(t, repo, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(headFiles, "image.png") {
		t.Fatalf("ignored file was committed:\n%s", headFiles)
	}
	if !strings.Contains(headFiles, "app.txt") {
		t.Fatalf("allowed file was not committed:\n%s", headFiles)
	}
	if cached := strings.TrimSpace(gitOutput(t, repo, "diff", "--cached", "--name-only")); cached != "" {
		t.Fatalf("expected clean index after commit, got %q", cached)
	}
}

func TestRunCommitBlocksTrackedFileCoveredByGitignore(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	writeFile(t, repo, "image.png", "old\n")
	runGit(t, repo, "add", "image.png")
	runGit(t, repo, "commit", "-m", "track image")

	writeFile(t, repo, ".gitignore", "*.png\n")
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-m", "ignore png")

	writeFile(t, repo, "image.png", "new\n")
	writeFile(t, repo, "app.txt", "code\n")

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Message:    "Add app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NoChanges {
		t.Fatalf("expected allowed changes to be committed")
	}
	if !containsDecision(result.UnstagedProtected, "image.png", "ignored by .gitignore") {
		t.Fatalf("expected modified tracked image.png to be skipped as ignored, got %#v", result.UnstagedProtected)
	}
	if contains(result.Files, "image.png") {
		t.Fatalf("image.png should not be part of the generated commit: %#v", result.Files)
	}

	headFiles := gitOutput(t, repo, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(headFiles, "image.png") {
		t.Fatalf("ignored tracked file modification was committed:\n%s", headFiles)
	}
	headImage := gitOutput(t, repo, "show", "HEAD:image.png")
	if headImage != "old\n" {
		t.Fatalf("expected HEAD image.png to remain unchanged, got %q", headImage)
	}
	status := gitOutput(t, repo, "status", "--porcelain=v1", "--", "image.png")
	if !strings.Contains(status, " M image.png") {
		t.Fatalf("expected image.png to remain modified in worktree, got %q", status)
	}
}

func TestRunCommitAllowsBinaryFromCommentedManagedGitignoreRule(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	path := "src-tauri/icons/icon.png"

	writeFile(t, repo, ".gitignore", `# Added by aicommit after detecting protected files
#/src-tauri/icons/icon.png

# Added by aicommit after detecting protected files
/src-tauri/icons/icon.png
`)
	writeBytes(t, repo, path, []byte{0, 1, 2, 3})

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Message:    "Add application icon",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NoChanges {
		t.Fatalf("expected commented rule to allow the binary file")
	}
	if !contains(result.Files, path) {
		t.Fatalf("expected icon to be committed, got %#v", result.Files)
	}
	if result.Metadata["gitignoreRepaired"] != "true" {
		t.Fatalf("expected duplicate active rule to be repaired, got %#v", result.Metadata)
	}

	headFiles := gitOutput(t, repo, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(headFiles, path) {
		t.Fatalf("allowed icon was not committed:\n%s", headFiles)
	}
	gitignore := gitOutput(t, repo, "show", "HEAD:.gitignore")
	if strings.Contains(gitignore, "\n/src-tauri/icons/icon.png\n") {
		t.Fatalf("active ignore rule should not be present:\n%s", gitignore)
	}
	if !strings.Contains(gitignore, "\n#/src-tauri/icons/icon.png\n") {
		t.Fatalf("commented allow rule should remain:\n%s", gitignore)
	}
}

func TestRunCommitCompactsFullyProtectedDirectoryInGitignore(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	writeFile(t, repo, ".gitignore", "# baseline\n")
	writeFile(t, repo, "docs/guide.md", "# Guide\n")
	writeBytes(t, repo, "docs/assets/aomiao-admin/one.png", []byte{0, 1, 2, 3})
	writeBytes(t, repo, "docs/assets/aomiao-admin/two.png", []byte{0, 4, 5, 6})

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Message:    "docs: add guide",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NoChanges {
		t.Fatalf("expected allowed documentation to be committed")
	}

	gitignore := gitOutput(t, repo, "show", "HEAD:.gitignore")
	if !strings.Contains(gitignore, "\n/docs/assets/*\n") {
		t.Fatalf("expected compacted directory rule, got:\n%s", gitignore)
	}
	for _, path := range []string{
		"/docs/assets/aomiao-admin/one.png",
		"/docs/assets/aomiao-admin/two.png",
	} {
		if strings.Contains(gitignore, "\n"+path+"\n") {
			t.Fatalf("did not expect concrete rule %q after compaction:\n%s", path, gitignore)
		}
	}
	if ignored := strings.TrimSpace(gitOutput(t, repo, "check-ignore", "docs/assets/aomiao-admin/one.png")); ignored != "docs/assets/aomiao-admin/one.png" {
		t.Fatalf("expected compacted rule to ignore nested asset, got %q", ignored)
	}
}

func TestRunCommitDoesNotCompactAcrossTrackedAllowedFile(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	writeFile(t, repo, ".gitignore", "# baseline\n")
	writeFile(t, repo, "docs/assets/manifest.txt", "tracked metadata\n")
	runGit(t, repo, "add", ".gitignore", "docs/assets/manifest.txt")
	runGit(t, repo, "commit", "-m", "add asset manifest")

	writeBytes(t, repo, "docs/assets/one.png", []byte{0, 1, 2, 3})
	writeBytes(t, repo, "docs/assets/two.png", []byte{0, 4, 5, 6})

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Message:    "chore: protect generated assets",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NoChanges {
		t.Fatalf("expected concrete ignore rules to be committed")
	}

	gitignore := gitOutput(t, repo, "show", "HEAD:.gitignore")
	if strings.Contains(gitignore, "\n/docs/assets/*\n") {
		t.Fatalf("tracked allowed file should prevent directory compaction:\n%s", gitignore)
	}
	for _, path := range []string{"/docs/assets/one.png", "/docs/assets/two.png"} {
		if !strings.Contains(gitignore, "\n"+path+"\n") {
			t.Fatalf("expected concrete rule %q, got:\n%s", path, gitignore)
		}
	}
}

func TestRunCommitAllowsNestedBinaryFromCommentedManagedDirectoryRule(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	path := "docs/assets/aomiao-admin/icon.png"

	writeFile(t, repo, ".gitignore", `# Added by aicommit after detecting protected files
#/docs/assets/*

# Added by aicommit after detecting protected files
/docs/assets/*
`)
	writeBytes(t, repo, path, []byte{0, 1, 2, 3})

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Message:    "docs: add application icon",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NoChanges {
		t.Fatalf("expected commented directory rule to allow the binary file")
	}
	if !contains(result.Files, path) {
		t.Fatalf("expected nested icon to be committed, got %#v", result.Files)
	}
	if result.Metadata["gitignoreRepaired"] != "true" {
		t.Fatalf("expected active directory duplicate to be repaired, got %#v", result.Metadata)
	}
	gitignore := gitOutput(t, repo, "show", "HEAD:.gitignore")
	if !strings.Contains(gitignore, "\n#/docs/assets/*\n") {
		t.Fatalf("commented directory rule should remain:\n%s", gitignore)
	}
	if strings.Contains(gitignore, "\n/docs/assets/*\n") {
		t.Fatalf("active directory duplicate should be removed:\n%s", gitignore)
	}
}

func TestRunCommitHandlesAlreadyStagedDeletion(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	path := "Wecom.GroupForwarder - bak/App.config"

	writeFile(t, repo, ".gitignore", "# baseline\n")
	writeFile(t, repo, path, "original\n")
	runGit(t, repo, "add", ".gitignore", path)
	runGit(t, repo, "commit", "-m", "initial")

	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(path))); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A", "--", path)

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Message:    "Remove backup project",
		DryRun:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result.Files, path) {
		t.Fatalf("expected staged deletion in result files, got %#v", result.Files)
	}
	if got := gitOutput(t, repo, "status", "--porcelain=v1", "--", path); !strings.HasPrefix(got, "D ") {
		t.Fatalf("expected deletion to remain staged, got %q", got)
	}
}

func TestRunCommitStagesWorktreeChangeOnAlreadyStagedPath(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	writeFile(t, repo, ".gitignore", "# baseline\n")
	writeFile(t, repo, "app.txt", "original\n")
	runGit(t, repo, "add", ".gitignore", "app.txt")
	runGit(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "app.txt", "staged\n")
	runGit(t, repo, "add", "app.txt")
	writeFile(t, repo, "app.txt", "latest\n")

	_, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Message:    "Update app",
		DryRun:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, repo, "show", ":app.txt"); got != "latest\n" {
		t.Fatalf("expected latest worktree content to be staged, got %q", got)
	}
}

func TestRunCommitAnchorsDetectedRootBinaryWithoutIgnoringCommandSourceDir(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	writeBytes(t, repo, "aicommit", []byte{0, 1, 2, 3})
	writeFile(t, repo, "cmd/aicommit/main.go", "package main\n\nfunc main() {}\n")

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Message:    "Add CLI source",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NoChanges {
		t.Fatalf("expected source changes to be committed")
	}
	if !containsDecision(result.Skipped, "aicommit", "binary file") {
		t.Fatalf("expected root binary to be skipped, got %#v", result.Skipped)
	}
	if !contains(result.Files, "cmd/aicommit/main.go") {
		t.Fatalf("expected command source to be committed, got %#v", result.Files)
	}
	if contains(result.Files, "aicommit") {
		t.Fatalf("root binary should not be committed, got %#v", result.Files)
	}

	gitignore := gitOutput(t, repo, "show", "HEAD:.gitignore")
	if !strings.Contains(gitignore, "\n/aicommit\n") {
		t.Fatalf("expected anchored root binary ignore pattern, got:\n%s", gitignore)
	}
	if strings.Contains(gitignore, "\naicommit\n") {
		t.Fatalf("unanchored pattern would ignore cmd/aicommit, got:\n%s", gitignore)
	}
}

func TestRunCommitUsesConfiguredMessageForGeneratedOnlyChanges(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeConfigFile(t, configPath, `provider: cdp
generated:
  patterns:
    - "*.pb.go"
  message: "chore: refresh generated files"
`)
	t.Setenv("AICOMMIT_CDP_COMMAND", "exit 42")

	writeFile(t, repo, ".gitignore", "# baseline\n")
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "api.pb.go", "package api\n")

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		DryRun:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "generated" {
		t.Fatalf("expected generated provider, got %q", result.Provider)
	}
	if result.Message != "chore: refresh generated files" {
		t.Fatalf("unexpected message %q", result.Message)
	}
	if result.Metadata["generatedFiles"] != "true" {
		t.Fatalf("expected generatedFiles metadata, got %#v", result.Metadata)
	}
	if !contains(result.Files, "api.pb.go") {
		t.Fatalf("expected generated file to be staged, got %#v", result.Files)
	}
}

func TestRunCommitPassesGeneratedFilesToProviderForMixedChanges(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeConfigFile(t, configPath, `provider: cdp
generated:
  patterns:
    - "*.pb.go"
  message: "chore: refresh generated files"
`)
	promptPath := filepath.Join(t.TempDir(), "prompt.txt")
	t.Setenv("PROMPT_CAPTURE", promptPath)
	t.Setenv("AICOMMIT_CDP_COMMAND", `cat > "$PROMPT_CAPTURE"; printf '{"message":"feat: update handler"}'`)

	writeFile(t, repo, ".gitignore", "# baseline\n")
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "api.pb.go", "package api\n")
	writeFile(t, repo, "handler.go", "package api\n")

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		DryRun:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "cdp" {
		t.Fatalf("expected cdp provider, got %q", result.Provider)
	}
	if result.Message != "feat: update handler" {
		t.Fatalf("unexpected message %q", result.Message)
	}
	if result.Metadata["generatedFiles"] == "true" {
		t.Fatalf("did not expect generated-only metadata for mixed changes")
	}

	promptData, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(promptData)
	if !strings.Contains(prompt, "Generated files (auto-generated, focus changes on other files):") {
		t.Fatalf("expected generated-files note in prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "api.pb.go") {
		t.Fatalf("expected generated file in prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "handler.go") {
		t.Fatalf("expected non-generated file in prompt:\n%s", prompt)
	}
}

func TestRunCommitAutoFallsBackFromClaudeCodeToCodex(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	binDir := t.TempDir()
	writeExecutableScript(t, binDir, "claude", `#!/bin/sh
printf '%s\n' '{"type":"result","is_error":true,"result":"API Error: Request rejected (429)"}'
exit 1
`)
	writeExecutableScript(t, binDir, "codex", `#!/bin/sh
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    output="$1"
  fi
  shift
done
printf '%s\n' '{"message":"fix: update refund price"}' > "$output"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	writeFile(t, repo, "refund.go", "package refund\n")

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		DryRun:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "codex" {
		t.Fatalf("expected codex fallback, got %q", result.Provider)
	}
	if result.Message != "fix: update refund price" {
		t.Fatalf("unexpected message %q", result.Message)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "API Error: Request rejected (429)") {
		t.Fatalf("expected claude failure warning, got %#v", result.Warnings)
	}
}

func TestRunCommitExplicitClaudeCodeDoesNotFallback(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	binDir := t.TempDir()
	writeExecutableScript(t, binDir, "claude", `#!/bin/sh
printf '%s\n' '{"type":"result","is_error":true,"result":"API Error: Request rejected (429)"}'
exit 1
`)
	writeExecutableScript(t, binDir, "codex", `#!/bin/sh
printf '%s\n' '{"message":"fix: should not be used"}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	writeFile(t, repo, "refund.go", "package refund\n")

	_, err := RunCommit(context.Background(), CommitOptions{
		Repo:       repo,
		ConfigPath: configPath,
		Provider:   "claude-code",
		DryRun:     true,
	})
	if err == nil || !strings.Contains(err.Error(), "API Error: Request rejected (429)") {
		t.Fatalf("expected explicit claude failure, got %v", err)
	}
}

func TestRunCommitDoesNotAutoIgnoreLargeTextDocuments(t *testing.T) {
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	writeFile(t, repo, ".gitignore", "# baseline\n")
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "references/api-automation.md", "# API automation\n\n"+strings.Repeat("field description ", 2000)+"\n")

	result, err := RunCommit(context.Background(), CommitOptions{
		Repo:         repo,
		ConfigPath:   configPath,
		Message:      "docs: add api automation reference",
		MaxFileBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NoChanges {
		t.Fatalf("expected large text document to be skipped by explicit maxFileBytes limit")
	}
	if !containsDecision(result.Skipped, "references/api-automation.md", "file is larger than maxFileBytes") {
		t.Fatalf("expected Markdown document to be skipped by size only, got %#v", result.Skipped)
	}

	gitignoreData, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	gitignore := string(gitignoreData)
	if strings.Contains(gitignore, "references/api-automation.md") {
		t.Fatalf("Markdown document should not be auto-ignored, got:\n%s", gitignore)
	}
}

func TestRunTagIncrementsLatestNumericTag(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, repo, "app.txt", "code\n")
	runGit(t, repo, "add", "app.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "tag", "v0.0.1")

	result, err := RunTag(context.Background(), TagOptions{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tag != "v0.0.2" {
		t.Fatalf("expected v0.0.2, got %q", result.Tag)
	}
	if result.Previous != "v0.0.1" {
		t.Fatalf("expected previous v0.0.1, got %q", result.Previous)
	}
	if got := strings.TrimSpace(gitOutput(t, repo, "tag", "--list", "v0.0.2")); got != "v0.0.2" {
		t.Fatalf("expected tag to be created, got %q", got)
	}
}

func TestRunTagIncrementsFourPartVersion(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, repo, "app.txt", "code\n")
	runGit(t, repo, "add", "app.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "tag", "v1.2.3.4")

	result, err := RunTag(context.Background(), TagOptions{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tag != "v1.2.3.5" {
		t.Fatalf("expected v1.2.3.5, got %q", result.Tag)
	}
	if result.Previous != "v1.2.3.4" {
		t.Fatalf("expected previous v1.2.3.4, got %q", result.Previous)
	}
}

func TestRunTagCreatesSpecifiedTag(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, repo, "app.txt", "code\n")
	runGit(t, repo, "add", "app.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "tag", "v0.0.1")

	result, err := RunTag(context.Background(), TagOptions{Repo: repo, Tag: "v2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tag != "v2.0.0" {
		t.Fatalf("expected v2.0.0, got %q", result.Tag)
	}
	if result.Previous != "" {
		t.Fatalf("did not expect previous for explicit tag, got %q", result.Previous)
	}
	if got := strings.TrimSpace(gitOutput(t, repo, "tag", "--list", "v2.0.0")); got != "v2.0.0" {
		t.Fatalf("expected explicit tag to be created, got %q", got)
	}
}

func TestRunTagDefaultsWhenNoNumericTagsExist(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, repo, "app.txt", "code\n")
	runGit(t, repo, "add", "app.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "tag", "release")

	result, err := RunTag(context.Background(), TagOptions{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tag != "v0.0.1" {
		t.Fatalf("expected v0.0.1, got %q", result.Tag)
	}
	if result.Previous != "" {
		t.Fatalf("did not expect previous, got %q", result.Previous)
	}
}

func TestRunTagPushesCreatedTag(t *testing.T) {
	repo := initGitRepo(t)
	remote := initBareGitRepo(t)
	runGit(t, repo, "remote", "add", "origin", remote)
	writeFile(t, repo, "app.txt", "code\n")
	runGit(t, repo, "add", "app.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "tag", "v0.0.1")

	result, err := RunTag(context.Background(), TagOptions{Repo: repo, Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tag != "v0.0.2" {
		t.Fatalf("expected v0.0.2, got %q", result.Tag)
	}
	if !result.Pushed {
		t.Fatalf("expected tag to be pushed, got %#v", result)
	}
	if result.PushTarget != "origin/v0.0.2" {
		t.Fatalf("expected origin/v0.0.2 target, got %q", result.PushTarget)
	}
	if got := strings.TrimSpace(gitOutput(t, remote, "tag", "--list", "v0.0.2")); got != "v0.0.2" {
		t.Fatalf("expected remote tag to exist, got %q", got)
	}
}

func TestRunPushTagPushesLatestNumericTag(t *testing.T) {
	repo := initGitRepo(t)
	remote := initBareGitRepo(t)
	runGit(t, repo, "remote", "add", "origin", remote)
	writeFile(t, repo, "app.txt", "code\n")
	runGit(t, repo, "add", "app.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "tag", "release")
	runGit(t, repo, "tag", "v1.2.3.4")

	result, err := RunPushTag(context.Background(), PushTagOptions{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tag != "v1.2.3.4" {
		t.Fatalf("expected v1.2.3.4, got %q", result.Tag)
	}
	if !result.Pushed {
		t.Fatalf("expected tag to be pushed, got %#v", result)
	}
	if result.Target != "origin/v1.2.3.4" {
		t.Fatalf("expected origin/v1.2.3.4 target, got %q", result.Target)
	}
	if got := strings.TrimSpace(gitOutput(t, remote, "tag", "--list", "v1.2.3.4")); got != "v1.2.3.4" {
		t.Fatalf("expected remote tag to exist, got %q", got)
	}
	if got := strings.TrimSpace(gitOutput(t, remote, "tag", "--list", "release")); got != "" {
		t.Fatalf("did not expect non-numeric tag to be pushed, got %q", got)
	}
}

func TestRunPushTagPushesSpecifiedTag(t *testing.T) {
	repo := initGitRepo(t)
	remote := initBareGitRepo(t)
	runGit(t, repo, "remote", "add", "origin", remote)
	writeFile(t, repo, "app.txt", "code\n")
	runGit(t, repo, "add", "app.txt")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "tag", "v1.0.0")
	runGit(t, repo, "tag", "v2.0.0")

	result, err := RunPushTag(context.Background(), PushTagOptions{Repo: repo, Tag: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tag != "v1.0.0" {
		t.Fatalf("expected v1.0.0, got %q", result.Tag)
	}
	if got := strings.TrimSpace(gitOutput(t, remote, "tag", "--list", "v1.0.0")); got != "v1.0.0" {
		t.Fatalf("expected specified remote tag to exist, got %q", got)
	}
	if got := strings.TrimSpace(gitOutput(t, remote, "tag", "--list", "v2.0.0")); got != "" {
		t.Fatalf("did not expect other tag to be pushed, got %q", got)
	}
}

func TestRunPushRecoversGoModConflictAndPushesMerge(t *testing.T) {
	remote := initBareGitRepo(t)
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	runGit(t, repo, "checkout", "-B", "main")
	runGit(t, repo, "remote", "add", "origin", remote)

	writeFile(t, repo, "go.mod", "module example.com/app\n\ngo 1.23\n")
	writeFile(t, repo, "app.go", "package app\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "push", "-u", "origin", "HEAD")

	other := cloneGitRepo(t, remote)
	runGit(t, other, "checkout", "main")
	runGit(t, other, "config", "user.email", "tester@example.com")
	runGit(t, other, "config", "user.name", "Tester")
	runGit(t, other, "config", "commit.gpgsign", "false")

	writeFile(t, repo, "liba/go.mod", "module example.com/liba\n\ngo 1.23\n")
	writeFile(t, repo, "liba/liba.go", "package liba\n\nfunc Name() string { return \"a\" }\n")
	writeFile(t, repo, "a.go", "package app\n\nimport \"example.com/liba\"\n\nfunc A() string { return liba.Name() }\n")
	writeFile(t, repo, "go.mod", `module example.com/app

go 1.23

require example.com/liba v0.0.0

replace example.com/liba => ./liba
`)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "local module")

	writeFile(t, other, "libb/go.mod", "module example.com/libb\n\ngo 1.23\n")
	writeFile(t, other, "libb/libb.go", "package libb\n\nfunc Name() string { return \"b\" }\n")
	writeFile(t, other, "b.go", "package app\n\nimport \"example.com/libb\"\n\nfunc B() string { return libb.Name() }\n")
	writeFile(t, other, "go.mod", `module example.com/app

go 1.23

require example.com/libb v0.0.0

replace example.com/libb => ./libb
`)
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "remote module")
	runGit(t, other, "push")

	writeConfigFile(t, configPath, "provider: command\nproviders:\n  command:\n    type: command\n    command:\n      - "+quoteYAML(writeExecutableScript(t, t.TempDir(), "resolve-go-mod", `#!/bin/sh
cat <<'JSON'
{"canResolve":true,"reason":"merge both local module requirements","files":[{"path":"go.mod","content":"module example.com/app\n\ngo 1.23\n\nrequire (\n\texample.com/liba v0.0.0\n\texample.com/libb v0.0.0\n)\n\nreplace example.com/liba => ./liba\n\nreplace example.com/libb => ./libb\n"}]}
JSON
`))+"\n")

	result, err := RunPush(context.Background(), PushOptions{Repo: repo, ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pushed {
		t.Fatalf("expected push after recovery, got %#v", result)
	}
	if !result.Recovered {
		t.Fatalf("expected recovered push, got %#v", result)
	}
	if result.RecoveryHash == "" {
		t.Fatalf("expected recovery merge hash, got %#v", result)
	}
	if !contains(result.RecoverySteps, "resolved merge conflicts with AI") {
		t.Fatalf("expected AI merge recovery step, got %#v", result.RecoverySteps)
	}
	if !contains(result.RecoverySteps, "ran go mod tidy") {
		t.Fatalf("expected go mod tidy recovery step, got %#v", result.RecoverySteps)
	}
	if !contains(result.RecoverySteps, "verified go build ./...") {
		t.Fatalf("expected go build recovery step, got %#v", result.RecoverySteps)
	}

	goMod := gitOutput(t, remote, "show", "main:go.mod")
	for _, want := range []string{"example.com/liba", "example.com/libb", "example.com/liba => ./liba", "example.com/libb => ./libb"} {
		if !strings.Contains(goMod, want) {
			t.Fatalf("expected remote go.mod to contain %q, got:\n%s", want, goMod)
		}
	}
	if strings.Contains(goMod, "<<<<<<<") {
		t.Fatalf("conflict markers should not remain in go.mod:\n%s", goMod)
	}
	if got := strings.TrimSpace(gitOutput(t, repo, "status", "--porcelain=v1")); got != "" {
		t.Fatalf("expected clean worktree after recovered push, got:\n%s", got)
	}
	if got := strings.TrimSpace(gitOutput(t, remote, "log", "--merges", "--format=%s", "-1", "main")); !strings.HasPrefix(got, "Merge") {
		t.Fatalf("expected merge commit on remote, got %q", got)
	}
}

func TestRunPushUsesAIForNonGoMergeConflict(t *testing.T) {
	remote := initBareGitRepo(t)
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	runGit(t, repo, "checkout", "-B", "main")
	runGit(t, repo, "remote", "add", "origin", remote)

	writeFile(t, repo, "notes.txt", "base\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "push", "-u", "origin", "HEAD")

	other := cloneGitRepo(t, remote)
	runGit(t, other, "checkout", "main")
	runGit(t, other, "config", "user.email", "tester@example.com")
	runGit(t, other, "config", "user.name", "Tester")
	runGit(t, other, "config", "commit.gpgsign", "false")

	writeFile(t, repo, "notes.txt", "base\nlocal\n")
	runGit(t, repo, "add", "notes.txt")
	runGit(t, repo, "commit", "-m", "local notes")

	writeFile(t, other, "notes.txt", "base\nremote\n")
	runGit(t, other, "add", "notes.txt")
	runGit(t, other, "commit", "-m", "remote notes")
	runGit(t, other, "push")

	writeConfigFile(t, configPath, "provider: command\nproviders:\n  command:\n    type: command\n    command:\n      - "+quoteYAML(writeExecutableScript(t, t.TempDir(), "resolve-notes", `#!/bin/sh
cat <<'JSON'
{"canResolve":true,"reason":"keep both note lines","files":[{"path":"notes.txt","content":"base\nlocal\nremote\n"}]}
JSON
`))+"\n")

	result, err := RunPush(context.Background(), PushOptions{Repo: repo, ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pushed || !result.Recovered {
		t.Fatalf("expected AI recovered push, got %#v", result)
	}
	if !contains(result.RecoverySteps, "resolved merge conflicts with AI") {
		t.Fatalf("expected AI merge recovery step, got %#v", result.RecoverySteps)
	}
	if got := gitOutput(t, remote, "show", "main:notes.txt"); got != "base\nlocal\nremote\n" {
		t.Fatalf("unexpected remote notes content %q", got)
	}
}

func TestRunPushUsesBuiltInRepairAgentActions(t *testing.T) {
	remote := initBareGitRepo(t)
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	runGit(t, repo, "checkout", "-B", "main")
	runGit(t, repo, "remote", "add", "origin", remote)

	writeFile(t, repo, "notes.txt", "base\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "push", "-u", "origin", "HEAD")

	other := cloneGitRepo(t, remote)
	runGit(t, other, "checkout", "main")
	runGit(t, other, "config", "user.email", "tester@example.com")
	runGit(t, other, "config", "user.name", "Tester")
	runGit(t, other, "config", "commit.gpgsign", "false")

	writeFile(t, repo, "notes.txt", "base\nlocal\n")
	runGit(t, repo, "add", "notes.txt")
	runGit(t, repo, "commit", "-m", "local notes")

	writeFile(t, other, "notes.txt", "base\nremote\n")
	runGit(t, other, "add", "notes.txt")
	runGit(t, other, "commit", "-m", "remote notes")
	runGit(t, other, "push")

	statePath := filepath.Join(t.TempDir(), "agent-state")
	t.Setenv("AICOMMIT_AGENT_STATE", statePath)
	script := writeExecutableScript(t, t.TempDir(), "repair-agent", `#!/bin/sh
step="$(cat "$AICOMMIT_AGENT_STATE" 2>/dev/null || true)"
case "$step" in
  "")
    echo read > "$AICOMMIT_AGENT_STATE"
    cat <<'JSON'
{"action":"read_file","path":"notes.txt"}
JSON
    ;;
  read)
    echo write > "$AICOMMIT_AGENT_STATE"
    cat <<'JSON'
{"action":"write_file","path":"notes.txt","content":"base\nlocal\nremote\n"}
JSON
    ;;
  *)
    cat <<'JSON'
{"action":"finish","repaired":true,"reason":"merged notes through built-in agent"}
JSON
    ;;
esac
`)
	writeConfigFile(t, configPath, "provider: command\nproviders:\n  command:\n    type: command\n    command:\n      - "+quoteYAML(script)+"\n")

	result, err := RunPush(context.Background(), PushOptions{Repo: repo, ConfigPath: configPath})
	if err != nil {
		state, _ := os.ReadFile(statePath)
		t.Fatalf("%v\nagent state: %q\nstatus:\n%s", err, strings.TrimSpace(string(state)), gitOutput(t, repo, "status", "--porcelain=v1"))
	}
	if !result.Pushed || !result.Recovered {
		t.Fatalf("expected built-in agent recovered push, got %#v", result)
	}
	if !contains(result.RecoverySteps, "resolved merge conflicts with AI") {
		t.Fatalf("expected AI merge recovery step, got %#v", result.RecoverySteps)
	}
	if got := gitOutput(t, remote, "show", "main:notes.txt"); got != "base\nlocal\nremote\n" {
		t.Fatalf("unexpected remote notes content %q", got)
	}
	if got := strings.TrimSpace(readFile(t, statePath)); got != "write" {
		t.Fatalf("expected repair agent to execute read and write actions, got state %q", got)
	}
}

func TestRunPushKeepsDerivedConflictsOutOfAI(t *testing.T) {
	remote := initBareGitRepo(t)
	repo := initGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	promptPath := filepath.Join(t.TempDir(), "prompt.txt")
	runGit(t, repo, "checkout", "-B", "main")
	runGit(t, repo, "remote", "add", "origin", remote)

	writeFile(t, repo, "module.txt", "base\n")
	writeFile(t, repo, "go.sum", "base sum\n")
	writeFile(t, repo, "internal/conf/conf.pb.go", "package conf\n\nconst Source = \"base\"\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "push", "-u", "origin", "HEAD")

	other := cloneGitRepo(t, remote)
	runGit(t, other, "checkout", "main")
	runGit(t, other, "config", "user.email", "tester@example.com")
	runGit(t, other, "config", "user.name", "Tester")
	runGit(t, other, "config", "commit.gpgsign", "false")

	writeFile(t, repo, "module.txt", "base\nlocal\n")
	writeFile(t, repo, "go.sum", "local sum\n")
	writeFile(t, repo, "internal/conf/conf.pb.go", "package conf\n\nconst Source = \"local\"\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "local changes")

	writeFile(t, other, "module.txt", "base\nremote\n")
	writeFile(t, other, "go.sum", "remote sum\n")
	writeFile(t, other, "internal/conf/conf.pb.go", "package conf\n\nconst Source = \"remote\"\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "remote changes")
	runGit(t, other, "push")

	t.Setenv("AICOMMIT_PROMPT_PATH", promptPath)
	writeConfigFile(t, configPath, "provider: command\nproviders:\n  command:\n    type: command\n    command:\n      - "+quoteYAML(writeExecutableScript(t, t.TempDir(), "resolve-filtered", `#!/bin/sh
cat > "$AICOMMIT_PROMPT_PATH"
if grep -q -- '--- FILE go.sum' "$AICOMMIT_PROMPT_PATH" || grep -q -- '--- FILE internal/conf/conf.pb.go' "$AICOMMIT_PROMPT_PATH"; then
  cat <<'JSON'
{"canResolve":false,"reason":"derived file was sent to AI"}
JSON
  exit 0
fi
cat <<'JSON'
{"canResolve":true,"reason":"merge non-derived file only","files":[{"path":"module.txt","content":"base\nlocal\nremote\n"}]}
JSON
`))+"\n")

	result, err := RunPush(context.Background(), PushOptions{Repo: repo, ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pushed || !result.Recovered {
		t.Fatalf("expected recovered push, got %#v", result)
	}
	if !containsPrefix(result.RecoverySteps, "resolved derived conflicts with current branch:") {
		t.Fatalf("expected derived conflict recovery step, got %#v", result.RecoverySteps)
	}
	if got := gitOutput(t, remote, "show", "main:module.txt"); got != "base\nlocal\nremote\n" {
		t.Fatalf("unexpected module.txt content %q", got)
	}
	if got := gitOutput(t, remote, "show", "main:go.sum"); got != "local sum\n" {
		t.Fatalf("expected go.sum from current branch, got %q", got)
	}
	if got := gitOutput(t, remote, "show", "main:internal/conf/conf.pb.go"); got != "package conf\n\nconst Source = \"local\"\n" {
		t.Fatalf("expected pb.go from current branch, got %q", got)
	}
	prompt := readFile(t, promptPath)
	if strings.Contains(prompt, "--- FILE go.sum") || strings.Contains(prompt, "--- FILE internal/conf/conf.pb.go") {
		t.Fatalf("derived files should not be sent to AI prompt:\n%s", prompt)
	}
}

func TestUnknownRevisionModulesParsesGoOutput(t *testing.T) {
	err := goCommandError{
		args: []string{"mod", "tidy"},
		output: `go: git.ikuban.com/server/wxbot-wxprotocol/cmd/server imports
        git.ikuban.com/server/kratos-utils/common: git.ikuban.com/server/kratos-utils@v1.0.1-0.20251208073058-adf7dde65a09: invalid version: unknown revision adf7dde65a09
go: other imports
        git.ikuban.com/server/kratos-utils/log: git.ikuban.com/server/kratos-utils@v1.0.1-0.20251208073058-adf7dde65a09: invalid version: unknown revision adf7dde65a09
`,
		err: errors.New("exit status 1"),
	}
	got := unknownRevisionModules(err)
	want := []string{"git.ikuban.com/server/kratos-utils"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unknownRevisionModules() = %#v, want %#v", got, want)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "tester@example.com")
	runGit(t, repo, "config", "user.name", "Tester")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	return repo
}

func initBareGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "-C", repo, "init", "--bare", "-q")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init --bare failed: %v\n%s", err, string(out))
	}
	return repo
}

func cloneGitRepo(t *testing.T, remote string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	cmd := exec.Command("git", "clone", "-q", remote, repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone failed: %v\n%s", err, string(out))
	}
	return repo
}

func writeFile(t *testing.T, repo, rel, contents string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeConfigFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeBytes(t *testing.T, repo, rel string, contents []byte) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeExecutableScript(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	_ = gitOutput(t, repo, args...)
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

func containsDecision(items []filter.Decision, path, reason string) bool {
	for _, item := range items {
		if item.Path == path && item.Reason == reason {
			return true
		}
	}
	return false
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func containsPrefix(items []string, prefix string) bool {
	for _, item := range items {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func quoteYAML(value string) string {
	return strconv.Quote(value)
}
