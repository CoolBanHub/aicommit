package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CoolBanHub/aicommit/internal/ai"
	"github.com/CoolBanHub/aicommit/internal/config"
	"github.com/CoolBanHub/aicommit/internal/filter"
	"github.com/CoolBanHub/aicommit/internal/gitx"
)

type CommitOptions struct {
	Repo            string   `json:"repo"`
	ConfigPath      string   `json:"configPath"`
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	Message         string   `json:"message"`
	Style           string   `json:"style"`
	PushMode        string   `json:"push"`
	DryRun          bool     `json:"dryRun"`
	IncludePatterns []string `json:"include"`
	ExcludePatterns []string `json:"exclude"`
	MaxDiffChars    int      `json:"maxDiffChars"`
	MaxFileBytes    int64    `json:"maxFileBytes"`
	DisableGPGSign  *bool    `json:"disableGPGSign"`
}

type CommitResult struct {
	RepoRoot          string            `json:"repoRoot"`
	Provider          string            `json:"provider"`
	Model             string            `json:"model,omitempty"`
	Message           string            `json:"message"`
	CommitHash        string            `json:"commitHash,omitempty"`
	Files             []string          `json:"files"`
	Skipped           []filter.Decision `json:"skipped"`
	NoChanges         bool              `json:"noChanges"`
	DryRun            bool              `json:"dryRun"`
	Pushed            bool              `json:"pushed"`
	PushTarget        string            `json:"pushTarget,omitempty"`
	PushSkippedReason string            `json:"pushSkippedReason,omitempty"`
	DiffTruncated     bool              `json:"diffTruncated"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	Warnings          []string          `json:"warnings,omitempty"`
	UnstagedProtected []filter.Decision `json:"unstagedProtected,omitempty"`
	StagedProtected   []filter.Decision `json:"stagedProtected,omitempty"`
}

type PushOptions struct {
	Repo string `json:"repo"`
}

type PushResult struct {
	RepoRoot      string `json:"repoRoot"`
	Pushed        bool   `json:"pushed"`
	Target        string `json:"target,omitempty"`
	SkippedReason string `json:"skippedReason,omitempty"`
}

func BoolPtr(v bool) *bool {
	return &v
}

func RunCommit(ctx context.Context, opts CommitOptions) (CommitResult, error) {
	repo := opts.Repo
	if repo == "" {
		repo = "."
	}
	repoRoot, initialized, err := gitx.EnsureRepo(ctx, repo)
	if err != nil {
		return CommitResult{}, err
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return CommitResult{}, err
	}
	applyCommitOverrides(&cfg, opts)
	if err := validatePushMode(cfg.Push); err != nil {
		return CommitResult{}, err
	}

	rules := filter.NewRules(filter.Options{
		MaxFileBytes: cfg.MaxFileBytes,
		Include:      append(append([]string{}, cfg.Protect.Include...), opts.IncludePatterns...),
		Exclude:      append(append([]string{}, cfg.Protect.Exclude...), opts.ExcludePatterns...),
	})

	result := CommitResult{
		RepoRoot: repoRoot,
		DryRun:   opts.DryRun,
		Metadata: map[string]string{},
	}
	if initialized {
		result.Metadata["gitInitialized"] = "true"
	}
	gitignorePatterns := append([]string{}, filter.DefaultGitignorePatterns()...)
	gitignorePatterns = append(gitignorePatterns, cfg.Protect.Exclude...)
	gitignorePatterns = append(gitignorePatterns, opts.ExcludePatterns...)
	if created, err := gitx.EnsureGitignore(repoRoot, gitignorePatterns); err != nil {
		return CommitResult{}, err
	} else if created {
		result.Metadata["gitignoreCreated"] = "true"
	}
	if repaired, err := gitx.RepairAicommitGitignore(repoRoot); err != nil {
		return CommitResult{}, err
	} else if repaired {
		result.Metadata["gitignoreRepaired"] = "true"
	}

	changed, err := gitx.StatusPaths(ctx, repoRoot)
	if err != nil {
		return CommitResult{}, err
	}
	staged, err := gitx.StagedPaths(ctx, repoRoot)
	if err != nil {
		return CommitResult{}, err
	}
	ignoredChanged, err := gitx.IgnoredPaths(ctx, repoRoot, changed)
	if err != nil {
		return CommitResult{}, err
	}
	ignoredStaged, err := gitx.IgnoredPaths(ctx, repoRoot, staged)
	if err != nil {
		return CommitResult{}, err
	}

	stagedSet := map[string]struct{}{}
	for _, path := range staged {
		stagedSet[path] = struct{}{}
		decision := decidePath(repoRoot, path, rules, ignoredStaged)
		if !decision.Allowed {
			if err := gitx.UnstagePath(ctx, repoRoot, path); err != nil {
				return CommitResult{}, fmt.Errorf("unstage protected file %s: %w", path, err)
			}
			result.Skipped = appendDecision(result.Skipped, decision)
			result.StagedProtected = appendDecision(result.StagedProtected, decision)
		}
	}

	var toStage []string
	seenChanged := map[string]struct{}{}
	var detectedIgnorePatterns []string
	for _, path := range changed {
		if _, seen := seenChanged[path]; seen {
			continue
		}
		seenChanged[path] = struct{}{}
		decision := decidePath(repoRoot, path, rules, ignoredChanged)
		if !decision.Allowed {
			result.Skipped = appendDecision(result.Skipped, decision)
			if decision.Reason != "ignored by .gitignore" {
				detectedIgnorePatterns = append(detectedIgnorePatterns, decision.Path)
			}
			if _, wasStaged := stagedSet[path]; !wasStaged {
				result.UnstagedProtected = appendDecision(result.UnstagedProtected, decision)
			}
			continue
		}
		toStage = append(toStage, path)
	}

	if updated, err := gitx.AppendGitignorePatterns(repoRoot, detectedIgnorePatterns); err != nil {
		return CommitResult{}, err
	} else if updated {
		result.Metadata["gitignoreUpdated"] = "true"
		changed, err = gitx.StatusPaths(ctx, repoRoot)
		if err != nil {
			return CommitResult{}, err
		}
		seenToStage := map[string]struct{}{}
		for _, path := range toStage {
			seenToStage[path] = struct{}{}
		}
		for _, path := range changed {
			if path == ".gitignore" {
				if _, seen := seenToStage[path]; !seen {
					toStage = append(toStage, path)
				}
				break
			}
		}
	}

	if len(toStage) > 0 {
		if err := gitx.StagePaths(ctx, repoRoot, toStage); err != nil {
			return CommitResult{}, err
		}
	}

	hasChanges, err := gitx.HasCachedChanges(ctx, repoRoot)
	if err != nil {
		return CommitResult{}, err
	}
	if !hasChanges {
		result.NoChanges = true
		return result, nil
	}

	files, err := gitx.StagedPaths(ctx, repoRoot)
	if err != nil {
		return CommitResult{}, err
	}
	result.Files = files

	stat, err := gitx.CachedStat(ctx, repoRoot)
	if err != nil {
		return CommitResult{}, err
	}
	diff, truncated, err := gitx.CachedDiff(ctx, repoRoot, cfg.MaxDiffChars)
	if err != nil {
		return CommitResult{}, err
	}
	result.DiffTruncated = truncated

	message := strings.TrimSpace(opts.Message)
	providerName := cfg.Provider
	model := cfg.Model
	if message == "" {
		provider, resolved, err := ai.NewProvider(ai.FactoryConfig{
			Provider:  providerName,
			Model:     model,
			Providers: cfg.Providers,
		})
		if err != nil {
			return CommitResult{}, err
		}
		result.Provider = resolved.Name
		result.Model = resolved.Model
		message, err = provider.GenerateCommitMessage(ctx, ai.CommitRequest{
			RepoRoot: repoRoot,
			Files:    files,
			Stat:     stat,
			Diff:     diff,
			Style:    cfg.Style,
		})
		if err != nil {
			return CommitResult{}, err
		}
	} else {
		result.Provider = "manual"
	}
	message = ai.NormalizeCommitMessage(message)
	if message == "" {
		return CommitResult{}, errors.New("commit message is empty")
	}
	result.Message = message

	if opts.DryRun {
		return result, nil
	}

	if err := gitx.Commit(ctx, repoRoot, message, cfg.DisableGPGSign); err != nil {
		return CommitResult{}, err
	}
	if hash, err := gitx.CommitHash(ctx, repoRoot); err == nil {
		result.CommitHash = hash
	}

	pushResult, err := pushIfNeeded(ctx, repoRoot, cfg.Push)
	if err != nil {
		return CommitResult{}, err
	}
	result.Pushed = pushResult.Pushed
	result.PushTarget = pushResult.Target
	result.PushSkippedReason = pushResult.SkippedReason
	return result, nil
}

func RunPush(ctx context.Context, opts PushOptions) (PushResult, error) {
	repo := opts.Repo
	if repo == "" {
		repo = "."
	}
	repoRoot, err := gitx.Root(ctx, repo)
	if err != nil {
		return PushResult{}, err
	}
	hasRemote, err := gitx.HasRemote(ctx, repoRoot)
	if err != nil {
		return PushResult{}, err
	}
	if !hasRemote {
		return PushResult{RepoRoot: repoRoot, SkippedReason: "no remote"}, nil
	}
	pushed, target, reason, err := gitx.Push(ctx, repoRoot)
	if err != nil {
		return PushResult{}, err
	}
	return PushResult{
		RepoRoot:      repoRoot,
		Pushed:        pushed,
		Target:        target,
		SkippedReason: reason,
	}, nil
}

func applyCommitOverrides(cfg *config.Config, opts CommitOptions) {
	if opts.Provider != "" {
		cfg.Provider = opts.Provider
	}
	if opts.Model != "" {
		cfg.Model = opts.Model
	}
	if opts.PushMode != "" {
		cfg.Push = opts.PushMode
	}
	if opts.Style != "" {
		if cfg.Style == "" {
			cfg.Style = opts.Style
		} else {
			cfg.Style = cfg.Style + "\n" + opts.Style
		}
	}
	if opts.MaxDiffChars > 0 {
		cfg.MaxDiffChars = opts.MaxDiffChars
	}
	if opts.MaxFileBytes > 0 {
		cfg.MaxFileBytes = opts.MaxFileBytes
	}
	if opts.DisableGPGSign != nil {
		cfg.DisableGPGSign = *opts.DisableGPGSign
	}
	if cfg.Push == "" {
		cfg.Push = config.PushNever
	}
	if cfg.Provider == "" {
		cfg.Provider = "auto"
	}
	if cfg.MaxDiffChars <= 0 {
		cfg.MaxDiffChars = 120_000
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = 2 * 1024 * 1024
	}
}

func validatePushMode(mode string) error {
	switch mode {
	case "", config.PushAuto, config.PushAlways, config.PushNever:
		return nil
	default:
		return fmt.Errorf("invalid push mode %q; use auto, always, or never", mode)
	}
}

type pushOutcome struct {
	Pushed        bool
	Target        string
	SkippedReason string
}

func pushIfNeeded(ctx context.Context, repoRoot, mode string) (pushOutcome, error) {
	if mode == "" {
		mode = config.PushAuto
	}
	if mode == config.PushNever {
		return pushOutcome{SkippedReason: "disabled"}, nil
	}
	hasRemote, err := gitx.HasRemote(ctx, repoRoot)
	if err != nil {
		return pushOutcome{}, err
	}
	if !hasRemote {
		if mode == config.PushAlways {
			return pushOutcome{}, errors.New("push requested, but no git remote is configured")
		}
		return pushOutcome{SkippedReason: "no remote"}, nil
	}
	pushed, target, reason, err := gitx.Push(ctx, repoRoot)
	if err != nil {
		return pushOutcome{}, err
	}
	return pushOutcome{Pushed: pushed, Target: target, SkippedReason: reason}, nil
}

func appendDecision(items []filter.Decision, item filter.Decision) []filter.Decision {
	for _, existing := range items {
		if existing.Path == item.Path && existing.Reason == item.Reason {
			return items
		}
	}
	return append(items, item)
}

func decidePath(repoRoot, path string, rules filter.Rules, ignored map[string]struct{}) filter.Decision {
	if _, ok := ignored[path]; ok {
		return filter.Decision{Path: path, Allowed: false, Reason: "ignored by .gitignore"}
	}
	return rules.Decide(repoRoot, path)
}
