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

const mergeResolutionSchema = `{
  "type": "object",
  "properties": {
    "canResolve": {
      "type": "boolean"
    },
    "reason": {
      "type": "string"
    },
    "files": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "path": {
            "type": "string"
          },
          "content": {
            "type": "string"
          },
          "delete": {
            "type": "boolean"
          }
        },
        "required": ["path"],
        "additionalProperties": false
      }
    }
  },
  "required": ["canResolve"],
  "additionalProperties": false
}`

const workspaceRepairSchema = `{
  "type": "object",
  "properties": {
    "repaired": {
      "type": "boolean"
    },
    "reason": {
      "type": "string"
    }
  },
  "required": ["repaired"],
  "additionalProperties": false
}`

const repairAgentActionSchema = `{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["read_file", "write_file", "delete_file", "list_files", "run_command", "finish"]
    },
    "path": {
      "type": "string"
    },
    "content": {
      "type": "string"
    },
    "command": {
      "type": "string"
    },
    "args": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "repaired": {
      "type": "boolean"
    },
    "reason": {
      "type": "string"
    }
  },
  "required": ["action"],
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

func (p *CodexCLIProvider) GenerateMergeResolution(ctx context.Context, req MergeResolutionRequest) (MergeResolutionResult, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return MergeResolutionResult{}, err
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "aicommit-codex-merge-*")
	if err != nil {
		return MergeResolutionResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	schemaPath := filepath.Join(tmpDir, "schema.json")
	outputPath := filepath.Join(tmpDir, "merge-resolution.json")
	if err := os.WriteFile(schemaPath, []byte(mergeResolutionSchema), 0o600); err != nil {
		return MergeResolutionResult{}, err
	}

	args := []string{"exec", "--cd", req.RepoRoot, "--sandbox", "read-only", "--ephemeral", "--output-schema", schemaPath, "-o", outputPath}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	args = append(args, "-")

	out, err := runCommandWithInput(ctx, req.RepoRoot, "codex", args, BuildMergeResolutionPrompt(req))
	if err != nil {
		return MergeResolutionResult{}, err
	}
	if data, readErr := os.ReadFile(outputPath); readErr == nil && len(bytes.TrimSpace(data)) > 0 {
		return ExtractMergeResolution(string(data))
	}
	return ExtractMergeResolution(out)
}

func (p *CodexCLIProvider) RepairWorkspace(ctx context.Context, req WorkspaceRepairRequest) (WorkspaceRepairResult, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return WorkspaceRepairResult{}, err
	}
	timeout := repairTimeoutSeconds(p.TimeoutSeconds)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "aicommit-codex-repair-*")
	if err != nil {
		return WorkspaceRepairResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	schemaPath := filepath.Join(tmpDir, "schema.json")
	outputPath := filepath.Join(tmpDir, "workspace-repair.json")
	if err := os.WriteFile(schemaPath, []byte(workspaceRepairSchema), 0o600); err != nil {
		return WorkspaceRepairResult{}, err
	}

	args := []string{"exec", "--cd", req.RepoRoot, "--sandbox", "workspace-write", "--ephemeral", "--output-schema", schemaPath, "-o", outputPath}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	args = append(args, "-")

	out, err := runCommandWithInput(ctx, req.RepoRoot, "codex", args, BuildWorkspaceRepairPrompt(req))
	if err != nil {
		return WorkspaceRepairResult{}, err
	}
	if data, readErr := os.ReadFile(outputPath); readErr == nil && len(bytes.TrimSpace(data)) > 0 {
		return ExtractWorkspaceRepairResult(string(data))
	}
	return ExtractWorkspaceRepairResult(out)
}

func (p *CodexCLIProvider) GenerateRepairAction(ctx context.Context, req RepairAgentRequest) (RepairAgentAction, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return RepairAgentAction{}, err
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "aicommit-codex-action-*")
	if err != nil {
		return RepairAgentAction{}, err
	}
	defer os.RemoveAll(tmpDir)

	schemaPath := filepath.Join(tmpDir, "schema.json")
	outputPath := filepath.Join(tmpDir, "repair-action.json")
	if err := os.WriteFile(schemaPath, []byte(repairAgentActionSchema), 0o600); err != nil {
		return RepairAgentAction{}, err
	}

	args := []string{"exec", "--cd", req.RepoRoot, "--sandbox", "read-only", "--ephemeral", "--output-schema", schemaPath, "-o", outputPath}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	args = append(args, "-")

	out, err := runCommandWithInput(ctx, req.RepoRoot, "codex", args, BuildRepairAgentPrompt(req))
	if err != nil {
		return RepairAgentAction{}, err
	}
	if data, readErr := os.ReadFile(outputPath); readErr == nil && len(bytes.TrimSpace(data)) > 0 {
		return ExtractRepairAgentAction(string(data))
	}
	return ExtractRepairAgentAction(out)
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

func (p *ClaudeCodeProvider) GenerateMergeResolution(ctx context.Context, req MergeResolutionRequest) (MergeResolutionResult, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return MergeResolutionResult{}, err
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	args := []string{"--print", "--output-format", "json", "--no-session-persistence", "--permission-mode", "dontAsk", "--tools", "", "--json-schema", compactJSON(mergeResolutionSchema)}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}

	out, err := runCommandWithInput(ctx, req.RepoRoot, "claude", args, BuildMergeResolutionPrompt(req))
	if err != nil {
		return MergeResolutionResult{}, err
	}
	if msg, ok := parseWrappedResult(out); ok {
		return ExtractMergeResolution(msg)
	}
	return ExtractMergeResolution(out)
}

func (p *ClaudeCodeProvider) RepairWorkspace(ctx context.Context, req WorkspaceRepairRequest) (WorkspaceRepairResult, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return WorkspaceRepairResult{}, err
	}
	timeout := repairTimeoutSeconds(p.TimeoutSeconds)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	args := []string{
		"--print",
		"--output-format", "json",
		"--no-session-persistence",
		"--permission-mode", "acceptEdits",
		"--tools", "default",
		"--json-schema", compactJSON(workspaceRepairSchema),
	}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}

	out, err := runCommandWithInput(ctx, req.RepoRoot, "claude", args, BuildWorkspaceRepairPrompt(req))
	if err != nil {
		return WorkspaceRepairResult{}, err
	}
	if msg, ok := parseWrappedResult(out); ok {
		return ExtractWorkspaceRepairResult(msg)
	}
	return ExtractWorkspaceRepairResult(out)
}

func (p *ClaudeCodeProvider) GenerateRepairAction(ctx context.Context, req RepairAgentRequest) (RepairAgentAction, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return RepairAgentAction{}, err
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	args := []string{"--print", "--output-format", "json", "--no-session-persistence", "--permission-mode", "dontAsk", "--tools", "", "--json-schema", compactJSON(repairAgentActionSchema)}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}

	out, err := runCommandWithInput(ctx, req.RepoRoot, "claude", args, BuildRepairAgentPrompt(req))
	if err != nil {
		return RepairAgentAction{}, err
	}
	return extractRepairAgentActionOutput(out)
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

func (p *CommandProvider) GenerateMergeResolution(ctx context.Context, req MergeResolutionRequest) (MergeResolutionResult, error) {
	if len(p.Command) == 0 {
		return MergeResolutionResult{}, fmt.Errorf("command provider has no command")
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	prompt := BuildMergeResolutionPrompt(req)
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
		return MergeResolutionResult{}, err
	}
	if msg, ok := parseWrappedResult(out); ok {
		return ExtractMergeResolution(msg)
	}
	return ExtractMergeResolution(out)
}

func (p *CommandProvider) RepairWorkspace(ctx context.Context, req WorkspaceRepairRequest) (WorkspaceRepairResult, error) {
	if len(p.Command) == 0 {
		return WorkspaceRepairResult{}, fmt.Errorf("command provider has no command")
	}
	timeout := repairTimeoutSeconds(p.TimeoutSeconds)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	prompt := BuildWorkspaceRepairPrompt(req)
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
		return WorkspaceRepairResult{}, err
	}
	if msg, ok := parseWrappedResult(out); ok {
		return ExtractWorkspaceRepairResult(msg)
	}
	return ExtractWorkspaceRepairResult(out)
}

func (p *CommandProvider) GenerateRepairAction(ctx context.Context, req RepairAgentRequest) (RepairAgentAction, error) {
	if len(p.Command) == 0 {
		return RepairAgentAction{}, fmt.Errorf("command provider has no command")
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	prompt := BuildRepairAgentPrompt(req)
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
		return RepairAgentAction{}, err
	}
	return extractRepairAgentActionOutput(out)
}

func repairTimeoutSeconds(timeout int) int {
	if timeout < 300 {
		return 300
	}
	return timeout
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
		return "", fmt.Errorf("%s failed: %w: %s", name, err, commandFailureDetails(stdout.String(), stderr.String()))
	}
	if stdout.Len() == 0 && stderr.Len() > 0 {
		return stderr.String(), nil
	}
	return stdout.String(), nil
}

func commandFailureDetails(stdout, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	stdout = strings.TrimSpace(stdout)
	if parsed := structuredErrorMessage(stdout); parsed != "" {
		stdout = parsed
	}
	if stderr != "" && stdout != "" {
		return truncateCommandOutput(stderr + "\n" + stdout)
	}
	if stderr != "" {
		return truncateCommandOutput(stderr)
	}
	if stdout != "" {
		return truncateCommandOutput(stdout)
	}
	return "no error output"
}

func structuredErrorMessage(text string) string {
	if text == "" {
		return ""
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(text), &generic); err != nil {
		return ""
	}
	if value, ok := generic["error"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if nested, ok := generic["error"].(map[string]any); ok {
		for _, key := range []string{"message", "detail", "type"} {
			if value, ok := nested[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	for _, key := range []string{"result", "message", "content", "text"} {
		if value, ok := generic[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truncateCommandOutput(text string) string {
	const maxErrorOutput = 4000
	if len(text) <= maxErrorOutput {
		return text
	}
	return strings.TrimSpace(text[:maxErrorOutput]) + "... (truncated)"
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

func extractRepairAgentActionOutput(out string) (RepairAgentAction, error) {
	action, err := ExtractRepairAgentAction(out)
	if err == nil {
		return action, nil
	}
	if msg, ok := parseWrappedResult(out); ok {
		return ExtractRepairAgentAction(msg)
	}
	return RepairAgentAction{}, err
}
