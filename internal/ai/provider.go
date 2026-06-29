package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/CoolBanHub/aicommit/internal/config"
)

type CommitRequest struct {
	RepoRoot string
	Files    []string
	Stat     string
	Diff     string
	Style    string
}

type Provider interface {
	GenerateCommitMessage(context.Context, CommitRequest) (string, error)
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
	if cfg, ok := providers["openai"]; ok && resolveAPIKey(cfg) != "" {
		return "openai"
	}
	if cfg, ok := providers["deepseek"]; ok && resolveAPIKey(cfg) != "" {
		return "deepseek"
	}
	if cfg, ok := providers["anthropic"]; ok && resolveAPIKey(cfg) != "" {
		return "anthropic"
	}
	if _, err := exec.LookPath("codex"); err == nil {
		return "codex"
	}
	if _, err := exec.LookPath("claude"); err == nil {
		return "claude-code"
	}
	return "openai"
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

Changed files:
%s

Diff stat:
%s

Cached diff:
%s
`, style, strings.Join(req.Files, "\n"), strings.TrimSpace(req.Stat), strings.TrimSpace(req.Diff))
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
