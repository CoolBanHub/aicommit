package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const addedByAicommitHeader = "# Added by aicommit after detecting protected files"

type commandError struct {
	args   []string
	output string
	err    error
}

func (e commandError) Error() string {
	out := strings.TrimSpace(e.output)
	if out == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.args, " "), e.err)
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.args, " "), e.err, out)
}

func Root(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func EnsureRepo(ctx context.Context, dir string) (string, bool, error) {
	if root, err := Root(ctx, dir); err == nil {
		return root, false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	if _, err := runRaw(ctx, dir, "init"); err != nil {
		return "", false, err
	}
	root, err := Root(ctx, dir)
	if err != nil {
		return "", false, err
	}
	return root, true, nil
}

func EnsureGitignore(repo string, patterns []string) (bool, error) {
	path := filepath.Join(repo, ".gitignore")
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return false, fmt.Errorf(".gitignore path is a directory")
		}
		return false, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	var builder strings.Builder
	builder.WriteString("# Created by aicommit\n")
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		builder.WriteString(pattern)
		builder.WriteString("\n")
	}
	return true, os.WriteFile(path, []byte(builder.String()), 0o644)
}

func AppendGitignorePatterns(repo string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return false, nil
	}
	path := filepath.Join(repo, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	existing := map[string]struct{}{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		existing[line] = struct{}{}
	}

	var toAppend []string
	seenNew := map[string]struct{}{}
	for _, pattern := range patterns {
		pattern = detectedGitignorePattern(pattern)
		if pattern == "" {
			continue
		}
		if _, ok := existing[pattern]; ok {
			continue
		}
		if _, ok := seenNew[pattern]; ok {
			continue
		}
		seenNew[pattern] = struct{}{}
		toAppend = append(toAppend, pattern)
	}
	if len(toAppend) == 0 {
		return false, nil
	}

	var builder strings.Builder
	if len(data) > 0 {
		builder.Write(data)
		if data[len(data)-1] != '\n' {
			builder.WriteString("\n")
		}
	}
	builder.WriteString("\n")
	builder.WriteString(addedByAicommitHeader)
	builder.WriteString("\n")
	for _, pattern := range toAppend {
		builder.WriteString(pattern)
		builder.WriteString("\n")
	}
	return true, os.WriteFile(path, []byte(builder.String()), 0o644)
}

