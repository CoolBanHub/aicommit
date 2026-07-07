package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
