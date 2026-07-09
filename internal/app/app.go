package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

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
	PushRecovered     bool              `json:"pushRecovered,omitempty"`
	PushRecoveryHash  string            `json:"pushRecoveryHash,omitempty"`
	PushRecoverySteps []string          `json:"pushRecoverySteps,omitempty"`
	DiffTruncated     bool              `json:"diffTruncated"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	Warnings          []string          `json:"warnings,omitempty"`
	UnstagedProtected []filter.Decision `json:"unstagedProtected,omitempty"`
	StagedProtected   []filter.Decision `json:"stagedProtected,omitempty"`
}

type PushOptions struct {
	Repo       string `json:"repo"`
	ConfigPath string `json:"configPath"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
}

type PushResult struct {
	RepoRoot      string   `json:"repoRoot"`
	Pushed        bool     `json:"pushed"`
	Target        string   `json:"target,omitempty"`
	SkippedReason string   `json:"skippedReason,omitempty"`
	Recovered     bool     `json:"recovered,omitempty"`
	RecoveryHash  string   `json:"recoveryHash,omitempty"`
	RecoverySteps []string `json:"recoverySteps,omitempty"`
}

type TagOptions struct {
	Repo string `json:"repo"`
	Tag  string `json:"tag"`
	Push bool   `json:"push"`
}

type TagResult struct {
	RepoRoot          string `json:"repoRoot"`
	Tag               string `json:"tag"`
	Previous          string `json:"previous,omitempty"`
	Pushed            bool   `json:"pushed"`
	PushTarget        string `json:"pushTarget,omitempty"`
	PushSkippedReason string `json:"pushSkippedReason,omitempty"`
}

type PushTagOptions struct {
	Repo string `json:"repo"`
	Tag  string `json:"tag"`
}

