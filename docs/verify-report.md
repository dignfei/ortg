# ORTG v2 验证报告

日期：2026-09-03。环境：Go 1.27.1，Linux，ortg 插件已装进 Claude Code（hook + MCP 五工具），本次验证会话本身就在用 v2 维护本仓库的纲要。

## 基线

- `go vet ./...`、`go test ./...` 通过；`CGO_ENABLED=0 go build -ldflags "-s -w" -o build/ortg .` 重建，`/usr/local/bin/ortg` 软链到 `build/ortg`。
- 每次改代码后重建；hook 每次都是新进程，立即生效；MCP 进程持续用会话开始时的旧二进制（本次验证期间 ortg_update 等工具走旧二进制，数据格式未变，无影响）。

## 闭环（步骤 2）

- SessionStart 注入了规则块与对账报告（27 个"无纲要"，因为 overview.txt 导入的条目全部进未来列）。
- 用 Read 逐个读了全部 Go 源码；Read 后 hook 正确提示"尚无纲要"，改代码未提交纲要后再 Read 正确提示"已过期"。
- Edit 前 hook 注入了未来纲要（未提交前）或现有纲要（提交后）；Edit 后 hook 催提交。
- 27 个文件全部经 ortg_update 提交纲要，实现与未来纲要不一致处以代码为准（如 fsio.go 的 Backup 未接线、index.go 的导出面）。纲要头经 ortg_header 同步了过期判定的新措辞。
- 最终 `ortg scan`：无纲要 0、过期 0、待执行 0，全部对齐。

## 对抗测试（步骤 3，全部实际执行）

| 项目 | 结果 |
| --- | --- |
| 改文件不提交纲要 | 判过期（同秒内也判过期，见 bug 1） |
| 提交纲要后再改文件 | 判过期 |
| 先改文件再提交纲要 | 静默对齐 |
| 删除有纲要的文件 → ortg_future delete=true → 对账 | 墓碑 → 清除，行从表中消失 |
| ortg_future 给不存在文件写未来纲要 | 报告"计划新建"，渲染 `#计划新建`；再 delete=true 后对账即清除 |
| ortg_future document 整份导入 | 目录段换算正确，`#未来:` 与 `#未来:删除` 归属正确；回导不再累积 `#代码纲要`（bug 5） |
| ortg_header 改忽略规则与目录描述 | JSON 段写入正确；含中文的 glob 曾不生效（bug 8） |
| 两个 ortg scan 并发 + 一个 status | 三个都正常返回，无残留 .lock；活 pid 的 .lock 等 3 秒报 lock timeout 退 2；死 pid 的 .lock 被回收 |
| 中文文件名 / 中文目录 | 枚举、提交、渲染、删除清除全部正确 |
| 非 git 目录 ortg scan | WalkDir 枚举，建表正常 |
| 手工损坏 ortg.tsv | scan/status 报 corrupt 退 2，文件原样保留；所有 hook 退 0；session hook 现在给一行停用提示（改进 9） |
| ortg.tsv.bak / .lock / .tmp* 残留 | 不进表（内置 `ortg.tsv.*` 排除 + `.git/info/exclude`） |
| 无 ortg.tsv 的目录跑 hook | 四种事件均输出空、退 0（TestHookBinary 也覆盖） |

## 发现并修复的 bug（每项：改 overview.txt → 改代码 → 补测试 → 重建 → ortg_update 同步）

1. **同秒改动判对齐** — 过期判据是"纲要时间早于 mtime"，秒级精度下提交纲要后同一秒内改代码会被静默对齐并前移指纹，改动永久丢失。原 `TestApplyAndOutlineFlow` 恰好把这个错误行为写成了预期。改为"不晚于"，测试用 `base.Now` 覆盖时钟分别验证真正的"纲要更新"与同秒过期。
2. **hook 与 ortg_update 不认排除规则** — Read/Edit overview.txt、ortg.tsv、build/ 下文件时 hook 照样催纲要；ortg_update 能给 overview.txt 写纲要。新增 `Index.Excluded`，hook 静默、Update 拒绝。
3. **纲要头含空行即损坏整表** — SetHeader 允许空行，落盘后 tsv 三段切分在空行处截断，表再也打不开。SetHeader 现在剔除空行。
4. **绝对路径被拒** — ortg_update / ortg_future 传绝对路径报"磁盘上不存在"。新增 normRel 经 base.Rel 换算。
5. **回导累积 `#代码纲要`** — 渲染文本经 document 参数导回时 body 标记进了纲要头，每轮多一行。导入前剥掉。
6. **删除后恢复的文件永远显示 `#已删除`** — 同内容恢复时指纹未变，墓碑无人清。MarkDeleted 改为双向 SetDeleted，对账按状态置清。
7. **overview.txt 不合法时留下半成品空表** — 建表后导入失败，表留着，下次不再导入。失败即 Discard。
8. **忽略 glob 含中文不生效** — globRegexp 按字节拼正则，UTF-8 多字节被拆碎。改为按 rune。
9. **附带改进**：fsio.Backup 从未被调用（设计写了"写前备份"），改为 tsv.save 内容未变不写、变了先写 .bak 再原子写；报告的"已删除待确认"不再列本轮已清除的行；session hook 在表损坏时给一行提示而非静默停用。

