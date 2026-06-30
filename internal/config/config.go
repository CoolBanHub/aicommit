package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	PushAuto   = "auto"
	PushAlways = "always"
	PushNever  = "never"

	DefaultOpenAIBaseURL    = "https://api.openai.com/v1"
	DefaultDeepSeekBaseURL  = "https://api.deepseek.com"
	DefaultAnthropicBaseURL = "https://api.anthropic.com/v1"
)

type Config struct {
	Provider       string                    `json:"provider" yaml:"provider"`
	Model          string                    `json:"model" yaml:"model"`
	Push           string                    `json:"push" yaml:"push"`
	Style          string                    `json:"style" yaml:"style"`
	MaxDiffChars   int                       `json:"maxDiffChars" yaml:"maxDiffChars"`
	MaxFileBytes   int64                     `json:"maxFileBytes" yaml:"maxFileBytes"`
	DisableGPGSign bool                      `json:"disableGPGSign" yaml:"disableGPGSign"`
	Protect        ProtectConfig             `json:"protect" yaml:"protect"`
	Generated      GeneratedConfig           `json:"generated" yaml:"generated"`
	Providers      map[string]ProviderConfig `json:"providers" yaml:"providers"`
}

type ProtectConfig struct {
	Include []string `json:"include" yaml:"include"`
	Exclude []string `json:"exclude" yaml:"exclude"`
}

type GeneratedConfig struct {
	Patterns []string `json:"patterns" yaml:"patterns"`
	Message  string   `json:"message" yaml:"message"`
}

type ProviderConfig struct {
	Type           string   `json:"type" yaml:"type"`
	BaseURL        string   `json:"baseURL" yaml:"baseURL"`
	APIKey         string   `json:"apiKey" yaml:"apiKey,omitempty"`
	APIKeyEnv      string   `json:"apiKeyEnv" yaml:"apiKeyEnv,omitempty"`
	Model          string   `json:"model" yaml:"model,omitempty"`
	ModelEnv       string   `json:"modelEnv" yaml:"modelEnv,omitempty"`
	CommandEnv     string   `json:"commandEnv" yaml:"commandEnv,omitempty"`
	Command        []string `json:"command" yaml:"command,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds" yaml:"timeoutSeconds,omitempty"`
}

func Default() Config {
	return Config{
		Provider:       "auto",
		Push:           PushNever,
		MaxDiffChars:   120_000,
		MaxFileBytes:   2 * 1024 * 1024,
		DisableGPGSign: true,
		Generated: GeneratedConfig{
			Patterns: []string{
				"*.pb.go",         // protobuf
				"*.pb.gw.go",      // grpc-gateway
				"*.gen.go",        // general generated
				"*.generated.go",  // general generated
				"*_gen.go",        // general generated
				"mock_*.go",       // go mock
				"*.db.go",         // sqlc
				"*.graphql.go",    // graphql
			},
			Message: "chore: update generated files",
		},
		Providers: map[string]ProviderConfig{
			"openai": {
				Type:      "openai",
				BaseURL:   DefaultOpenAIBaseURL,
				APIKeyEnv: "OPENAI_API_KEY",
				ModelEnv:  "OPENAI_MODEL",
				Model:     "gpt-5.4-mini",
			},
			"deepseek": {
				Type:      "openai-compatible",
				BaseURL:   DefaultDeepSeekBaseURL,
				APIKeyEnv: "DEEPSEEK_API_KEY",
				ModelEnv:  "DEEPSEEK_MODEL",
				Model:     "deepseek-v4-flash",
			},
			"anthropic": {
				Type:      "anthropic",
				BaseURL:   DefaultAnthropicBaseURL,
				APIKeyEnv: "ANTHROPIC_API_KEY",
				ModelEnv:  "ANTHROPIC_MODEL",
				Model:     "claude-haiku-4-5-20251001",
			},
			"codex": {
				Type:           "codex-cli",
				ModelEnv:       "CODEX_MODEL",
				TimeoutSeconds: 120,
			},
			"claude-code": {
				Type:           "claude-code-cli",
				ModelEnv:       "CLAUDE_MODEL",
				Model:          "sonnet",
				TimeoutSeconds: 120,
			},
			"cdp": {
				Type:           "command",
				ModelEnv:       "AICOMMIT_CDP_MODEL",
				CommandEnv:     "AICOMMIT_CDP_COMMAND",
				TimeoutSeconds: 120,
			},
		},
	}
}

