# ORTG 开发指南

给参与开发 ORTG 的 AI 编码代理与贡献者看。Claude Code 经 `CLAUDE.md` 引入本文件，Codex 直接读本文件。用户文档见 [README.md](README.md)，完整规则见 [docs/rules.md](docs/rules.md)；它们与代码不一致时以代码为准。

## 构建与测试

- Go 1.27（`go.mod` 声明），只用标准库，零第三方依赖。
- 构建：`CGO_ENABLED=0 go build -o build/ortg .`
- 测试：`go test ./...`；`go test -short ./...` 跳过真编译二进制的集成测试。
- 提交前：`gofmt -l .` 无输出、`go vet ./...` 通过、`go test ./...` 全过。
- 装成插件调试见 README「安装为插件」。改了 `hooks/*.json` 或插件清单后，要卸载插件、删缓存再重装，宿主才会读到新版本。

## 代码布局

改之前先想清楚改动属于哪一层：

- `base/`：底层机制，不含任何产品决策。顶层只经 `base/index.go` 的语义操作与 `base/hookio.go` 的 hook 收发使用它；`tsv`/`fs`/`entry`/`render` 只被 `index.go` 调用。
- `logic.go`：全部产品策略——状态判定、MCP 工具语义、hook 应答、读写门禁。改行为改这里。
- `prompt.go`：模型能看到的全部文案只放这里。改一个字就改了系统行为，有测试锁定的文案改了要同改测试。
- `main.go`：只把子命令与 MCP 工具绑定到 `logic.go`，不放逻辑。
- `migrate/`：把旧版表与纲要文档转成当前格式；核心包不 import 它。
- Claude Code 与 Codex 的宿主差异全部收敛在 `base/hookio.go`。

## 必须同步改的地方

- 版本号四处一致：`.claude-plugin/plugin.json`、`.claude-plugin/marketplace.json`、`.codex-plugin/plugin.json`、`logic.go` 的 `version`（`manifest_test.go` 校验）。
- `hooks/hooks.json` 与 `hooks/codex.json` 的命令式条目必须一模一样；新增 hook 事件要同改两份文件与 `manifest_test.go`。
- 行为变了，同步改 `README.md` 与 `docs/rules.md`。

## 用 ORTG 开发 ORTG

仓库自带本项目的纲要 `ortg.tsv`。装了 ortg 插件后，会话开始会注入规则，按规则走：

1. 开工先调用 `ortg_overview` 取纲要；纲要能回答的不去读源码。
2. 只改一个文件直接改；要改多个文件，先用 `ortg_target` 为每个文件写目标纲要，`ortg_review` 核对到连续两次计划不变，再动手。
3. 改完一个文件立即用 `ortg_update` 提交它的新纲要，`ortg.tsv` 随代码一起提交。

没装插件也可以贡献代码，在 PR 里说明纲要没更新即可。

## 提交

- 一次提交只做一件事，用 conventional commits（`feat:` `fix:` `docs:` `refactor:` `test:` `chore:`），写明改了什么、为什么。
- 不提交 `build/`、`ortg.tsv.bak` 等生成物，也不提交 `CLAUDE.local.md`（本机专属设置，已在 `.gitignore`）。

## 许可证

贡献的代码按 Apache License 2.0 发布。不要删改 `LICENSE` 与 `NOTICE`。