10. **PreToolUse 每次 Edit 都整段注入**（用户反馈）— 现按 session_id + 路径去重，标记是临时目录 `ortg-hook-<session_id>/` 下的空文件，不进表也不进会话；SessionStart 来源为 startup/clear/compact 时清空标记（resume 保留上下文，不清），所以压缩后首次改动重新注入；无 session_id 时每次注入。用真实载荷验证：第二次 pre 输出空，compact 后再次注入。

提交：`a0d2915`（bug 1-7、9）、`6e29431`（bug 8、损坏提示）、见 git log（第 10 项）。

## tmux 驱动真实 Claude 会话的端到端测试

在临时 git 仓库（a.go、b.go、README.md，`ortg scan` 建表）里用 tmux 启动 `claude --permission-mode acceptEdits`，发送提示词驱动，事后解析会话 jsonl 里的 hook 记录与 /tmp 标记目录核对。

| 步骤 | 观察 |
| --- | --- |
| 启动 | SessionStart:startup 注入规则块与对账报告 |
| 调用 ortg_overview | MCP 工具正常返回整份纲要 |
| 对 a.go 连续两次独立 Edit | 第一次 PreToolUse 注入骨架规则，第二次 PreToolUse 无注入；PostToolUse 两次都催提交；`/tmp/ortg-hook-<session_id>/` 出现一个标记 |
| /compact | SessionStart:compact 触发，标记目录被删；随后 Edit a.go 重新注入，标记重建 |
| Read b.go 后按提示 ortg_update b.go 与 a.go | PostToolUse:Read 提示"尚无纲要"；两次 ortg_update 成功；`ortg status` 显示 a.go、b.go 对齐 |
| /exit 后 `claude --resume` | SessionStart:resume 触发，标记目录保留；再 Edit a.go 时 PreToolUse 无注入 |
| resume 后 Edit a.go 不提交再 Read a.go | PostToolUse:Read 提示"纲要已过期"；`ortg status` 判 a.go 过期 |

## 第二轮：Bash 路径的盲区与 pre(Bash) 的 A/B 否决（2026-09-03 晚）

背景：全局约定要求在 bypass 权限模式下用 cat/sed/grep 读写文件，`Read`/`Edit` 工具几乎不用。第一轮验证全部走的是 Read/Edit 匹配器，这条主路径没被测过。

### bug 11：Bash 读骨架文件全程沉默

`post(Bash)` 只走 `shellStaleNote` → `TouchedPaths()`，而后者第一行就是 `r.Outline == "" → continue`，骨架行天生看不见。`cat newmod.go` 输出空，同一文件走 `Read` 工具却正常提醒。

修法：新增 `base.MentionedPaths(cmd)`，不碰磁盘、只从命令文本认出表内的活行——全路径可带前导斜杠，裸文件名须在表中唯一且前面不是斜杠，两端都要落在路径 token 边界上（`myindex.go`、`index.golden` 不误命中）。`shellNote` 取代 `shellStaleNote`：命令点名的文件既报骨架也报过期，全表 stat 保留为只报过期的兜底（`git checkout` 一类命令不写路径）。骨架与过期各记一把会话标记。

验证（tmux 驱动真实会话，`/tmp/ortg-sandbox`）：

| 步骤 | 观察 |
| --- | --- |
| hook 层 A/B | 修复前 `cat` 骨架文件输出空；修复后给出骨架提醒；不提文件名的命令仍静默；同会话不重复 |
| 端到端 | 先问一个纲要能答的问题让模型的对账快照固定，**之后**才新建 `retention.go` 并 scan——该文件不在模型上下文的任何清单里，hook 是唯一线索 |
| 结果 | 模型用 Bash 读它后自述"读完顺手补了一行并提交了"；磁盘核对无纲要 4→3、对齐 32→33，纲要行质量合格 |

提交：`720762d`。

### 试过并否决：给 Bash 挂 PreToolUse

