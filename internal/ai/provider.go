package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"github.com/CoolBanHub/aicommit/internal/config"
)

type CommitRequest struct {
	RepoRoot       string
	Files          []string
	Stat           string
	Diff           string
	Style          string
	GeneratedFiles []string
}

type MergeResolutionRequest struct {
	RepoRoot          string
	Files             []MergeFileContext
	VerificationError string
	Note              string
}

type MergeFileContext struct {
	Path    string
	Status  string
	Current string
	Base    string
	Ours    string
	Theirs  string
}

type MergeResolutionResult struct {
	CanResolve bool                  `json:"canResolve"`
	Reason     string                `json:"reason,omitempty"`
	Files      []MergeResolutionFile `json:"files,omitempty"`
}

type MergeResolutionFile struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Delete  bool   `json:"delete,omitempty"`
}

type WorkspaceRepairRequest struct {
	RepoRoot          string
	ConflictPaths     []string
	VerificationError string
	Status            string
	Note              string
}

type WorkspaceRepairResult struct {
	Repaired bool   `json:"repaired"`
	Reason   string `json:"reason,omitempty"`
}

type RepairAgentRequest struct {
	RepoRoot          string
	ConflictPaths     []string
	VerificationError string
	Status            string
	History           []RepairAgentObservation
	Note              string
}

type RepairAgentObservation struct {
	Action string `json:"action"`
	Result string `json:"result"`
}