type PushTagResult struct {
	RepoRoot      string `json:"repoRoot"`
	Tag           string `json:"tag"`
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
			if shouldAutoIgnoreDecision(decision) {
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

	generatedFiles := filter.MatchGeneratedFiles(files, cfg.Generated.Patterns)
	generatedOnly := len(files) > 0 && len(generatedFiles) == len(files)

	if message == "" && generatedOnly {
		message = strings.TrimSpace(cfg.Generated.Message)
		if message == "" {
			message = "chore: update generated files"
		}
		result.Provider = "generated"
		result.Metadata["generatedFiles"] = "true"
	}

	if message == "" {
		request := ai.CommitRequest{
			RepoRoot:       repoRoot,
			Files:          files,
			Stat:           stat,
			Diff:           diff,
			Style:          cfg.Style,
			GeneratedFiles: generatedFiles,
		}
		resolved, warnings, err := generateAICommitMessage(ctx, providerName, model, cfg.Providers, request, &message)
		if err != nil {
			return CommitResult{}, err
		}
		result.Provider = resolved.Name
		result.Model = resolved.Model
		result.Warnings = append(result.Warnings, warnings...)
	} else if opts.Message != "" {
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

	pushResult, err := pushIfNeeded(ctx, repoRoot, cfg)
	if err != nil {
		return CommitResult{}, err
	}
	result.Pushed = pushResult.Pushed
	result.PushTarget = pushResult.Target
	result.PushSkippedReason = pushResult.SkippedReason
	result.PushRecovered = pushResult.Recovered
	result.PushRecoveryHash = pushResult.RecoveryHash
	result.PushRecoverySteps = pushResult.RecoverySteps
	return result, nil
}

func generateAICommitMessage(ctx context.Context, providerName, model string, providers map[string]config.ProviderConfig, request ai.CommitRequest, message *string) (ai.ResolvedProvider, []string, error) {
	if !isAutoProvider(providerName) {
		provider, resolved, err := ai.NewProvider(ai.FactoryConfig{
			Provider:  providerName,
			Model:     model,
			Providers: providers,
		})
		if err != nil {
			return ai.ResolvedProvider{}, nil, err
		}
		generated, err := provider.GenerateCommitMessage(ctx, request)
		if err != nil {
			return ai.ResolvedProvider{}, nil, err
		}
		*message = generated
		return resolved, nil, nil
	}

	var failures []string
	for _, candidate := range ai.AutoProviderCandidates(providers) {
		provider, resolved, err := ai.NewProvider(ai.FactoryConfig{
			Provider:  candidate,
			Model:     model,
			Providers: providers,
		})
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		generated, err := provider.GenerateCommitMessage(ctx, request)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", resolved.Name, err))
			continue
		}
		*message = generated
		return resolved, autoFallbackWarnings(failures, resolved.Name), nil
	}
	if len(failures) == 0 {
		return ai.ResolvedProvider{}, nil, errors.New("no auto providers are available")
	}
	return ai.ResolvedProvider{}, nil, fmt.Errorf("all auto providers failed: %s", strings.Join(failures, "; "))
}

func isAutoProvider(providerName string) bool {
	providerName = strings.TrimSpace(providerName)
	return providerName == "" || providerName == "auto"
}

func autoFallbackWarnings(failures []string, selected string) []string {
	if len(failures) == 0 {
		return nil
	}
	warnings := make([]string, 0, len(failures))
	for _, failure := range failures {
		warnings = append(warnings, fmt.Sprintf("auto provider fallback: %s; using %s", failure, selected))
	}
	return warnings
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
	pushResult, err := pushWithRecoveryForPushCommand(ctx, repoRoot, opts)
	if err != nil {
		return PushResult{}, err
	}
	return PushResult{
		RepoRoot:      repoRoot,
		Pushed:        pushResult.Pushed,
		Target:        pushResult.Target,
		SkippedReason: pushResult.SkippedReason,
		Recovered:     pushResult.Recovered,
		RecoveryHash:  pushResult.RecoveryHash,
		RecoverySteps: pushResult.RecoverySteps,
	}, nil
}

func RunTag(ctx context.Context, opts TagOptions) (TagResult, error) {
	repo := opts.Repo
	if repo == "" {
		repo = "."
	}
	repoRoot, err := gitx.Root(ctx, repo)
	if err != nil {
		return TagResult{}, err
	}

	tag := strings.TrimSpace(opts.Tag)
	var previous string
	if tag == "" {
		tags, err := gitx.TagsSortedByVersion(ctx, repoRoot)
		if err != nil {
			return TagResult{}, err
		}
		tag, previous, err = nextTag(tags)
		if err != nil {
			return TagResult{}, err
		}
	}
	if tag == "" {
		return TagResult{}, errors.New("tag is empty")
	}
	if err := gitx.CreateTag(ctx, repoRoot, tag); err != nil {
		return TagResult{}, err
	}
	result := TagResult{RepoRoot: repoRoot, Tag: tag, Previous: previous}
	if opts.Push {
		pushed, target, reason, err := gitx.PushTag(ctx, repoRoot, tag)
		if err != nil {
			return TagResult{}, err
		}
		result.Pushed = pushed
		result.PushTarget = target
		result.PushSkippedReason = reason
	}
	return result, nil
}

func RunPushTag(ctx context.Context, opts PushTagOptions) (PushTagResult, error) {
	repo := opts.Repo
	if repo == "" {
		repo = "."
	}
	repoRoot, err := gitx.Root(ctx, repo)
	if err != nil {
		return PushTagResult{}, err
	}
	tag := strings.TrimSpace(opts.Tag)
	if tag == "" {
		tags, err := gitx.TagsSortedByVersion(ctx, repoRoot)
		if err != nil {
			return PushTagResult{}, err
		}
		tag = latestNumericTag(tags)
	}
	if tag == "" {
		return PushTagResult{}, errors.New("tag is empty")
	}
	pushed, target, reason, err := gitx.PushTag(ctx, repoRoot, tag)
	if err != nil {
		return PushTagResult{}, err
	}
	return PushTagResult{
		RepoRoot:      repoRoot,
		Tag:           tag,
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

func applyPushOverrides(cfg *config.Config, opts PushOptions) {
	if opts.Provider != "" {
		cfg.Provider = opts.Provider
	}
	if opts.Model != "" {
		cfg.Model = opts.Model
	}
	if cfg.Provider == "" {
		cfg.Provider = "auto"
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
	Recovered     bool
	RecoveryHash  string
	RecoverySteps []string
}

func pushIfNeeded(ctx context.Context, repoRoot string, cfg config.Config) (pushOutcome, error) {
	mode := cfg.Push
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
	pushResult, err := pushWithRecovery(ctx, repoRoot, cfg)
	if err != nil {
		return pushOutcome{}, err
	}
	return pushResult, nil
}

func pushWithRecovery(ctx context.Context, repoRoot string, cfg config.Config) (pushOutcome, error) {
	pushed, target, reason, err := gitx.Push(ctx, repoRoot)
	if err == nil {
		return pushOutcome{Pushed: pushed, Target: target, SkippedReason: reason}, nil
	}
	if !gitx.IsNonFastForward(err) {
		return pushOutcome{}, err
	}
	return recoverPushAfterNonFastForward(ctx, repoRoot, cfg, err)
}

func pushWithRecoveryForPushCommand(ctx context.Context, repoRoot string, opts PushOptions) (pushOutcome, error) {
	pushed, target, reason, err := gitx.Push(ctx, repoRoot)
	if err == nil {
		return pushOutcome{Pushed: pushed, Target: target, SkippedReason: reason}, nil
	}
	if !gitx.IsNonFastForward(err) {
		return pushOutcome{}, err
	}
	cfg, cfgErr := config.Load(opts.ConfigPath)
	if cfgErr != nil {
		return pushOutcome{}, fmt.Errorf("%w; additionally failed to load config for auto recovery: %v", err, cfgErr)
	}
	applyPushOverrides(&cfg, opts)
	return recoverPushAfterNonFastForward(ctx, repoRoot, cfg, err)
}

func recoverPushAfterNonFastForward(ctx context.Context, repoRoot string, cfg config.Config, pushErr error) (pushOutcome, error) {
	clean, cleanErr := gitx.IsClean(ctx, repoRoot)
	if cleanErr != nil {
		return pushOutcome{}, fmt.Errorf("%w; additionally failed to check worktree before auto recovery: %v", pushErr, cleanErr)
	}
	if !clean {
		return pushOutcome{}, fmt.Errorf("%w; auto recovery requires a clean worktree", pushErr)
	}
	result, recoveryErr := recoverNonFastForwardPush(ctx, repoRoot, cfg)
	if recoveryErr != nil {
		return pushOutcome{}, fmt.Errorf("%w; auto recovery failed: %v", pushErr, recoveryErr)
	}
	return result, nil
}

func recoverNonFastForwardPush(ctx context.Context, repoRoot string, cfg config.Config) (pushOutcome, error) {
	steps := []string{"detected non-fast-forward push"}
	moduleConflictsResolved := false

	ref, err := gitx.FetchCurrentBranch(ctx, repoRoot)
	if err != nil {
		return pushOutcome{}, err
	}
	if canFastForward, err := gitx.IsAncestor(ctx, repoRoot, "HEAD", ref); err != nil {
		return pushOutcome{}, err
	} else if canFastForward {
		if err := gitx.MergeFastForward(ctx, repoRoot, ref); err != nil {
			return pushOutcome{}, err
		}
		steps = append(steps, "fast-forwarded local branch to remote")
		return pushOutcome{
			SkippedReason: "fast-forwarded to remote; nothing to push",
			Recovered:     true,
			RecoverySteps: steps,
		}, nil
	}

	if err := gitx.MergeNoCommit(ctx, repoRoot, ref); err != nil {
		unmerged, unmergedErr := gitx.UnmergedPaths(ctx, repoRoot)
		if unmergedErr != nil {
			return pushOutcome{}, fmt.Errorf("merge remote changes: %w; additionally failed to inspect conflicts: %v", err, unmergedErr)
		}
		if len(unmerged) == 0 {
			return pushOutcome{}, fmt.Errorf("merge remote changes: %w", err)
		}
		resolvedDerived, err := resolveDerivedConflictsWithOurs(ctx, repoRoot, unmerged)
		if err != nil {
			return pushOutcome{}, err
		}
		if len(resolvedDerived) > 0 {
			steps = append(steps, "resolved derived conflicts with current branch: "+strings.Join(resolvedDerived, ", "))
			unmerged, err = gitx.UnmergedPaths(ctx, repoRoot)
			if err != nil {
				return pushOutcome{}, err
			}
		}
		if len(unmerged) > 0 {
			aiResolved, aiReason, aiErr := repairMergeWithAI(ctx, repoRoot, cfg, unmerged, "")
			if aiErr == nil && aiResolved {
				steps = append(steps, "resolved merge conflicts with AI")
			} else {
				aiFailure := "AI could not resolve merge"
				if aiErr != nil {
					aiFailure = "AI merge resolution failed: " + aiErr.Error()
				} else if aiReason != "" {
					aiFailure = "AI could not resolve merge: " + aiReason
				}
				steps = append(steps, aiFailure)
				if !onlyRootGoModuleConflicts(unmerged) {
					return pushOutcome{}, fmt.Errorf("merge has non-Go-module conflicts: %s; %s", strings.Join(unmerged, ", "), aiFailure)
				}
				if err := resolveGoModuleConflicts(ctx, repoRoot, unmerged); err != nil {
					return pushOutcome{}, err
				}
				moduleConflictsResolved = true
				steps = append(steps, "resolved go.mod/go.sum conflicts with built-in fallback")
			}
		}
	}
	steps = append(steps, "merged remote branch without committing")

	verified, err := verifyGoModuleAfterMerge(ctx, repoRoot, cfg, &steps)
	if err != nil {
		return pushOutcome{}, err
	}
	if moduleConflictsResolved && !verified {
		return pushOutcome{}, errors.New("resolved go.mod/go.sum conflicts, but no root go.mod was available for verification")
	}

	if err := gitx.StageAll(ctx, repoRoot); err != nil {
		return pushOutcome{}, err
	}
	if unmerged, err := gitx.UnmergedPaths(ctx, repoRoot); err != nil {
		return pushOutcome{}, err
	} else if len(unmerged) > 0 {
		return pushOutcome{}, fmt.Errorf("unresolved conflicts remain: %s", strings.Join(unmerged, ", "))
	}
	if err := gitx.CommitNoEdit(ctx, repoRoot, true); err != nil {
		return pushOutcome{}, err
	}
	hash, _ := gitx.CommitHash(ctx, repoRoot)
	steps = append(steps, "committed merge result")

	pushed, target, reason, err := gitx.Push(ctx, repoRoot)
	if err != nil {
		return pushOutcome{}, err
	}
	return pushOutcome{
		Pushed:        pushed,
		Target:        target,
		SkippedReason: reason,
		Recovered:     true,
		RecoveryHash:  hash,
		RecoverySteps: steps,
	}, nil
}

func verifyGoModuleAfterMerge(ctx context.Context, repoRoot string, cfg config.Config, steps *[]string) (bool, error) {
	if !rootGoModExists(repoRoot) {
		return false, nil
	}
	if err := runGo(ctx, repoRoot, "mod", "tidy"); err != nil {
		tidyErr := err
		if repaired, modules, repairErr := repairUnknownGoModuleRevisions(ctx, repoRoot, tidyErr); repairErr != nil {
			return true, fmt.Errorf("%w; Go module version repair failed: %v", tidyErr, repairErr)
		} else if repaired {
			*steps = append(*steps, "refreshed invalid Go module versions: "+strings.Join(modules, ", "))
			tidyErr = runGo(ctx, repoRoot, "mod", "tidy")
		}
		if tidyErr != nil {
			resolved, reason, aiErr := repairMergeWithAI(ctx, repoRoot, cfg, nil, tidyErr.Error())
			if aiErr != nil {
				return true, fmt.Errorf("%w; AI repair failed: %v", tidyErr, aiErr)
			}
			if !resolved {
				return true, fmt.Errorf("%w; AI could not repair: %s", tidyErr, reason)
			}
			*steps = append(*steps, "repaired go mod tidy failure with AI")
			if err := runGo(ctx, repoRoot, "mod", "tidy"); err != nil {
				return true, err
			}
		}
	}
	*steps = append(*steps, "ran go mod tidy")

	if err := runGo(ctx, repoRoot, "build", "./..."); err != nil {
		buildErr := err
		if repaired, modules, repairErr := repairUnknownGoModuleRevisions(ctx, repoRoot, buildErr); repairErr != nil {
			return true, fmt.Errorf("%w; Go module version repair failed: %v", buildErr, repairErr)
		} else if repaired {
			*steps = append(*steps, "refreshed invalid Go module versions: "+strings.Join(modules, ", "))
			if err := runGo(ctx, repoRoot, "mod", "tidy"); err != nil {
				return true, err
			}
			*steps = append(*steps, "ran go mod tidy after Go module version repair")
			buildErr = runGo(ctx, repoRoot, "build", "./...")
		}
		if buildErr != nil {
			resolved, reason, aiErr := repairMergeWithAI(ctx, repoRoot, cfg, nil, buildErr.Error())
			if aiErr != nil {
				return true, fmt.Errorf("%w; AI repair failed: %v", buildErr, aiErr)
			}
			if !resolved {
				return true, fmt.Errorf("%w; AI could not repair: %s", buildErr, reason)
			}
			*steps = append(*steps, "repaired go build failure with AI")
			if err := runGo(ctx, repoRoot, "mod", "tidy"); err != nil {
				return true, err
			}
			*steps = append(*steps, "ran go mod tidy after AI repair")
			if err := runGo(ctx, repoRoot, "build", "./..."); err != nil {
				return true, err
			}
		}
	}
	*steps = append(*steps, "verified go build ./...")
	return true, nil
}

func rootGoModExists(repoRoot string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, "go.mod"))
	return err == nil && !info.IsDir()
}

func repairUnknownGoModuleRevisions(ctx context.Context, repoRoot string, err error) (bool, []string, error) {
	modules := unknownRevisionModules(err)
	if len(modules) == 0 {
		return false, nil, nil
	}
	for _, module := range modules {
		if err := runGo(ctx, repoRoot, "mod", "edit", "-droprequire="+module); err != nil {
			return false, modules, err
		}
		if err := runGo(ctx, repoRoot, "get", module); err != nil {
			return false, modules, err
		}
	}
	return true, modules, nil
}

func unknownRevisionModules(err error) []string {
	text := err.Error()
	var goErr goCommandError
	if errors.As(err, &goErr) {
		text = goErr.output
	}
	if !strings.Contains(text, "unknown revision") {
		return nil
	}
	re := regexp.MustCompile(`(?m)([A-Za-z0-9][A-Za-z0-9._~!$&'()*+,;=:@%/-]*\.[A-Za-z0-9._~!$&'()*+,;=:@%/-]+)@[^:\s]+:\s+invalid version:\s+unknown revision\b`)
	var modules []string
	seen := map[string]struct{}{}
	for _, match := range re.FindAllStringSubmatch(text, -1) {
		module := strings.TrimSpace(match[1])
		if module == "" {
			continue
		}
		if _, ok := seen[module]; ok {
			continue
		}
		seen[module] = struct{}{}
		modules = append(modules, module)
	}
	return modules
}

func resolveDerivedConflictsWithOurs(ctx context.Context, repoRoot string, paths []string) ([]string, error) {
	var resolved []string
	for _, path := range paths {
		cleaned, err := cleanRelativeRepoPath(path)
		if err != nil {
			return nil, err
		}
		if !isDerivedConflictPath(cleaned) {
			continue
		}
		stages, err := gitx.IndexFileStages(ctx, repoRoot, cleaned)
		if err != nil {
			return nil, err
		}
		fullPath := filepath.Join(repoRoot, filepath.FromSlash(cleaned))
		if _, ok := stages[2]; ok {
			data, err := gitx.IndexFileStage(ctx, repoRoot, 2, cleaned)
			if err != nil {
				return nil, fmt.Errorf("read current branch version for %s: %w", cleaned, err)
			}
			if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(fullPath, data, 0o644); err != nil {
				return nil, err
			}
		} else {
			if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if err := gitx.StagePaths(ctx, repoRoot, []string{cleaned}); err != nil {
			return nil, err
		}
		resolved = append(resolved, cleaned)
	}
	return resolved, nil
}

func isDerivedConflictPath(path string) bool {
	base := filepath.Base(path)
	return path == "go.sum" || strings.HasSuffix(base, ".pb.go")
}

func filterAIRepairPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		cleaned, err := cleanRelativeRepoPath(path)
		if err != nil {
			continue
		}
		if isDerivedConflictPath(cleaned) {
			continue
		}
		out = append(out, cleaned)
	}
	return uniqueStrings(out)
}

func repairMergeWithAI(ctx context.Context, repoRoot string, cfg config.Config, conflictPaths []string, verificationError string) (bool, string, error) {
	conflictPaths = filterAIRepairPaths(conflictPaths)
	repaired, agentReason, agentErr := repairWorkspaceWithAI(ctx, repoRoot, cfg, conflictPaths, verificationError)
	if agentErr == nil && repaired {
		return true, agentReason, nil
	}

	resolved, jsonReason, jsonErr := resolveMergeWithAI(ctx, repoRoot, cfg, conflictPaths, verificationError)
	if jsonErr == nil {
		if resolved {
			return true, jsonReason, nil
		}
		if agentErr != nil {
			if jsonReason != "" {
				return false, "", fmt.Errorf("AI repair agent failed: %v; AI file repair could not resolve: %s", agentErr, jsonReason)
			}
			return false, "", agentErr
		}
		if agentReason != "" {
			return false, agentReason, nil
		}
		return false, jsonReason, nil
	}
	if agentErr == nil {
		return false, agentReason, nil
	}
	return false, "", fmt.Errorf("AI repair agent failed: %v; AI file repair failed: %w", agentErr, jsonErr)
}

func repairWorkspaceWithAI(ctx context.Context, repoRoot string, cfg config.Config, conflictPaths []string, verificationError string) (bool, string, error) {
	beforeHead, err := gitx.FullCommitHash(ctx, repoRoot)
	if err != nil {
		return false, "", err
	}
	var history []ai.RepairAgentObservation
	for step := 0; step < maxRepairAgentSteps; step++ {
		request, err := buildRepairAgentRequest(ctx, repoRoot, conflictPaths, verificationError, history)
		if err != nil {
			return false, "", err
		}
		_, action, _, err := generateAIRepairAction(ctx, cfg.Provider, cfg.Model, cfg.Providers, request)
		if err != nil {
			return false, "", err
		}
		if action.Action == "finish" {
			if err := ensureHeadUnchanged(ctx, repoRoot, beforeHead); err != nil {
				return false, "", err
			}
			if !action.Repaired {
				return false, action.Reason, nil
			}
			return true, action.Reason, nil
		}
		result := executeRepairAgentAction(ctx, repoRoot, action)
		if err := ensureHeadUnchanged(ctx, repoRoot, beforeHead); err != nil {
			return false, "", err
		}
		history = append(history, ai.RepairAgentObservation{
			Action: repairAgentActionSummary(action),
			Result: truncateRepairAgentText(result),
		})
	}
	return false, "", fmt.Errorf("AI repair agent reached %d steps without finishing", maxRepairAgentSteps)
}

const maxRepairAgentSteps = 12

func generateAIRepairAction(ctx context.Context, providerName, model string, providers map[string]config.ProviderConfig, request ai.RepairAgentRequest) (ai.ResolvedProvider, ai.RepairAgentAction, []string, error) {
	if !isAutoProvider(providerName) {
		provider, resolved, err := ai.NewProvider(ai.FactoryConfig{
			Provider:  providerName,
			Model:     model,
			Providers: providers,
		})
		if err != nil {
			return ai.ResolvedProvider{}, ai.RepairAgentAction{}, nil, err
		}
		action, err := provider.GenerateRepairAction(ctx, request)
		if err != nil {
			return ai.ResolvedProvider{}, ai.RepairAgentAction{}, nil, err
		}
		return resolved, action, nil, nil
	}

	var failures []string
	for _, candidate := range ai.AutoProviderCandidates(providers) {
		provider, resolved, err := ai.NewProvider(ai.FactoryConfig{
			Provider:  candidate,
			Model:     model,
			Providers: providers,
		})
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		action, err := provider.GenerateRepairAction(ctx, request)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", resolved.Name, err))
			continue
		}
		return resolved, action, autoFallbackWarnings(failures, resolved.Name), nil
	}
	if len(failures) == 0 {
		return ai.ResolvedProvider{}, ai.RepairAgentAction{}, nil, errors.New("no auto providers are available")
	}
	return ai.ResolvedProvider{}, ai.RepairAgentAction{}, nil, fmt.Errorf("all repair agent providers failed: %s", strings.Join(failures, "; "))
}

