package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/CoolBanHub/aicommit/internal/app"
	"github.com/CoolBanHub/aicommit/internal/config"
	"github.com/CoolBanHub/aicommit/internal/server"
)

var version = "dev"

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	if value == "" {
		return nil
	}
	*m = append(*m, value)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "aicommit: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printRootUsage()
		return nil
	}

	switch args[0] {
	case "commit":
		return runCommit(ctx, args[1:])
	case "push":
		return runPush(ctx, args[1:])
	case "serve":
		return runServe(ctx, args[1:])
	case "version":
		fmt.Println(version)
		return nil
	case "-h", "--help", "help":
		printRootUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runCommit(ctx context.Context, args []string) error {
	var includes multiFlag
	var excludes multiFlag
	var opts app.CommitOptions
	var outputJSON bool
	var push bool
	var noPush bool
	var gpgSign bool

	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.Repo, "repo", ".", "target git repository")
	fs.StringVar(&opts.ConfigPath, "config", "", "config file path, defaults to .aicommit.json or ~/.aicommit.json")
	fs.StringVar(&opts.Provider, "provider", "", "provider: auto, openai, deepseek, anthropic, codex, claude-code, cdp, command")
	fs.StringVar(&opts.Model, "model", "", "provider model override")
	fs.StringVar(&opts.Message, "message", "", "commit message override; skips AI generation")
	fs.StringVar(&opts.Style, "style", "", "extra commit-message style instruction")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "stage and generate the message, but do not commit or push")
	fs.BoolVar(&outputJSON, "json", false, "print JSON result")
	fs.BoolVar(&push, "push", false, "always push after a successful commit")
	fs.BoolVar(&noPush, "no-push", false, "never push after a successful commit")
	fs.BoolVar(&gpgSign, "gpg-sign", false, "respect git commit signing instead of disabling gpgsign")
	fs.IntVar(&opts.MaxDiffChars, "max-diff-chars", 0, "maximum cached diff characters sent to AI")
	fs.Int64Var(&opts.MaxFileBytes, "max-file-bytes", 0, "maximum file size staged by default")
	fs.Var(&includes, "include", "glob to force-include a path; repeatable")
	fs.Var(&excludes, "exclude", "glob to exclude a path; repeatable")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if push && noPush {
		return errors.New("--push and --no-push cannot be used together")
	}
	if push {
		opts.PushMode = config.PushAlways
	}
	if noPush {
		opts.PushMode = config.PushNever
	}
	if gpgSign {
		opts.DisableGPGSign = app.BoolPtr(false)
	}
	opts.IncludePatterns = includes
	opts.ExcludePatterns = excludes

	result, err := app.RunCommit(ctx, opts)
	if err != nil {
		return err
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if result.NoChanges {
		fmt.Println("No allowed changes to commit.")
		printSkipped(result)
		return nil
	}
	if result.DryRun {
		fmt.Printf("Dry run message: %s\n", result.Message)
	} else {
		fmt.Printf("Committed %s: %s\n", result.CommitHash, result.Message)
	}
	if len(result.Files) > 0 {
		fmt.Printf("Files: %d\n", len(result.Files))
	}
	printSkipped(result)
	if result.Pushed {
		fmt.Printf("Pushed to %s\n", result.PushTarget)
	} else if result.PushSkippedReason != "" {
		fmt.Printf("Push skipped: %s\n", result.PushSkippedReason)
	}
	return nil
}

func runPush(ctx context.Context, args []string) error {
	var opts app.PushOptions
	var outputJSON bool

	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.Repo, "repo", ".", "target git repository")
	fs.BoolVar(&outputJSON, "json", false, "print JSON result")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	result, err := app.RunPush(ctx, opts)
	if err != nil {
		return err
	}
	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	if result.Pushed {
		fmt.Printf("Pushed to %s\n", result.Target)
	} else if result.SkippedReason != "" {
		fmt.Printf("Push skipped: %s\n", result.SkippedReason)
	} else {
		fmt.Println("Nothing pushed.")
	}
	return nil
}

func runServe(ctx context.Context, args []string) error {
	var addr string
	var configPath string
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&addr, "addr", "127.0.0.1:8686", "HTTP listen address")
	fs.StringVar(&configPath, "config", "", "default config path for requests")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return server.Run(ctx, server.Options{Addr: addr, ConfigPath: configPath})
}

func printSkipped(result app.CommitResult) {
	if len(result.Skipped) == 0 {
		return
	}
	fmt.Printf("Skipped protected files: %d\n", len(result.Skipped))
	for _, skipped := range result.Skipped {
		fmt.Printf("  - %s (%s)\n", skipped.Path, skipped.Reason)
	}
}

func printRootUsage() {
	fmt.Print(`aicommit commits AI-generated code with an AI-written commit message.

Usage:
  aicommit commit [flags]
  aicommit push [flags]
  aicommit serve [flags]
  aicommit version

Common examples:
  aicommit commit
  aicommit commit --provider openai --model gpt-5.4-mini
  aicommit commit --provider codex
  aicommit push
  aicommit serve --addr 127.0.0.1:8686
`)
}