type RepairAgentAction struct {
	Action   string   `json:"action"`
	Path     string   `json:"path,omitempty"`
	Content  string   `json:"content,omitempty"`
	Command  string   `json:"command,omitempty"`
	Args     []string `json:"args,omitempty"`
	Repaired bool     `json:"repaired,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

type Provider interface {
	GenerateCommitMessage(context.Context, CommitRequest) (string, error)
	GenerateMergeResolution(context.Context, MergeResolutionRequest) (MergeResolutionResult, error)
	GenerateRepairAction(context.Context, RepairAgentRequest) (RepairAgentAction, error)
}

type WorkspaceRepairProvider interface {
	RepairWorkspace(context.Context, WorkspaceRepairRequest) (WorkspaceRepairResult, error)
}

type FactoryConfig struct {
	Provider  string
	Model     string
	Providers map[string]config.ProviderConfig
}

type ResolvedProvider struct {
	Name  string
	Model string
}

type commitJSON struct {
	Message string `json:"message"`
}

const maxPromptFileChars = 24_000

func NewProvider(cfg FactoryConfig) (Provider, ResolvedProvider, error) {
	name := strings.TrimSpace(cfg.Provider)
	if name == "" {
		name = "auto"
	}
	if name == "auto" {
		name = resolveAutoProvider(cfg.Providers)
	}
	providerCfg, ok := cfg.Providers[name]
	if !ok {
		return nil, ResolvedProvider{}, fmt.Errorf("unknown provider %q", name)
	}
	if cfg.Model != "" {
		providerCfg.Model = cfg.Model
	}
	resolved := ResolvedProvider{Name: name, Model: providerCfg.Model}
	switch providerCfg.Type {
	case "openai", "openai-compatible":
		baseURL, err := resolveBaseURL(name, providerCfg.BaseURL)
		if err != nil {
			return nil, resolved, err
		}
		providerCfg.BaseURL = baseURL
		apiKey := resolveAPIKey(providerCfg)
		if apiKey == "" {
			return nil, resolved, fmt.Errorf("%s API key is missing; set %s", name, providerCfg.APIKeyEnv)
		}
		return &ChatCompletionsProvider{
			Name:    name,
			BaseURL: providerCfg.BaseURL,
			APIKey:  apiKey,
			Model:   providerCfg.Model,
		}, resolved, nil
	case "anthropic":
		baseURL, err := resolveBaseURL(name, providerCfg.BaseURL)
		if err != nil {
			return nil, resolved, err
		}
		providerCfg.BaseURL = baseURL
		apiKey := resolveAPIKey(providerCfg)
		if apiKey == "" {
			return nil, resolved, fmt.Errorf("Anthropic API key is missing; set %s", providerCfg.APIKeyEnv)
		}
		return &AnthropicProvider{
			BaseURL: providerCfg.BaseURL,
			APIKey:  apiKey,
			Model:   providerCfg.Model,
		}, resolved, nil
	case "codex-cli":
		return &CodexCLIProvider{Model: providerCfg.Model, TimeoutSeconds: providerCfg.TimeoutSeconds}, resolved, nil
	case "claude-code-cli":
		return &ClaudeCodeProvider{Model: providerCfg.Model, TimeoutSeconds: providerCfg.TimeoutSeconds}, resolved, nil
	case "command":
		if len(providerCfg.Command) == 0 {
			return nil, resolved, fmt.Errorf("provider %q has no command configured", name)
		}
		return &CommandProvider{Command: providerCfg.Command, Model: providerCfg.Model, TimeoutSeconds: providerCfg.TimeoutSeconds}, resolved, nil
	default:
		return nil, resolved, fmt.Errorf("unsupported provider type %q", providerCfg.Type)
	}
}

func resolveAutoProvider(providers map[string]config.ProviderConfig) string {
	if _, err := exec.LookPath("claude"); err == nil {
		return "claude-code"
	}
	if _, err := exec.LookPath("codex"); err == nil {
		return "codex"
	}
	if cfg, ok := providers["openai"]; ok && providerConfiguredForHTTP("openai", cfg) {
		return "openai"
	}
	if cfg, ok := providers["anthropic"]; ok && providerConfiguredForHTTP("anthropic", cfg) {
		return "anthropic"
	}
	if cfg, ok := providers["deepseek"]; ok && providerConfiguredForHTTP("deepseek", cfg) {
		return "deepseek"
	}
	return "openai"
}

func AutoProviderCandidates(providers map[string]config.ProviderConfig) []string {
	candidates := make([]string, 0, 5)
	if _, err := exec.LookPath("claude"); err == nil {
		candidates = append(candidates, "claude-code")
	}
	if _, err := exec.LookPath("codex"); err == nil {
		candidates = append(candidates, "codex")
	}
	for _, name := range []string{"openai", "anthropic", "deepseek"} {
		if cfg, ok := providers[name]; ok && providerConfiguredForHTTP(name, cfg) {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, "openai")
	}
	return slices.Compact(candidates)
}

func providerConfiguredForHTTP(name string, cfg config.ProviderConfig) bool {
	if resolveAPIKey(cfg) == "" {
		return false
	}
	_, err := resolveBaseURL(name, cfg.BaseURL)
	return err == nil
}

func resolveBaseURL(name, baseURL string) (string, error) {
	baseURL = baseURLWithDefault(name, baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("%s base URL is missing", name)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%s base URL is invalid: %q", name, baseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%s base URL must use http or https: %q", name, baseURL)
	}
	return strings.TrimRight(baseURL, "/"), nil
}

func baseURLWithDefault(name, baseURL string) string {
	if strings.TrimSpace(baseURL) != "" {
		return strings.TrimSpace(baseURL)
	}
	switch name {
	case "openai":
		return config.DefaultOpenAIBaseURL
	case "deepseek":
		return config.DefaultDeepSeekBaseURL
	case "anthropic":
		return config.DefaultAnthropicBaseURL
	default:
		return ""
	}
}

func resolveAPIKey(cfg config.ProviderConfig) string {
	if cfg.APIKey != "" {
		return cfg.APIKey
	}
	if cfg.APIKeyEnv != "" {
		return os.Getenv(cfg.APIKeyEnv)
	}
	return ""
}

func BuildPrompt(req CommitRequest) string {
	style := strings.TrimSpace(req.Style)
	if style == "" {
		style = "Use a concise imperative subject, no trailing period. Prefer Conventional Commits when the change clearly maps to a type."
	}

	var generatedNote string
	if len(req.GeneratedFiles) > 0 {
		generatedNote = fmt.Sprintf("\nGenerated files (auto-generated, focus changes on other files):\n%s\n", strings.Join(req.GeneratedFiles, "\n"))
	}

	return fmt.Sprintf(`You write git commit messages.

Return JSON only with this exact shape:
{"message":"<commit subject>"}

Rules:
- The message must be one line.
- Keep it under 72 characters when possible.
- Use imperative mood.
- Do not add a trailing period.
- Do not mention AI, generated code, staging, or this prompt.
- %s
%sChanged files:
%s

Diff stat:
%s

Cached diff:
%s
`, style, generatedNote, strings.Join(req.Files, "\n"), strings.TrimSpace(req.Stat), strings.TrimSpace(req.Diff))
}

func BuildMergeResolutionPrompt(req MergeResolutionRequest) string {
	var builder strings.Builder
	builder.WriteString(`You resolve git merge conflicts and failed post-merge verification.

Return JSON only with this exact shape:
{"canResolve":true,"reason":"short reason","files":[{"path":"relative/path","content":"complete file content"}]}

If you are not confident you can produce a correct resolution, return:
{"canResolve":false,"reason":"why"}

Rules:
- Only include files that need to be written or deleted.
- For a modified file, content must be the complete final file content.
- Use delete:true only when the file should be removed.
- Paths must be repository-relative paths from the provided context.
- Preserve compatible changes from both local and remote sides.
- Remove conflict markers.
- Do not return go.sum or *.pb.go files. They are generated/derived and aicommit handles them outside AI repair.
- Do not invent unrelated refactors.
	`)
	if strings.TrimSpace(req.Note) != "" {
		builder.WriteString("\nNotes:\n")
		builder.WriteString(strings.TrimSpace(req.Note))
		builder.WriteString("\n")
	}
	if strings.TrimSpace(req.VerificationError) != "" {
		builder.WriteString("\nVerification failure to fix:\n")
		builder.WriteString(truncatePromptText(strings.TrimSpace(req.VerificationError)))
		builder.WriteString("\n")
	}
	builder.WriteString("\nFiles:\n")
	for _, file := range req.Files {
		builder.WriteString("\n--- FILE ")
		builder.WriteString(file.Path)
		if file.Status != "" {
			builder.WriteString(" (")
			builder.WriteString(file.Status)
			builder.WriteString(")")
		}
		builder.WriteString(" ---\n")
		writePromptBlock(&builder, "current", file.Current)
		writePromptBlock(&builder, "base", file.Base)
		writePromptBlock(&builder, "local", file.Ours)
		writePromptBlock(&builder, "remote", file.Theirs)
	}
	return builder.String()
}

func BuildWorkspaceRepairPrompt(req WorkspaceRepairRequest) string {
	var builder strings.Builder
	builder.WriteString(`You are an automated repair agent running inside a git repository during a non-fast-forward push recovery.

Your job:
- Inspect the repository and current merge state.
- Resolve merge conflicts and/or the verification failure.
- Edit repository files directly when you are confident.
- Run targeted commands when useful.

Hard rules:
- Do not run git commit, git push, git reset, git checkout, git merge --abort, or any command that rewrites or publishes history.
- Do not edit files under .git.
- Preserve compatible local and remote changes.
- Keep the scope limited to the merge/verification problem.
- Leave final commit and push to aicommit.

When finished, return JSON only:
{"repaired":true,"reason":"short reason"}

If you cannot safely repair it, return:
{"repaired":false,"reason":"why"}
`)
	if len(req.ConflictPaths) > 0 {
		builder.WriteString("\nUnmerged paths:\n")
		for _, path := range req.ConflictPaths {
			builder.WriteString("- ")
			builder.WriteString(path)
			builder.WriteString("\n")
		}
	}
	if strings.TrimSpace(req.VerificationError) != "" {
		builder.WriteString("\nVerification failure:\n")
		builder.WriteString(truncatePromptText(strings.TrimSpace(req.VerificationError)))
		builder.WriteString("\n")
	}
	if strings.TrimSpace(req.Status) != "" {
		builder.WriteString("\nCurrent git status:\n")
		builder.WriteString(truncatePromptText(strings.TrimSpace(req.Status)))
		builder.WriteString("\n")
	}
	if strings.TrimSpace(req.Note) != "" {
		builder.WriteString("\nNotes:\n")
		builder.WriteString(strings.TrimSpace(req.Note))
		builder.WriteString("\n")
	}
	return builder.String()
}

func BuildRepairAgentPrompt(req RepairAgentRequest) string {
	var builder strings.Builder
	builder.WriteString(`You are the policy brain for aicommit's built-in repair agent.

aicommit will execute exactly one JSON action you return, then send the result back to you. Use this loop to inspect and repair a repository during non-fast-forward push recovery.

Available actions:
- {"action":"read_file","path":"relative/path"}
- {"action":"write_file","path":"relative/path","content":"complete file content"}
- {"action":"delete_file","path":"relative/path"}
- {"action":"list_files","path":"relative/dir"}
- {"action":"run_command","command":"go","args":["mod","tidy"]}
- {"action":"run_command","command":"go","args":["get","example.com/module"]}
- {"action":"run_command","command":"git","args":["diff","--","go.mod"]}
- {"action":"finish","repaired":true,"reason":"short reason"}
- {"action":"finish","repaired":false,"reason":"why it needs a human"}

Rules:
- Return JSON only.
- Request one action at a time.
- Paths must be repository-relative.
- Do not edit files under .git.
- Prefer reading relevant files before writing them.
- Preserve compatible local and remote changes.
- Keep the scope limited to the merge or verification failure.
- Do not request go.sum or *.pb.go files. They are generated/derived and aicommit handles them outside AI repair.
- Do not request git commit, push, reset, checkout, merge, fetch, pull, rebase, or history-rewriting commands.
- run_command does not use a shell. Only limited go and git commands are available.
- If you are confident the workspace is repaired, finish with repaired:true. aicommit will still rerun verification, commit, and push.
`)
	if len(req.ConflictPaths) > 0 {
		builder.WriteString("\nUnmerged paths:\n")
		for _, path := range req.ConflictPaths {
			builder.WriteString("- ")
			builder.WriteString(path)
			builder.WriteString("\n")
		}
	}
	if strings.TrimSpace(req.VerificationError) != "" {
		builder.WriteString("\nVerification failure:\n")
		builder.WriteString(truncatePromptText(strings.TrimSpace(req.VerificationError)))
		builder.WriteString("\n")
	}
	if strings.TrimSpace(req.Status) != "" {
		builder.WriteString("\nCurrent git status:\n")
		builder.WriteString(truncatePromptText(strings.TrimSpace(req.Status)))
		builder.WriteString("\n")
	}
	if strings.TrimSpace(req.Note) != "" {
		builder.WriteString("\nNotes:\n")
		builder.WriteString(strings.TrimSpace(req.Note))
		builder.WriteString("\n")
	}
	if len(req.History) > 0 {
		builder.WriteString("\nRepair agent history:\n")
		for i, item := range req.History {
			builder.WriteString("\nStep ")
			builder.WriteString(fmt.Sprintf("%d", i+1))
			builder.WriteString(" action:\n")
			builder.WriteString(truncatePromptText(item.Action))
			builder.WriteString("\nStep ")
			builder.WriteString(fmt.Sprintf("%d", i+1))
			builder.WriteString(" result:\n")
			builder.WriteString(truncatePromptText(item.Result))
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func writePromptBlock(builder *strings.Builder, label, text string) {
	if text == "" {
		return
	}
	builder.WriteString(label)
	builder.WriteString(":\n")
	builder.WriteString(truncatePromptText(text))
	builder.WriteString("\n")
}

func truncatePromptText(text string) string {
	if len(text) <= maxPromptFileChars {
		return text
	}
	return text[:maxPromptFileChars] + "\n[truncated]\n"
}

func ExtractCommitMessage(text string) (string, error) {
	text = strings.TrimSpace(text)
	text = stripCodeFence(text)
	var parsed commitJSON
	if err := json.Unmarshal([]byte(text), &parsed); err == nil && strings.TrimSpace(parsed.Message) != "" {
		return NormalizeCommitMessage(parsed.Message), nil
	}
	if object := firstJSONObject(text); object != "" {
		if err := json.Unmarshal([]byte(object), &parsed); err == nil && strings.TrimSpace(parsed.Message) != "" {
			return NormalizeCommitMessage(parsed.Message), nil
		}
	}
	line := firstNonEmptyLine(text)
	if line == "" {
		return "", errors.New("AI response did not include a commit message")
	}
	return NormalizeCommitMessage(line), nil
}

func ExtractMergeResolution(text string) (MergeResolutionResult, error) {
	text = strings.TrimSpace(stripCodeFence(text))
	var parsed MergeResolutionResult
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		object := firstJSONObject(text)
		if object == "" {
			return MergeResolutionResult{}, err
		}
		if err := json.Unmarshal([]byte(object), &parsed); err != nil {
			return MergeResolutionResult{}, err
		}
	}
	parsed.Reason = strings.TrimSpace(parsed.Reason)
	for i := range parsed.Files {
		parsed.Files[i].Path = strings.TrimSpace(parsed.Files[i].Path)
	}
	return parsed, nil
}

func ExtractWorkspaceRepairResult(text string) (WorkspaceRepairResult, error) {
	text = strings.TrimSpace(stripCodeFence(text))
	var parsed WorkspaceRepairResult
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		object := firstJSONObject(text)
		if object == "" {
			return WorkspaceRepairResult{}, err
		}
		if err := json.Unmarshal([]byte(object), &parsed); err != nil {
			return WorkspaceRepairResult{}, err
		}
	}
	parsed.Reason = strings.TrimSpace(parsed.Reason)
	return parsed, nil
}

func ExtractRepairAgentAction(text string) (RepairAgentAction, error) {
	text = strings.TrimSpace(stripCodeFence(text))
	var parsed RepairAgentAction
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		object := firstJSONObject(text)
		if object == "" {
			return RepairAgentAction{}, err
		}
		if err := json.Unmarshal([]byte(object), &parsed); err != nil {
			return RepairAgentAction{}, err
		}
	}
	parsed.Action = strings.ToLower(strings.TrimSpace(parsed.Action))
	parsed.Path = strings.TrimSpace(parsed.Path)
	parsed.Command = strings.TrimSpace(parsed.Command)
	parsed.Reason = strings.TrimSpace(parsed.Reason)
	if parsed.Action == "" {
		return RepairAgentAction{}, errors.New("repair agent response did not include an action")
	}
	return parsed, nil
}

func NormalizeCommitMessage(message string) string {
	message = strings.TrimSpace(stripCodeFence(message))
	if parsed, err := ExtractJSONField(message, "message"); err == nil && parsed != "" {
		message = parsed
	}
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\r", "\n")
	lines := strings.Split(message, "\n")
	if len(lines) > 0 {
		message = strings.TrimSpace(lines[0])
	}
	message = strings.Trim(message, `"'`)
	message = strings.TrimSpace(message)
	message = strings.TrimSuffix(message, ".")
	if len(message) > 200 {
		message = strings.TrimSpace(message[:200])
	}
	return message
}

func ExtractJSONField(text, field string) (string, error) {
	var generic map[string]any
	if err := json.Unmarshal([]byte(text), &generic); err != nil {
		return "", err
	}
	value, ok := generic[field].(string)
	if !ok {
		return "", fmt.Errorf("field %q is not a string", field)
	}
	return strings.TrimSpace(value), nil
}

func stripCodeFence(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return text
	}
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	return strings.TrimSpace(text)
}

func firstJSONObject(text string) string {
	re := regexp.MustCompile(`(?s)\{.*\}`)
	match := re.FindString(text)
	return strings.TrimSpace(match)
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