func buildRepairAgentRequest(ctx context.Context, repoRoot string, conflictPaths []string, verificationError string, history []ai.RepairAgentObservation) (ai.RepairAgentRequest, error) {
	status, err := gitx.StatusShort(ctx, repoRoot)
	if err != nil {
		return ai.RepairAgentRequest{}, err
	}
	return ai.RepairAgentRequest{
		RepoRoot:          repoRoot,
		ConflictPaths:     uniqueStrings(conflictPaths),
		VerificationError: verificationError,
		Status:            filterAIRepairStatus(status),
		History:           history,
		Note:              "aicommit will rerun go mod tidy/go build when applicable, then create the merge commit and push if verification passes.",
	}, nil
}

func filterAIRepairStatus(status string) string {
	var lines []string
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if statusLineMentionsDerivedPath(line) {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func statusLineMentionsDerivedPath(line string) bool {
	if len(line) < 4 {
		return false
	}
	pathText := strings.TrimSpace(line[3:])
	for _, part := range strings.Split(pathText, " -> ") {
		cleaned, err := cleanRelativeRepoPath(part)
		if err == nil && isDerivedConflictPath(cleaned) {
			return true
		}
	}
	return false
}

func executeRepairAgentAction(ctx context.Context, repoRoot string, action ai.RepairAgentAction) string {
	switch action.Action {
	case "read_file":
		if err := rejectDerivedAgentPath(action.Path); err != nil {
			return "ERROR: " + err.Error()
		}
		fullPath, err := repairAgentPath(repoRoot, action.Path, false)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		return textForPrompt(data)
	case "write_file":
		if err := rejectDerivedAgentPath(action.Path); err != nil {
			return "ERROR: " + err.Error()
		}
		fullPath, err := repairAgentWritablePath(repoRoot, action.Path)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return "ERROR: " + err.Error()
		}
		if err := os.WriteFile(fullPath, []byte(action.Content), 0o644); err != nil {
			return "ERROR: " + err.Error()
		}
		return fmt.Sprintf("OK: wrote %s (%d bytes)", action.Path, len(action.Content))
	case "delete_file":
		if err := rejectDerivedAgentPath(action.Path); err != nil {
			return "ERROR: " + err.Error()
		}
		fullPath, err := repairAgentPath(repoRoot, action.Path, false)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "ERROR: " + err.Error()
		}
		return "OK: deleted " + action.Path
	case "list_files":
		fullPath, err := repairAgentPath(repoRoot, action.Path, true)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		entries, err := os.ReadDir(fullPath)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		var names []string
		for _, entry := range entries {
			name := entry.Name()
			relName := name
			if strings.TrimSpace(action.Path) != "" && strings.TrimSpace(action.Path) != "." {
				relName = filepath.ToSlash(filepath.Join(action.Path, name))
			}
			if cleaned, err := cleanRelativeRepoPath(relName); err == nil && isDerivedConflictPath(cleaned) {
				continue
			}
			if entry.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		sort.Strings(names)
		return strings.Join(names, "\n")
	case "run_command":
		return runRepairAgentCommand(ctx, repoRoot, action)
	default:
		return "ERROR: unsupported action " + action.Action
	}
}

func rejectDerivedAgentPath(path string) error {
	cleaned, err := cleanRelativeRepoPath(path)
	if err != nil {
		return err
	}
	if isDerivedConflictPath(cleaned) {
		return fmt.Errorf("%s is generated/derived and is not available to AI repair", cleaned)
	}
	return nil
}

func runRepairAgentCommand(ctx context.Context, repoRoot string, action ai.RepairAgentAction) string {
	if err := validateRepairAgentCommand(action); err != nil {
		return "ERROR: " + err.Error()
	}
	commandCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, action.Command, action.Args...)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		if result == "" {
			result = err.Error()
		} else {
			result = err.Error() + "\n" + result
		}
		return "ERROR: " + truncateRepairAgentText(result)
	}
	if result == "" {
		return "OK"
	}
	return "OK:\n" + truncateRepairAgentText(result)
}