func Load(explicitPath string) (Config, error) {
	cfg := Default()
	path, err := ResolvePath(explicitPath)
	if err != nil {
		return cfg, err
	}
	if err := EnsureFile(path, cfg); err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	var user Config
	if err := yaml.Unmarshal(data, &user); err != nil {
		return cfg, err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return cfg, err
	}
	Merge(&cfg, user)
	if _, ok := raw["disableGPGSign"]; ok {
		cfg.DisableGPGSign = user.DisableGPGSign
	}
	applyEnvOverrides(&cfg)
	return cfg, nil
}

func ResolvePath(explicitPath string) (string, error) {
	if explicitPath != "" {
		return explicitPath, nil
	}
	if envPath := os.Getenv("AICOMMIT_CONFIG"); envPath != "" {
		return envPath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("unable to determine home directory for config")
	}
	return filepath.Join(home, ".aicommit", "config.yaml"), nil
}

func EnsureFile(path string, defaults Config) error {
	if path == "" {
		return nil
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("config path is a directory: %s", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(defaults)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func Merge(dst *Config, src Config) {
	if src.Provider != "" {
		dst.Provider = src.Provider
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Push != "" {
		dst.Push = normalizePush(src.Push)
	}
	if src.Style != "" {
		dst.Style = src.Style
	}
	if src.MaxDiffChars > 0 {
		dst.MaxDiffChars = src.MaxDiffChars
	}
	if src.MaxFileBytes > 0 {
		dst.MaxFileBytes = src.MaxFileBytes
	}
	if src.Protect.Include != nil {
		dst.Protect.Include = append([]string{}, src.Protect.Include...)
	}
	if src.Protect.Exclude != nil {
		dst.Protect.Exclude = append([]string{}, src.Protect.Exclude...)
	}
	if src.Generated.Patterns != nil {
		dst.Generated.Patterns = append([]string{}, src.Generated.Patterns...)
	}
	if src.Generated.Message != "" {
		dst.Generated.Message = src.Generated.Message
	}
	if src.Providers != nil {
		if dst.Providers == nil {
			dst.Providers = map[string]ProviderConfig{}
		}
		for name, override := range src.Providers {
			base := dst.Providers[name]
			MergeProvider(&base, override)
			dst.Providers[name] = base
		}
	}
	if src.DisableGPGSign {
		dst.DisableGPGSign = true
	}
}

func MergeProvider(dst *ProviderConfig, src ProviderConfig) {
	if src.Type != "" {
		dst.Type = src.Type
	}
	if src.BaseURL != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.APIKey != "" {
		dst.APIKey = src.APIKey
	}
	if src.APIKeyEnv != "" {
		dst.APIKeyEnv = src.APIKeyEnv
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.ModelEnv != "" {
		dst.ModelEnv = src.ModelEnv
	}
	if src.CommandEnv != "" {
		dst.CommandEnv = src.CommandEnv
	}
	if src.Command != nil {
		dst.Command = append([]string{}, src.Command...)
	}
	if src.TimeoutSeconds > 0 {
		dst.TimeoutSeconds = src.TimeoutSeconds
	}
}

func applyEnvOverrides(cfg *Config) {
	if provider := os.Getenv("AICOMMIT_PROVIDER"); provider != "" {
		cfg.Provider = provider
	}
	if model := os.Getenv("AICOMMIT_MODEL"); model != "" {
		cfg.Model = model
	}
	if push := os.Getenv("AICOMMIT_PUSH"); push != "" {
		cfg.Push = normalizePush(push)
	}
	for name, provider := range cfg.Providers {
		if provider.ModelEnv != "" {
			if model := os.Getenv(provider.ModelEnv); model != "" {
				provider.Model = model
			}
		}
		if provider.CommandEnv != "" {
			if command := os.Getenv(provider.CommandEnv); command != "" {
				provider.Command = []string{"sh", "-c", command}
			}
		}
		cfg.Providers[name] = provider
	}
}

func normalizePush(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