做法是让 `pre(Bash)` 用 `MentionedPaths` 把命令点到名的文件的纲要注入进去（sed/heredoc 写文件到不了 Edit 匹配器）。实现了 43 行生产代码 + 单测 + 三处清单注册，然后做 A/B 真会话对照再决定去留。

任务故意挑的：下调 S 配额，需要同时改 `logic.go` 的 quota 表与 `prompt.go` 的两处文案，而这条联动**只写在纲要里**（`prompt.go` 的 S："改一处就得改另一处"）。两个沙箱唯一差异是 `settings.json` 里有没有 `PreToolUse:Bash`。

| | A（有 pre Bash） | B（无） |
| --- | --- | --- |
| 四处联动改动 | 全对 | 全对 |
| 测试 / 纲要对齐 | 通过 / 31 对齐 | 通过 / 31 对齐 |
| pre 注入 | 1 次 2383 字 | 0 |
| 输入 token | 2.39M | 2.16M（A 多 10.6%） |

时序上注入确实赶在改动之前到达（`04:00:38` 只读命令触发注入 → `04:00:48` 才发出改文件的 sed），B 组没有这 2383 字一样全改对——因为开工 `ortg_overview` 时就读到了。另取本仓库 588 条真实 Bash 命令统计：命中率 3%（每文件每会话只注入一次），但一个会话累计注入 11470 字 = **重复灌回整份纲要的 76%**。

否决的机制理由（比单次结果更硬）：模型开工必读整份纲要，`pre(Bash)` 注入的是它已有的信息；`pre(Edit)` 有价值是因为 Edit 是精确的单文件写，而 Bash 命令 97% 只读、pre 阶段无法区分读写，注入是盲打；真正的缺口 `post(Bash)` 已经补上，且它在确认文件被碰过之后才说话。代码已退回，不要再加一遍。

补记（2026-09-26）：否决的前提"模型开工必读整份纲要"后来实测不成立——上下文压缩后、或用户一上来就要 grep 时，模型会跳过 `ortg_overview` 直接读代码。于是加了 gate（见 rules.md 的 hook 表）：它不注入纲要内容，只在本上下文还没加载纲要时拦一次、之后每文件提醒一次，已加载时静默；跑在本会话的 ortg MCP 进程里（hook 类型 `mcp_tool`），不起进程。与这里否决的"注入式 pre(Bash)"不是一回事。

### S 配额上调 900 → 1200（C9-8）

上一条修复把 `shellNote` 的语义写进纲要时，`logic.go` 的 S 从 909 被压到 893 才塞得下，删掉的都是还有用的信息（`Overview带module只放行同Module`、`导出钩子导入后才挂`、`ortg_status走Scan`）。`index.go` 同样顶在 898/900。

`logic.go` 已经是 C9、`index.go` 是 C8，而 C9 与 C8 配额相同，提标签换不来空间——上限本身才是约束。C9-8 改为 1200，四处同步：`logic.go` 的 quota 表、`prompt.go` 的 `updateDiscipline` 与 `headerTemplate`、以及已建表仓库的表头（`ortg_header`，模板只管新建表）。被压掉的内容已还回。

同一份统计还暴露 `prompt.go` 定档过低：C5（常规/资产）却 569 字超标，而它是全部模型可见文案的唯一出处、与 `logic.go` 的 quota 表三处联动，改一个字就改系统行为。提为 C8。

调整后余量：`logic.go` 1175/1200、`index.go` 1021/1200、`prompt.go` 625/1200。`hookio.go` 497/500 与 `fs.go` 476/500 顶在 C7 档，暂未动。

## 还剩的问题（未改）

- 忽略又不在磁盘的行会一直留在表里（ignored=1 且缺失），不渲染但不清理。
- 报告里的"忽略 N"只计表内行，不计从未入表的被忽略文件。
- ortg_future document 的纲要头若只给部分行（缺 #A/#B/#E 字典）会整份导入失败，这是校验本意，但错误信息只说缺字典。
- S 字段配额只是警告，不阻断提交。C9-8 上调到 1200 后 index.go 与 logic.go 有余量；hookio.go（497/500）与 fs.go（476/500）顶在 C7 档，下次它们再长就得连 C7-4 一起重估。
- 注入标记目录在会话结束后不会自动删除，留在系统临时目录里直到重启；要严格可加 SessionEnd hook 删目录。
- 二进制重建后 MCP 进程不会自动换，需 /mcp 重连或新会话；hook 不受影响。
- session hook 对账超过 4 秒退出时，后台 goroutine 会被进程退出打断，可能留下 .tmp 或死 pid 的 .lock，下次由排除规则与回收逻辑兜底，未专门测试超时路径。