func validateRepairAgentCommand(action ai.RepairAgentAction) error {
	switch action.Command {
	case "go":
		return validateRepairAgentGoArgs(action.Args)
	case "git":
		return validateRepairAgentGitArgs(action.Args)
	default:
		return fmt.Errorf("command %q is not allowed", action.Command)
	}
}

func validateRepairAgentGoArgs(args []string) error {
	if len(args) == 0 {
		return errors.New("go command requires arguments")
	}
	switch args[0] {
	case "version", "list", "build", "test":
		return nil
	case "get":
		if len(args) < 2 {
			return errors.New("go get requires at least one module")
		}
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("go get option %q is not allowed", arg)
			}
			if arg == "." || arg == "./..." || strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") {
				return fmt.Errorf("go get target %q is not allowed", arg)
			}
		}
		return nil
	case "env":
		for _, arg := range args[1:] {
			if arg == "-w" || strings.HasPrefix(arg, "-w=") || arg == "-u" || strings.HasPrefix(arg, "-u=") {
				return fmt.Errorf("go env mutation flag %q is not allowed", arg)
			}
		}
		return nil
	case "mod":
		if len(args) < 2 {
			return errors.New("go mod command requires a subcommand")
		}
		switch args[1] {
		case "tidy", "graph", "why", "download", "edit":
			return nil
		default:
			return fmt.Errorf("go mod %s is not allowed", args[1])
		}
	default:
		return fmt.Errorf("go %s is not allowed", args[0])
	}
}

