# aicommit

`aicommit` is a small Go tool for the last mile after an AI agent changes code:
stage safe files, generate a commit message from the staged diff, commit, and
push separately when you are ready.

## Build

```bash
go build -o aicommit ./cmd/aicommit
```

## CLI

```bash
./aicommit commit
./aicommit commit --provider openai --model gpt-5.4-mini
./aicommit commit --provider deepseek
./aicommit commit --provider anthropic
./aicommit commit --provider codex
./aicommit commit --provider claude-code --dry-run
./aicommit commit --provider cdp
./aicommit push
```

By default, `commit`:

1. Detects the target git repository, or runs `git init` if none exists.
2. Creates `.gitignore` when missing and adds the default protected patterns.
3. Removes protected files from the index if they were already staged.
4. Stages allowed changed files.
5. Sends the cached diff to the selected AI provider.
6. Runs `git commit -m <generated message>`.

It does not push by default. Use `aicommit push` to push all local commits on
the current branch. Set `push: auto` or `push: always` in the config if you want
`aicommit commit` to push automatically.

## Service Mode

```bash
./aicommit serve --addr 127.0.0.1:8686
curl -X POST http://127.0.0.1:8686/commit \
  -H 'content-type: application/json' \
  -d '{"repo":"/path/to/repo","provider":"codex","dryRun":true}'
curl -X POST http://127.0.0.1:8686/push \
  -H 'content-type: application/json' \
  -d '{"repo":"/path/to/repo"}'
```

## Providers

Supported provider names:

- `openai`: OpenAI-compatible chat completions with `OPENAI_API_KEY`.
- `deepseek`: DeepSeek OpenAI-compatible API with `DEEPSEEK_API_KEY`.
- `anthropic`: Anthropic Messages API with `ANTHROPIC_API_KEY`.
- `codex`: local `codex exec` in read-only mode.
- `claude-code`: local `claude --print` with structured output.
- `cdp`: command/protocol bridge provider; set `AICOMMIT_CDP_COMMAND`.
- `command`: custom command adapter for CDP or any local generator.

`auto` chooses the first available option in this order: local Claude Code CLI,
local Codex CLI, OpenAI, Anthropic, DeepSeek. For OpenAI-compatible and
Anthropic providers, leaving `baseURL` empty uses the official endpoint.

Default models can be overridden with flags or environment variables:

- `--model ...` or `AICOMMIT_MODEL` for the selected provider
- `OPENAI_MODEL`, `DEEPSEEK_MODEL`, `ANTHROPIC_MODEL`
- `CODEX_MODEL`, `CLAUDE_MODEL`, `AICOMMIT_CDP_MODEL`

For CDP or any custom bridge, the command receives the full prompt on stdin and
should print either `{"message":"..."}` or a plain one-line message:

```bash
export AICOMMIT_CDP_COMMAND='your-cdp-client generate-commit-message'
./aicommit commit --provider cdp
```

## Protected Files

Protected files are not committed by default. If they are already staged,
`aicommit` removes them from the index before committing.

The project `.gitignore` is always respected. If you manually add patterns such
as `*.png`, `*.pdf`, `*.so`, or `*.dll`, matching files are treated as protected
and removed from the index before the commit.

When `aicommit` detects a protected file that is not already covered by
`.gitignore`, it appends that concrete path to `.gitignore` and includes the
updated `.gitignore` in the commit.

Default protections include:

- `.env`, `.env.*`, `.npmrc`, private key and credential-like files
- `node_modules`, `dist`, `build`, `coverage`, `target`, `vendor`
- common archive, credential, generated, audio, video, and font extensions
- `.so`, `.dll`, `.jpg`, `.png`, and `.pdf` are not filtered by extension alone;
  review those manually, add them to `.gitignore`, or add `protect.exclude` rules
- files larger than `maxFileBytes`
- files whose first bytes look binary

Override with:

```bash
./aicommit commit --include vendor/safe.txt --exclude "*.snapshot"
```

## Config

The config file lives at `~/.aicommit/config.yaml`. If it does not exist,
`aicommit` creates it with defaults on first run.

Useful fields:

```yaml
provider: auto
push: never # never, auto, or always
style: Follow Conventional Commits when possible.
providers:
  openai:
    apiKeyEnv: OPENAI_API_KEY
    model: gpt-5.4-mini
  command:
    type: command
    command:
      - sh
      - -c
      - cat | your-generator
```
