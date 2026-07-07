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