func validateRepairAgentGitArgs(args []string) error {
	if len(args) == 0 {
		return errors.New("git command requires arguments")
	}
	switch args[0] {
	case "status", "ls-files":
		for _, arg := range args[1:] {
			if arg == "--output" ||
				strings.HasPrefix(arg, "--output=") ||
				arg == "--no-index" ||
				arg == "--ext-diff" ||
				strings.HasPrefix(arg, "--ext-diff=") ||
				arg == "--external-diff" ||
				arg == "--textconv" {
				return fmt.Errorf("git %s option %q is not allowed", args[0], arg)
			}
			if !strings.HasPrefix(arg, "-") {
				if cleaned, err := cleanRelativeRepoPath(arg); err == nil && isDerivedConflictPath(cleaned) {
					return fmt.Errorf("git %s path %q is generated/derived and is not available to AI repair", args[0], arg)
				}
			}
		}
		return nil
	case "diff":
		if !repairAgentGitDiffHasPathspec(args[1:]) {
			return errors.New("git diff requires an explicit pathspec after --")
		}
		for _, arg := range args[1:] {
			if arg == "--output" ||
				strings.HasPrefix(arg, "--output=") ||
				arg == "--no-index" ||
				arg == "--ext-diff" ||
				strings.HasPrefix(arg, "--ext-diff=") ||
				arg == "--external-diff" ||
				arg == "--textconv" {
				return fmt.Errorf("git diff option %q is not allowed", arg)
			}
			if !strings.HasPrefix(arg, "-") {
				if cleaned, err := cleanRelativeRepoPath(arg); err == nil && isDerivedConflictPath(cleaned) {
					return fmt.Errorf("git diff path %q is generated/derived and is not available to AI repair", arg)
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("git %s is not allowed", args[0])
	}
}

func repairAgentGitDiffHasPathspec(args []string) bool {
	for i, arg := range args {
		if arg != "--" {
			continue
		}
		for _, path := range args[i+1:] {
			if strings.TrimSpace(path) == "" {
				continue
			}
			cleaned, err := cleanRelativeRepoPath(path)
			if err == nil && !isDerivedConflictPath(cleaned) {
				return true
			}
		}
		return false
	}
	return false
}

func repairAgentPath(repoRoot, path string, allowRoot bool) (string, error) {
	path = strings.TrimSpace(path)
	if allowRoot && (path == "" || path == ".") {
		return repoRoot, nil
	}
	cleaned, err := cleanRelativeRepoPath(path)
	if err != nil {
		return "", err
	}
	fullPath := filepath.Join(repoRoot, filepath.FromSlash(cleaned))
	if err := ensureExistingPathInsideRepo(repoRoot, fullPath); err != nil {
		return "", err
	}
	return fullPath, nil
}

func repairAgentWritablePath(repoRoot, path string) (string, error) {
	cleaned, err := cleanRelativeRepoPath(path)
	if err != nil {
		return "", err
	}
	fullPath := filepath.Join(repoRoot, filepath.FromSlash(cleaned))
	if info, err := os.Lstat(fullPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("cannot edit symlink %q", cleaned)
	}
	if err := ensureParentInsideRepo(repoRoot, fullPath); err != nil {
		return "", err
	}
	return fullPath, nil
}

func ensureExistingPathInsideRepo(repoRoot, fullPath string) error {
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return err
	}
	return ensurePathInsideRepo(repoRoot, resolved)
}

func ensureParentInsideRepo(repoRoot, fullPath string) error {
	parent := filepath.Dir(fullPath)
	for {
		if info, err := os.Lstat(parent); err == nil {
			if !info.IsDir() {
				return fmt.Errorf("parent path is not a directory: %s", parent)
			}
			resolved, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return err
			}
			return ensurePathInsideRepo(repoRoot, resolved)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return fmt.Errorf("no existing parent directory for %s", fullPath)
		}
		parent = next
	}
}

func ensurePathInsideRepo(repoRoot, path string) error {
	repoRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes repository: %s", path)
	}
	return nil
}

func ensureHeadUnchanged(ctx context.Context, repoRoot, beforeHead string) error {
	afterHead, err := gitx.FullCommitHash(ctx, repoRoot)
	if err != nil {
		return err
	}
	if beforeHead != afterHead {
		return errors.New("AI repair changed HEAD; refusing to continue automatic push recovery")
	}
	return nil
}

func repairAgentActionSummary(action ai.RepairAgentAction) string {
	if action.Content != "" {
		action.Content = fmt.Sprintf("[content omitted: %d bytes]", len(action.Content))
	}
	data, err := json.Marshal(action)
	if err != nil {
		return action.Action
	}
	return string(data)
}

