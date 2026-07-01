# aicommit

`aicommit` 是一个用 Go 编写的小工具，专注于解决 AI 代理修改代码后的"最后一公里"问题：
暂存安全文件、基于暂存的 diff 生成提交信息、执行提交，并在你准备就绪时单独推送。

## 解决的问题

AI 工具（如 Claude Code、Cursor、Copilot 等）修改代码时，经常会生成或修改一些不应该提交到 git 的文件：
- 构建产物（`node_modules/`、`dist/`、`build/`、`.next/` 等）
- 环境配置和密钥文件（`.env`、`.npmrc`、私钥等）
- 二进制文件和资源（`.so`、`.dll`、`.png`、`.pdf` 等）
- 临时文件和缓存

如果开发者不注意，这些文件就会被错误地提交到仓库中。

## 与其他 aicommit 工具的不同

| 特性 | aicommit | 其他工具 |
|------|----------|----------|
| **智能文件保护** | ✅ 自动检测并排除敏感/构建产物文件，减少误提交 | ❌ 通常不管，全部提交 |
| **自动维护 .gitignore** | ✅ 检测到未覆盖的受保护文件会自动添加到 .gitignore | ❌ 无此功能 |
| **二进制文件检测** | ✅ 通过文件头字节检测二进制文件并排除 | ❌ 无此功能 |
| **可覆盖规则** | ✅ 支持通过 `--include` / `--exclude` 灵活覆盖 | ❌ 规则较固定 |

## 安装

### macOS / Linux（一键安装）

```bash
curl -fsSL https://raw.githubusercontent.com/CoolBanHub/aicommit/main/install.sh | sh
```

脚本会自动识别操作系统和架构，下载对应的二进制文件（如 `aicommit-darwin-arm64`），
重命名为 `aicommit` 并安装为全局命令。

执行脚本后，二进制程序会安装到：

- `/usr/local/bin/aicommit`：当 `/usr/local/bin` 存在且当前用户有写权限时。
- `~/.local/bin/aicommit`：当 `/usr/local/bin` 不可写时。
- `<自定义目录>/aicommit`：使用 `--dir` 指定安装目录时。

也可以手动指定版本或安装目录：

```bash
curl -fsSL https://raw.githubusercontent.com/CoolBanHub/aicommit/main/install.sh | sh -s -- --version v0.0.1 --dir ~/.local/bin
```

### Windows（PowerShell）

```powershell
iwr -useb https://raw.githubusercontent.com/CoolBanHub/aicommit/main/install.ps1 | iex
```

执行脚本后，二进制程序会安装到 `%USERPROFILE%\.aicommit\bin\aicommit.exe`。
脚本会将 `%USERPROFILE%\.aicommit\bin` 加入用户 PATH；打开新终端后即可使用 `aicommit`。

### 手动下载