func RepairAicommitGitignore(repo string) (bool, error) {
	path := filepath.Join(repo, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	lines := strings.Split(string(data), "\n")
	inDetectedSection := false
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == addedByAicommitHeader {
			inDetectedSection = true
			continue
		}
		if !inDetectedSection || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		normalized := detectedGitignorePattern(line)
		if normalized != line {
			lines[i] = normalized
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func detectedGitignorePattern(pattern string) string {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	pattern = strings.TrimPrefix(pattern, "./")
	if pattern == "" || strings.HasPrefix(pattern, "/") {
		return pattern
	}
	return "/" + pattern
}

func StatusPaths(ctx context.Context, repo string) ([]string, error) {
	out, err := runBytes(ctx, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	return parsePorcelainZ(out), nil
}

func StagedPaths(ctx context.Context, repo string) ([]string, error) {
	out, err := runBytes(ctx, repo, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return nil, err
	}
	return splitZ(out), nil
}

func IgnoredPaths(ctx context.Context, repo string, paths []string) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	if len(paths) == 0 {
		return result, nil
	}
	for _, batch := range batches(paths, 100) {
		input := joinZ(batch)
		out, err := runBytesInputAllowExit(ctx, repo, input, []int{1}, "check-ignore", "-z", "--no-index", "--stdin")
		if err != nil {
			return nil, err
		}
		for _, path := range splitZ(out) {
			result[path] = struct{}{}
		}
	}
	return result, nil
}

func StagePaths(ctx context.Context, repo string, paths []string) error {
	for _, batch := range batches(paths, 100) {
		args := append([]string{"add", "-A", "--"}, batch...)
		if _, err := run(ctx, repo, args...); err != nil {
			return err
		}
	}
	return nil
}

func UnstagePath(ctx context.Context, repo, path string) error {
	if _, err := run(ctx, repo, "restore", "--staged", "--", path); err == nil {
		return nil
	}
	if _, err := run(ctx, repo, "rm", "--cached", "-r", "--ignore-unmatch", "--", path); err == nil {
		return nil
	}
	_, err := run(ctx, repo, "reset", "-q", "HEAD", "--", path)
	return err
}

func HasCachedChanges(ctx context.Context, repo string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "diff", "--cached", "--quiet", "--exit-code")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, commandError{args: []string{"diff", "--cached", "--quiet", "--exit-code"}, output: string(out), err: err}
}

func CachedStat(ctx context.Context, repo string) (string, error) {
	return run(ctx, repo, "diff", "--cached", "--stat", "--no-ext-diff", "--no-color")
}

func CachedDiff(ctx context.Context, repo string, maxChars int) (string, bool, error) {
	out, err := run(ctx, repo, "diff", "--cached", "--patch", "--no-ext-diff", "--no-color")
	if err != nil {
		return "", false, err
	}
	if maxChars <= 0 || len(out) <= maxChars {
		return out, false, nil
	}
	return out[:maxChars] + "\n\n[diff truncated]\n", true, nil
}

func Commit(ctx context.Context, repo, message string, disableGPGSign bool) error {
	args := []string{"commit"}
	if disableGPGSign {
		args = append([]string{"-c", "commit.gpgsign=false"}, args...)
	}
	args = append(args, "-m", message)
	_, err := run(ctx, repo, args...)
	return err
}

func CommitHash(ctx context.Context, repo string) (string, error) {
	out, err := run(ctx, repo, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func HasRemote(ctx context.Context, repo string) (bool, error) {
	out, err := run(ctx, repo, "remote")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func Push(ctx context.Context, repo string) (bool, string, string, error) {
	branch, err := currentBranch(ctx, repo)
	if err != nil {
		return false, "", "", err
	}
	if branch == "" {
		return false, "", "detached HEAD", nil
	}
	if upstream, err := run(ctx, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil && strings.TrimSpace(upstream) != "" {
		target := strings.TrimSpace(upstream)
		if _, err := run(ctx, repo, "push"); err != nil {
			return false, "", "", err
		}
		return true, target, "", nil
	}
	remotes, err := remoteList(ctx, repo)
	if err != nil {
		return false, "", "", err
	}
	remote := chooseRemote(remotes)
	if remote == "" {
		return false, "", "no remote", nil
	}
	if _, err := run(ctx, repo, "push", "-u", remote, branch); err != nil {
		return false, "", "", err
	}
	return true, remote + "/" + branch, "", nil
}

func currentBranch(ctx context.Context, repo string) (string, error) {
	out, err := run(ctx, repo, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func remoteList(ctx context.Context, repo string) ([]string, error) {
	out, err := run(ctx, repo, "remote")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var remotes []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			remotes = append(remotes, line)
		}
	}
	return remotes, nil
}

func chooseRemote(remotes []string) string {
	for _, remote := range remotes {
		if remote == "origin" {
			return remote
		}
	}
	if len(remotes) > 0 {
		return remotes[0]
	}
	return ""
}

func run(ctx context.Context, repo string, args ...string) (string, error) {
	out, err := runBytes(ctx, repo, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func runBytes(ctx context.Context, repo string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, commandError{args: args, output: string(out), err: err}
	}
	return out, nil
}

func runBytesAllowExit(ctx context.Context, repo string, allowedExitCodes []int, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return out, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		for _, code := range allowedExitCodes {
			if exitErr.ExitCode() == code {
				return out, nil
			}
		}
	}
	return nil, commandError{args: args, output: string(out), err: err}
}

func runBytesInputAllowExit(ctx context.Context, repo string, input []byte, allowedExitCodes []int, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return out, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		for _, code := range allowedExitCodes {
			if exitErr.ExitCode() == code {
				return out, nil
			}
		}
	}
	return nil, commandError{args: args, output: string(out), err: err}
}

func runRaw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, commandError{args: args, output: string(out), err: err}
	}
	return out, nil
}

func parsePorcelainZ(out []byte) []string {
	entries := bytes.Split(out, []byte{0})
	var paths []string
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		x := entry[0]
		y := entry[1]
		path := string(entry[3:])
		if path != "" {
			paths = append(paths, path)
		}
		if x == 'R' || y == 'R' || x == 'C' || y == 'C' {
			i++
		}
	}
	return paths
}

func splitZ(out []byte) []string {
	parts := bytes.Split(out, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			paths = append(paths, string(part))
		}
	}
	return paths
}

func joinZ(paths []string) []byte {
	var buf bytes.Buffer
	for _, path := range paths {
		buf.WriteString(path)
		buf.WriteByte(0)
	}
	return buf.Bytes()
}

func batches(paths []string, size int) [][]string {
	if size <= 0 || len(paths) <= size {
		return [][]string{paths}
	}
	var out [][]string
	for len(paths) > 0 {
		n := size
		if len(paths) < n {
			n = len(paths)
		}
		out = append(out, paths[:n])
		paths = paths[n:]
	}
	return out
}