func truncateRepairAgentText(text string) string {
	const maxRepairAgentResultChars = 24_000
	if len(text) <= maxRepairAgentResultChars {
		return text
	}
	return text[:maxRepairAgentResultChars] + "\n[truncated]\n"
}

func resolveMergeWithAI(ctx context.Context, repoRoot string, cfg config.Config, conflictPaths []string, verificationError string) (bool, string, error) {
	request, allowed, err := buildMergeResolutionRequest(ctx, repoRoot, conflictPaths, verificationError)
	if err != nil {
		return false, "", err
	}
	_, result, _, err := generateAIMergeResolution(ctx, cfg.Provider, cfg.Model, cfg.Providers, request)
	if err != nil {
		return false, "", err
	}
	if !result.CanResolve {
		return false, result.Reason, nil
	}
	if err := applyAIMergeResolution(repoRoot, result, allowed); err != nil {
		return false, result.Reason, err
	}
	return true, result.Reason, nil
}

func generateAIMergeResolution(ctx context.Context, providerName, model string, providers map[string]config.ProviderConfig, request ai.MergeResolutionRequest) (ai.ResolvedProvider, ai.MergeResolutionResult, []string, error) {
	if !isAutoProvider(providerName) {
		provider, resolved, err := ai.NewProvider(ai.FactoryConfig{
			Provider:  providerName,
			Model:     model,
			Providers: providers,
		})
		if err != nil {
			return ai.ResolvedProvider{}, ai.MergeResolutionResult{}, nil, err
		}
		result, err := provider.GenerateMergeResolution(ctx, request)
		if err != nil {
			return ai.ResolvedProvider{}, ai.MergeResolutionResult{}, nil, err
		}
		return resolved, result, nil, nil
	}

	var failures []string
	for _, candidate := range ai.AutoProviderCandidates(providers) {
		provider, resolved, err := ai.NewProvider(ai.FactoryConfig{
			Provider:  candidate,
			Model:     model,
			Providers: providers,
		})
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		result, err := provider.GenerateMergeResolution(ctx, request)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", resolved.Name, err))
			continue
		}
		return resolved, result, autoFallbackWarnings(failures, resolved.Name), nil
	}
	if len(failures) == 0 {
		return ai.ResolvedProvider{}, ai.MergeResolutionResult{}, nil, errors.New("no auto providers are available")
	}
	return ai.ResolvedProvider{}, ai.MergeResolutionResult{}, nil, fmt.Errorf("all auto providers failed: %s", strings.Join(failures, "; "))
}

func buildMergeResolutionRequest(ctx context.Context, repoRoot string, conflictPaths []string, verificationError string) (ai.MergeResolutionRequest, map[string]struct{}, error) {
	paths := filterAIRepairPaths(conflictPaths)
	if len(paths) == 0 {
		changed, err := gitx.StatusPaths(ctx, repoRoot)
		if err != nil {
			return ai.MergeResolutionRequest{}, nil, err
		}
		paths = filterAIRepairPaths(changed)
	}
	allowed := map[string]struct{}{}
	files := make([]ai.MergeFileContext, 0, len(paths))
	for _, path := range paths {
		cleaned, err := cleanRelativeRepoPath(path)
		if err != nil {
			continue
		}
		allowed[cleaned] = struct{}{}
		ctxFile := ai.MergeFileContext{
			Path:    cleaned,
			Status:  "changed",
			Current: readRepoTextFile(repoRoot, cleaned),
		}
		if stringSliceContains(conflictPaths, path) {
			ctxFile.Status = "conflict"
			if data, ok := conflictStageText(ctx, repoRoot, cleaned, 1); ok {
				ctxFile.Base = data
			}
			if data, ok := conflictStageText(ctx, repoRoot, cleaned, 2); ok {
				ctxFile.Ours = data
			}
			if data, ok := conflictStageText(ctx, repoRoot, cleaned, 3); ok {
				ctxFile.Theirs = data
			}
		}
		files = append(files, ctxFile)
	}
	if len(files) == 0 && rootGoModExists(repoRoot) {
		allowed["go.mod"] = struct{}{}
		files = append(files, ai.MergeFileContext{
			Path:    "go.mod",
			Status:  "current",
			Current: readRepoTextFile(repoRoot, "go.mod"),
		})
	}
	return ai.MergeResolutionRequest{
		RepoRoot:          repoRoot,
		Files:             files,
		VerificationError: verificationError,
		Note:              "aicommit will only write files listed in this context, then rerun verification before committing.",
	}, allowed, nil
}

func conflictStageText(ctx context.Context, repoRoot, path string, stage int) (string, bool) {
	stages, err := gitx.IndexFileStages(ctx, repoRoot, path)
	if err != nil {
		return "", false
	}
	if _, ok := stages[stage]; !ok {
		return "", false
	}
	data, err := gitx.IndexFileStage(ctx, repoRoot, stage, path)
	if err != nil {
		return "", false
	}
	return textForPrompt(data), true
}

func readRepoTextFile(repoRoot, path string) string {
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
	if err != nil {
		return ""
	}
	return textForPrompt(data)
}

func textForPrompt(data []byte) string {
	if strings.ContainsRune(string(data), '\x00') {
		return "[binary file omitted]"
	}
	return string(data)
}