前往 [Releases 页面](https://github.com/CoolBanHub/aicommit/releases/latest)，根据平台下载对应文件
（如 `aicommit-darwin-arm64`），重命名为 `aicommit`（Windows 为 `aicommit.exe`），赋予执行权限并放入 `PATH`：

```bash
chmod +x aicommit-darwin-arm64
sudo mv aicommit-darwin-arm64 /usr/local/bin/aicommit
aicommit version
```

## 构建

```bash
go build -o aicommit ./cmd/aicommit
```

## CLI 用法

```bash
./aicommit commit
./aicommit commit --provider openai --model gpt-5.4-mini
./aicommit commit --provider deepseek
./aicommit commit --provider anthropic
./aicommit commit --provider codex
./aicommit commit --provider claude-code --dry-run
./aicommit commit --provider cdp
./aicommit push
./aicommit tag
./aicommit tag --push
./aicommit tag v1.2.3
./aicommit push-tag v1.2.3
```

默认情况下，`commit` 命令会执行以下操作：

1. 检测目标 git 仓库，如果没有则运行 `git init`
2. 当 `.gitignore` 不存在时创建它，并添加默认的保护模式
3. 如果已保护的文件已被暂存，将其从索引中移除
4. 暂存允许的变更文件
5. 将缓存的 diff 发送给选定的 AI 提供商
6. 运行 `git commit -m <生成的提交信息>`

默认情况下不会推送。使用 `aicommit push` 来推送当前分支的所有本地提交。
如需自动推送，可在配置中设置 `push: auto` 或 `push: always`。

## 标签

```bash
./aicommit tag
./aicommit tag --push
./aicommit tag v1.2.3
./aicommit push-tag v1.2.3
./aicommit push tag v1.2.3
```

`aicommit tag` 会基于当前仓库已有的最新数字版本标签自动递增最后一段数字。
例如已有 `v0.0.1` 时会创建 `v0.0.2`；已有 `v1.2.3.4` 时会创建 `v1.2.3.5`。
也可以直接指定要创建的版本标签。使用 `--push` 会在创建后推送刚创建的标签。

`aicommit push-tag` 会推送指定标签；不指定标签时，会推送当前仓库已有的最新数字版本标签。
`aicommit push tag` 是同等功能的别名。

## 服务模式

```bash
./aicommit serve --addr 127.0.0.1:8686
curl -X POST http://127.0.0.1:8686/commit \
  -H 'content-type: application/json' \
  -d '{"repo":"/path/to/repo","provider":"codex","dryRun":true}'
curl -X POST http://127.0.0.1:8686/push \
  -H 'content-type: application/json' \
  -d '{"repo":"/path/to/repo"}'
```

## AI 提供商

支持的提供商名称：

- `openai`：兼容 OpenAI 的聊天补全 API，使用 `OPENAI_API_KEY`
- `deepseek`：DeepSeek 的 OpenAI 兼容 API，使用 `DEEPSEEK_API_KEY`
- `anthropic`：Anthropic Messages API，使用 `ANTHROPIC_API_KEY`
- `codex`：本地只读模式运行 `codex exec`
- `claude-code`：本地运行 `claude --print` 并获取结构化输出
- `cdp`：命令/协议桥接提供商；设置 `AICOMMIT_CDP_COMMAND`
- `command`：CDP 或任何本地生成器的自定义命令适配器

`auto` 会按以下顺序选择第一个可用的选项：本地 Claude Code CLI、本地 Codex CLI、OpenAI、Anthropic、DeepSeek。
对于兼容 OpenAI 和 Anthropic 的提供商，留空 `baseURL` 将使用官方端点。

可通过标志或环境变量覆盖默认模型：

- `--model ...` 或 `AICOMMIT_MODEL` 用于选定的提供商
- `OPENAI_MODEL`、`DEEPSEEK_MODEL`、`ANTHROPIC_MODEL`
- `CODEX_MODEL`、`CLAUDE_MODEL`、`AICOMMIT_CDP_MODEL`

对于 CDP 或任何自定义桥接，命令会通过 stdin 接收完整的提示，并应输出
`{"message":"..."}` 或单行纯文本消息：

```bash
export AICOMMIT_CDP_COMMAND='your-cdp-client generate-commit-message'
./aicommit commit --provider cdp
```

## 文件保护

受保护的文件默认不会被提交。如果它们已被暂存，`aicommit` 会在提交前将其从索引中移除。

项目的 `.gitignore` 始终受到尊重。如果你手动添加了 `*.png`、`*.pdf`、`*.so` 或 `*.dll` 等模式，
匹配的文件将被视为受保护文件，在提交前从索引中移除。

当 `aicommit` 检测到未被 `.gitignore` 覆盖的受保护文件时，它会将该具体路径追加到 `.gitignore`，
并将更新后的 `.gitignore` 包含在提交中。

默认保护包括：

- `.env`、`.env.*`、`.npmrc`、私钥和凭证类文件
- `node_modules`、`dist`、`build`、`coverage`、`target`、`vendor`
- 常见的压缩包、凭证、生成文件、音频、视频和字体扩展名
- `.so`、`.dll`、`.jpg`、`.png` 和 `.pdf` 不会仅按扩展名过滤；
  请手动审查，将它们添加到 `.gitignore`，或添加 `protect.exclude` 规则
- 超过 `maxFileBytes` 大小的文件
- 起始字节看起来是二进制内容的文件

可通过以下方式覆盖：

```bash
./aicommit commit --include vendor/safe.txt --exclude "*.snapshot"
```

## 配置

配置文件位于 `~/.aicommit/config.yaml`。如果不存在，`aicommit` 会在首次运行时创建默认配置。

常用字段：

```yaml
provider: auto
push: never # 可选值：never、auto 或 always
style: 尽可能遵循约定式提交（Conventional Commits）。
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
