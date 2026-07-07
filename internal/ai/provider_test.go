package ai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CoolBanHub/aicommit/internal/config"
)

func TestExtractCommitMessageFromJSON(t *testing.T) {
	got, err := ExtractCommitMessage(`{"message":"Add commit generation"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Add commit generation" {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestExtractCommitMessageFromFencedJSON(t *testing.T) {
	got, err := ExtractCommitMessage("```json\n{\"message\":\"fix: handle env files.\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if got != "fix: handle env files" {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestNormalizeCommitMessageUsesFirstLine(t *testing.T) {
	got := NormalizeCommitMessage("Add service mode\n\nMore details")
	if got != "Add service mode" {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestResolveAutoProviderPrefersClaudeCodeThenCodex(t *testing.T) {
	tempDir := t.TempDir()
	writeExecutable(t, tempDir, "claude")
	writeExecutable(t, tempDir, "codex")
	t.Setenv("PATH", tempDir)

	got := resolveAutoProvider(map[string]config.ProviderConfig{})
	if got != "claude-code" {
		t.Fatalf("unexpected provider %q", got)
	}
}

func TestAutoProviderCandidatesIncludeClaudeThenCodex(t *testing.T) {
	tempDir := t.TempDir()
	writeExecutable(t, tempDir, "claude")
	writeExecutable(t, tempDir, "codex")
	t.Setenv("PATH", tempDir)

	got := AutoProviderCandidates(map[string]config.ProviderConfig{})
	want := []string{"claude-code", "codex"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected providers %#v", got)
	}
}

func TestCommandFailureDetailsUsesStructuredStdout(t *testing.T) {
	got := commandFailureDetails(`{"type":"result","is_error":true,"result":"API Error: Request rejected (429)"}`, "")
	if got != "API Error: Request rejected (429)" {
		t.Fatalf("unexpected details %q", got)
	}
}

func TestResolveAutoProviderFallsBackToCodexWhenClaudeMissing(t *testing.T) {
	tempDir := t.TempDir()
	writeExecutable(t, tempDir, "codex")
	t.Setenv("PATH", tempDir)

	got := resolveAutoProvider(map[string]config.ProviderConfig{})
	if got != "codex" {
		t.Fatalf("unexpected provider %q", got)
	}
}

func TestResolveAutoProviderPrefersOpenAIWhenConfigured(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	got := resolveAutoProvider(map[string]config.ProviderConfig{
		"openai": {
			Type:      "openai",
			APIKeyEnv: "OPENAI_API_KEY",
		},
		"anthropic": {
			Type:      "anthropic",
			APIKeyEnv: "ANTHROPIC_API_KEY",
		},
	})
	if got != "openai" {
		t.Fatalf("unexpected provider %q", got)
	}
}

func TestResolveAutoProviderSkipsInvalidBaseURL(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	got := resolveAutoProvider(map[string]config.ProviderConfig{
		"openai": {
			Type:      "openai",
			BaseURL:   "not a url",
			APIKeyEnv: "OPENAI_API_KEY",
		},
		"anthropic": {
			Type:      "anthropic",
			APIKeyEnv: "ANTHROPIC_API_KEY",
		},
	})
	if got != "anthropic" {
		t.Fatalf("unexpected provider %q", got)
	}
}

func TestResolveAutoProviderFallsBackToAnthropicThenDeepSeek(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-key")

	got := resolveAutoProvider(map[string]config.ProviderConfig{
		"anthropic": {
			Type:      "anthropic",
			APIKeyEnv: "ANTHROPIC_API_KEY",
		},
		"deepseek": {
			Type:      "openai-compatible",
			APIKeyEnv: "DEEPSEEK_API_KEY",
		},
	})
	if got != "anthropic" {
		t.Fatalf("unexpected provider %q", got)
	}
}

func TestResolveAutoProviderUsesDeepSeekWhenOnlyDeepSeekConfigured(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-key")

	got := resolveAutoProvider(map[string]config.ProviderConfig{
		"deepseek": {
			Type:      "openai-compatible",
			APIKeyEnv: "DEEPSEEK_API_KEY",
		},
	})
	if got != "deepseek" {
		t.Fatalf("unexpected provider %q", got)
	}
}

func TestNewProviderDefaultsBaseURL(t *testing.T) {
	provider, resolved, err := NewProvider(FactoryConfig{
		Provider: "openai",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type:   "openai",
				APIKey: "openai-key",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Name != "openai" {
		t.Fatalf("unexpected resolved provider %q", resolved.Name)
	}
	chatProvider, ok := provider.(*ChatCompletionsProvider)
	if !ok {
		t.Fatalf("unexpected provider type %T", provider)
	}
	if chatProvider.BaseURL != config.DefaultOpenAIBaseURL {
		t.Fatalf("unexpected base URL %q", chatProvider.BaseURL)
	}
}

func TestNewProviderRejectsInvalidBaseURL(t *testing.T) {
	_, _, err := NewProvider(FactoryConfig{
		Provider: "openai",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Type:    "openai",
				BaseURL: "ftp://example.com",
				APIKey:  "openai-key",
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "base URL must use http or https") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestBaseURLWithDefault(t *testing.T) {
	if got := baseURLWithDefault("openai", ""); got != config.DefaultOpenAIBaseURL {
		t.Fatalf("unexpected base URL %q", got)
	}
	if got := baseURLWithDefault("anthropic", " "); got != config.DefaultAnthropicBaseURL {
		t.Fatalf("unexpected base URL %q", got)
	}
	if got := baseURLWithDefault("deepseek", "https://example.com"); got != "https://example.com" {
		t.Fatalf("unexpected base URL %q", got)
	}
}

func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