func applyAIMergeResolution(repoRoot string, result ai.MergeResolutionResult, allowed map[string]struct{}) error {
	if len(result.Files) == 0 {
		return errors.New("AI said it can resolve the merge but returned no files")
	}
	for _, file := range result.Files {
		path, err := cleanRelativeRepoPath(file.Path)
		if err != nil {
			return err
		}
		if isDerivedConflictPath(path) {
			return fmt.Errorf("AI tried to edit generated/derived file %q", path)
		}
		if len(allowed) > 0 {
			if _, ok := allowed[path]; !ok {
				return fmt.Errorf("AI tried to edit %q, which was not part of the merge context", path)
			}
		}
		fullPath := filepath.Join(repoRoot, filepath.FromSlash(path))
		if info, err := os.Lstat(fullPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("AI tried to edit symlink %q", path)
		}
		if file.Delete {
			if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(fullPath, []byte(file.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func cleanRelativeRepoPath(path string) (string, error) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute path is not allowed: %s", path)
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", fmt.Errorf("path escapes repository: %s", path)
	}
	if cleaned == ".git" || strings.HasPrefix(cleaned, ".git/") || strings.Contains(cleaned, "/.git/") || strings.HasSuffix(cleaned, "/.git") {
		return "", fmt.Errorf("path inside .git is not allowed: %s", path)
	}
	return cleaned, nil
}

func onlyRootGoModuleConflicts(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		if path != "go.mod" && path != "go.sum" {
			return false
		}
	}
	return true
}

func uniqueStrings(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func resolveGoModuleConflicts(ctx context.Context, repoRoot string, paths []string) error {
	if stringSliceContains(paths, "go.mod") {
		ours, err := gitx.IndexFileStage(ctx, repoRoot, 2, "go.mod")
		if err != nil {
			return fmt.Errorf("read local go.mod conflict stage: %w", err)
		}
		theirs, err := gitx.IndexFileStage(ctx, repoRoot, 3, "go.mod")
		if err != nil {
			return fmt.Errorf("read remote go.mod conflict stage: %w", err)
		}
		merged, err := mergeGoModContents(ctx, ours, theirs)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), merged, 0o644); err != nil {
			return err
		}
	}
	if stringSliceContains(paths, "go.sum") {
		contents, err := conflictStageContents(ctx, repoRoot, "go.sum", []int{1, 2, 3})
		if err != nil {
			return err
		}
		merged := mergeGoSumContents(contents)
		path := filepath.Join(repoRoot, "go.sum")
		if len(merged) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if err := os.WriteFile(path, merged, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func conflictStageContents(ctx context.Context, repoRoot, path string, stages []int) ([][]byte, error) {
	available, err := gitx.IndexFileStages(ctx, repoRoot, path)
	if err != nil {
		return nil, err
	}
	var contents [][]byte
	for _, stage := range stages {
		if _, ok := available[stage]; !ok {
			continue
		}
		data, err := gitx.IndexFileStage(ctx, repoRoot, stage, path)
		if err != nil {
			return nil, err
		}
		contents = append(contents, data)
	}
	return contents, nil
}

func mergeGoSumContents(contents [][]byte) []byte {
	lines := map[string]struct{}{}
	for _, data := range contents {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lines[line] = struct{}{}
		}
	}
	merged := make([]string, 0, len(lines))
	for line := range lines {
		merged = append(merged, line)
	}
	sort.Strings(merged)
	if len(merged) == 0 {
		return nil
	}
	return []byte(strings.Join(merged, "\n") + "\n")
}

type goModFile struct {
	Module    goModModule    `json:"Module"`
	Go        string         `json:"Go"`
	Toolchain string         `json:"Toolchain"`
	Godebug   []goModGodebug `json:"Godebug"`
	Require   []goModRequire `json:"Require"`
	Exclude   []goModModule  `json:"Exclude"`
	Replace   []goModReplace `json:"Replace"`
	Retract   []goModRetract `json:"Retract"`
	Tool      []goModTool    `json:"Tool"`
	Ignore    []goModIgnore  `json:"Ignore"`
}

type goModModule struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
}

type goModRequire struct {
	Path     string `json:"Path"`
	Version  string `json:"Version"`
	Indirect bool   `json:"Indirect"`
}

type goModReplace struct {
	Old goModModule `json:"Old"`
	New goModModule `json:"New"`
}

type goModRetract struct {
	Low       string `json:"Low"`
	High      string `json:"High"`
	Rationale string `json:"Rationale"`
}

type goModGodebug struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type goModTool struct {
	Path string `json:"Path"`
}

type goModIgnore struct {
	Path string `json:"Path"`
}

func mergeGoModContents(ctx context.Context, ours, theirs []byte) ([]byte, error) {
	oursMod, err := parseGoMod(ctx, ours)
	if err != nil {
		return nil, fmt.Errorf("parse local go.mod: %w", err)
	}
	theirsMod, err := parseGoMod(ctx, theirs)
	if err != nil {
		return nil, fmt.Errorf("parse remote go.mod: %w", err)
	}
	merged, err := mergeGoModFiles(oursMod, theirsMod)
	if err != nil {
		return nil, err
	}
	return renderGoMod(ctx, merged)
}

func parseGoMod(ctx context.Context, data []byte) (goModFile, error) {
	dir, err := os.MkdirTemp("", "aicommit-gomod-parse-*")
	if err != nil {
		return goModFile{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return goModFile{}, err
	}
	cmd := exec.CommandContext(ctx, "go", "mod", "edit", "-json", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return goModFile{}, fmt.Errorf("go mod edit -json: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var parsed goModFile
	if err := json.Unmarshal(out, &parsed); err != nil {
		return goModFile{}, err
	}
	if parsed.Module.Path == "" {
		return goModFile{}, errors.New("module path is empty")
	}
	return parsed, nil
}

func mergeGoModFiles(ours, theirs goModFile) (goModFile, error) {
	if ours.Module.Path != theirs.Module.Path {
		return goModFile{}, fmt.Errorf("go.mod module path differs: local %q, remote %q", ours.Module.Path, theirs.Module.Path)
	}
	merged := goModFile{
		Module:    ours.Module,
		Go:        higherGoVersion(ours.Go, theirs.Go),
		Toolchain: higherGoVersion(ours.Toolchain, theirs.Toolchain),
	}
	merged.Require = mergeGoModRequires(ours.Require, theirs.Require)
	merged.Exclude = mergeGoModModules(ours.Exclude, theirs.Exclude)
	replaces, err := mergeGoModReplaces(ours.Replace, theirs.Replace)
	if err != nil {
		return goModFile{}, err
	}
	merged.Replace = replaces
	merged.Retract = mergeGoModRetracts(ours.Retract, theirs.Retract)
	merged.Godebug = mergeGoModGodebug(ours.Godebug, theirs.Godebug)
	merged.Tool = mergeGoModTools(ours.Tool, theirs.Tool)
	merged.Ignore = mergeGoModIgnores(ours.Ignore, theirs.Ignore)
	return merged, nil
}

func mergeGoModRequires(groups ...[]goModRequire) []goModRequire {
	byPath := map[string]goModRequire{}
	for _, group := range groups {
		for _, item := range group {
			if item.Path == "" || item.Version == "" {
				continue
			}
			existing, ok := byPath[item.Path]
			if !ok {
				byPath[item.Path] = item
				continue
			}
			if compareModuleVersion(item.Version, existing.Version) >= 0 {
				existing.Version = item.Version
			}
			existing.Indirect = existing.Indirect && item.Indirect
			byPath[item.Path] = existing
		}
	}
	out := make([]goModRequire, 0, len(byPath))
	for _, item := range byPath {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func mergeGoModModules(groups ...[]goModModule) []goModModule {
	seen := map[string]goModModule{}
	for _, group := range groups {
		for _, item := range group {
			if item.Path == "" {
				continue
			}
			seen[moduleKey(item)] = item
		}
	}
	out := make([]goModModule, 0, len(seen))
	for _, item := range seen {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return moduleKey(out[i]) < moduleKey(out[j]) })
	return out
}

func mergeGoModReplaces(groups ...[]goModReplace) ([]goModReplace, error) {
	byOld := map[string]goModReplace{}
	for _, group := range groups {
		for _, item := range group {
			if item.Old.Path == "" || item.New.Path == "" {
				continue
			}
			key := moduleKey(item.Old)
			existing, ok := byOld[key]
			if ok && moduleKey(existing.New) != moduleKey(item.New) {
				return nil, fmt.Errorf("go.mod replace conflict for %s: %s vs %s", moduleFlag(item.Old), moduleFlag(existing.New), moduleFlag(item.New))
			}
			byOld[key] = item
		}
	}
	out := make([]goModReplace, 0, len(byOld))
	for _, item := range byOld {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return moduleKey(out[i].Old) < moduleKey(out[j].Old) })
	return out, nil
}

func mergeGoModRetracts(groups ...[]goModRetract) []goModRetract {
	seen := map[string]goModRetract{}
	for _, group := range groups {
		for _, item := range group {
			if item.Low == "" {
				continue
			}
			key := item.Low + "\x00" + item.High
			if existing, ok := seen[key]; ok && existing.Rationale != "" {
				continue
			}
			seen[key] = item
		}
	}
	out := make([]goModRetract, 0, len(seen))
	for _, item := range seen {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Low == out[j].Low {
			return out[i].High < out[j].High
		}
		return out[i].Low < out[j].Low
	})
	return out
}

func mergeGoModGodebug(ours, theirs []goModGodebug) []goModGodebug {
	byKey := map[string]goModGodebug{}
	for _, item := range ours {
		if item.Key != "" {
			byKey[item.Key] = item
		}
	}
	for _, item := range theirs {
		if item.Key != "" {
			byKey[item.Key] = item
		}
	}
	out := make([]goModGodebug, 0, len(byKey))
	for _, item := range byKey {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func mergeGoModTools(groups ...[]goModTool) []goModTool {
	seen := map[string]goModTool{}
	for _, group := range groups {
		for _, item := range group {
			if item.Path != "" {
				seen[item.Path] = item
			}
		}
	}
	out := make([]goModTool, 0, len(seen))
	for _, item := range seen {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func mergeGoModIgnores(groups ...[]goModIgnore) []goModIgnore {
	seen := map[string]goModIgnore{}
	for _, group := range groups {
		for _, item := range group {
			if item.Path != "" {
				seen[item.Path] = item
			}
		}
	}
	out := make([]goModIgnore, 0, len(seen))
	for _, item := range seen {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func renderGoMod(ctx context.Context, mod goModFile) ([]byte, error) {
	dir, err := os.MkdirTemp("", "aicommit-gomod-render-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "go.mod")
	base := fmt.Sprintf("module %s\n", mod.Module.Path)
	if mod.Go != "" {
		base += "\ngo " + mod.Go + "\n"
	}
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		return nil, err
	}

	var flags []string
	if mod.Toolchain != "" {
		flags = append(flags, "-toolchain="+mod.Toolchain)
	}
	for _, item := range mod.Godebug {
		flags = append(flags, "-godebug="+item.Key+"="+item.Value)
	}
	for _, item := range mod.Require {
		flags = append(flags, "-require="+item.Path+"@"+item.Version)
	}
	for _, item := range mod.Exclude {
		flags = append(flags, "-exclude="+moduleFlag(item))
	}
	for _, item := range mod.Replace {
		flags = append(flags, "-replace="+moduleFlag(item.Old)+"="+moduleFlag(item.New))
	}
	for _, item := range mod.Retract {
		flags = append(flags, "-retract="+retractFlag(item))
	}
	for _, item := range mod.Tool {
		flags = append(flags, "-tool="+item.Path)
	}
	for _, item := range mod.Ignore {
		flags = append(flags, "-ignore="+item.Path)
	}
	if len(flags) > 0 {
		args := append([]string{"mod", "edit"}, flags...)
		args = append(args, path)
		if err := runGo(ctx, "", args...); err != nil {
			return nil, err
		}
	}
	if err := runGo(ctx, "", "mod", "edit", "-fmt", path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func moduleKey(module goModModule) string {
	return module.Path + "\x00" + module.Version
}

func moduleFlag(module goModModule) string {
	if module.Version == "" {
		return module.Path
	}
	return module.Path + "@" + module.Version
}

func retractFlag(item goModRetract) string {
	if item.High == "" || item.High == item.Low {
		return item.Low
	}
	return "[" + item.Low + "," + item.High + "]"
}

func higherGoVersion(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if compareDottedVersion(strings.TrimPrefix(a, "go"), strings.TrimPrefix(b, "go")) >= 0 {
		return a
	}
	return b
}

func compareModuleVersion(a, b string) int {
	if a == b {
		return 0
	}
	cmp := compareDottedVersion(strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v"))
	if cmp != 0 {
		return cmp
	}
	aSuffix := versionSuffix(a)
	bSuffix := versionSuffix(b)
	if aSuffix == bSuffix {
		return strings.Compare(a, b)
	}
	if aSuffix == "" {
		return 1
	}
	if bSuffix == "" {
		return -1
	}
	return strings.Compare(aSuffix, bSuffix)
}

func compareDottedVersion(a, b string) int {
	aNums := leadingVersionNumbers(a)
	bNums := leadingVersionNumbers(b)
	maxLen := len(aNums)
	if len(bNums) > maxLen {
		maxLen = len(bNums)
	}
	for i := 0; i < maxLen; i++ {
		var av, bv int
		if i < len(aNums) {
			av = aNums[i]
		}
		if i < len(bNums) {
			bv = bNums[i]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return 0
}

func leadingVersionNumbers(version string) []int {
	var numbers []int
	for _, part := range strings.Split(version, ".") {
		if part == "" {
			break
		}
		end := 0
		for end < len(part) && part[end] >= '0' && part[end] <= '9' {
			end++
		}
		if end == 0 {
			break
		}
		value, err := strconv.Atoi(part[:end])
		if err != nil {
			break
		}
		numbers = append(numbers, value)
		if end < len(part) {
			break
		}
	}
	return numbers
}

func versionSuffix(version string) string {
	if i := strings.IndexByte(version, '-'); i >= 0 {
		return version[i:]
	}
	return ""
}

type goCommandError struct {
	args   []string
	output string
	err    error
}

func (e goCommandError) Error() string {
	out := strings.TrimSpace(e.output)
	if out == "" {
		return fmt.Sprintf("go %s: %v", strings.Join(e.args, " "), e.err)
	}
	return fmt.Sprintf("go %s: %v: %s", strings.Join(e.args, " "), e.err, out)
}

func (e goCommandError) Unwrap() error {
	return e.err
}

func runGo(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "go", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return goCommandError{args: append([]string{}, args...), output: string(out), err: err}
	}
	return nil
}

func stringSliceContains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
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

func shouldAutoIgnoreDecision(decision filter.Decision) bool {
	if decision.Allowed {
		return false
	}
	switch decision.Reason {
	case "ignored by .gitignore", "file is larger than maxFileBytes":
		return false
	default:
		return true
	}
}

func nextTag(tags []string) (string, string, error) {
	for _, tag := range tags {
		next, ok := incrementTrailingNumber(tag)
		if ok {
			return next, tag, nil
		}
	}
	return "v0.0.1", "", nil
}

func latestNumericTag(tags []string) string {
	for _, tag := range tags {
		if _, ok := incrementTrailingNumber(tag); ok {
			return tag
		}
	}
	return ""
}

func incrementTrailingNumber(tag string) (string, bool) {
	if tag == "" {
		return "", false
	}
	end := len(tag)
	start := end
	for start > 0 && tag[start-1] >= '0' && tag[start-1] <= '9' {
		start--
	}
	if start == end {
		return "", false
	}
	number, err := strconv.Atoi(tag[start:end])
	if err != nil {
		return "", false
	}
	return tag[:start] + strconv.Itoa(number+1), true
}
