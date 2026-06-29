package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type CodexCLIProvider struct {
	Model          string
	TimeoutSeconds int
}

type ClaudeCodeProvider struct {
	Model          string
	TimeoutSeconds int
}

type CommandProvider struct {
	Command        []string
	Model          string
	TimeoutSeconds int
}

const messageSchema = `{
  "type": "object",
  "properties": {
    "message": {
      "type": "string",
      "minLength": 1,
      "maxLength": 120
    }
  },
  "required": ["message"],
  "additionalProperties": false
}`

func (p *CodexCLIProvider) GenerateCommitMessage(ctx context.Context, req CommitRequest) (string, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return "", err
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "aicommit-codex-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	schemaPath := filepath.Join(tmpDir, "schema.json")
	outputPath := filepath.Join(tmpDir, "message.json")
	if err := os.WriteFile(schemaPath, []byte(messageSchema), 0o600); err != nil {
		return "", err
	}

	args := []string{"exec", "--cd", req.RepoRoot, "--sandbox", "read-only", "--ephemeral", "--output-schema", schemaPath, "-o", outputPath}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	args = append(args, "-")

	out, err := runCommandWithInput(ctx, req.RepoRoot, "codex", args, BuildPrompt(req))
	if err != nil {
		return "", err
	}
	if data, readErr := os.ReadFile(outputPath); readErr == nil && len(bytes.TrimSpace(data)) > 0 {
		return ExtractCommitMessage(string(data))
	}
	return ExtractCommitMessage(out)
}

func (p *ClaudeCodeProvider) GenerateCommitMessage(ctx context.Context, req CommitRequest) (string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return "", err
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	args := []string{"--print", "--output-format", "json", "--no-session-persistence", "--permission-mode", "dontAsk", "--tools", "", "--json-schema", compactJSON(messageSchema)}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}

	out, err := runCommandWithInput(ctx, req.RepoRoot, "claude", args, BuildPrompt(req))
	if err != nil {
		return "", err
	}
	if msg, ok := parseWrappedResult(out); ok {
		return ExtractCommitMessage(msg)
	}
	return ExtractCommitMessage(out)
}

func (p *CommandProvider) GenerateCommitMessage(ctx context.Context, req CommitRequest) (string, error) {
	if len(p.Command) == 0 {
		return "", fmt.Errorf("command provider has no command")
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	prompt := BuildPrompt(req)
	args := make([]string, len(p.Command))
	promptInArgs := false
	for i, part := range p.Command {
		part = strings.ReplaceAll(part, "{repo}", req.RepoRoot)
		part = strings.ReplaceAll(part, "{model}", p.Model)
		if strings.Contains(part, "{prompt}") {
			promptInArgs = true
			part = strings.ReplaceAll(part, "{prompt}", prompt)
		}
		args[i] = part
	}
	stdin := prompt
	if promptInArgs {
		stdin = ""
	}
	out, err := runCommandWithInput(ctx, req.RepoRoot, args[0], args[1:], stdin)
	if err != nil {
		return "", err
	}
	if msg, ok := parseWrappedResult(out); ok {
		return ExtractCommitMessage(msg)
	}
	return ExtractCommitMessage(out)
}

func runCommandWithInput(ctx context.Context, dir, name string, args []string, input string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 && stderr.Len() > 0 {
		return stderr.String(), nil
	}
	return stdout.String(), nil
}

func compactJSON(text string) string {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return text
	}
	data, err := json.Marshal(v)
	if err != nil {
		return text
	}
	return string(data)
}

func parseWrappedResult(out string) (string, bool) {
	var generic map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &generic); err != nil {
		return "", false
	}
	for _, key := range []string{"message", "result", "content", "text"} {
		if value, ok := generic[key].(string); ok && strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	if nested, ok := generic["result"].(map[string]any); ok {
		if value, ok := nested["message"].(string); ok {
			return value, true
		}
	}
	return "", false
}
