# ORTG v2

单文件纲要引擎。每个源码文件一行模型写的纲要（`文件名[标签]: Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束`，标签如 `[8 L ST T J M]`：重要度数字在前，其后是以空格分隔的层级、模块、特征、规模，取自纲要头字典），全部状态存在仓库根的 `ortg.tsv`，模型通过 MCP 读整份纲要并回写，宿主 hook 在读代码、改代码时注入纲要与提醒。

## 获取与构建

```bash
git clone https://github.com/dignfei/ortg.git && cd ortg
```

在仓库根目录执行（需要 Go 1.27，`go.mod` 声明；本机 Go 较旧时会自动下载 1.27 工具链，网络不通就先装好 1.27）：

```bash
CGO_ENABLED=0 go build -ldflags "-s -w" -o build/ortg .
go test ./...
ln -sf "$PWD/build/ortg" /usr/local/bin/ortg   # 放进 PATH；之后重新编译不用再链
```

## 安装为插件

Claude Code（会话里 `/plugin …` 与命令行 `claude plugin …` 等价）：

```bash
claude plugin marketplace add "$PWD"   # 在 ortg 仓库根目录执行
claude plugin install ortg@ortg
```

装进去的是仓库工作树在那一刻的拷贝（`~/.claude/plugins/cache/ortg/ortg/<版本>/`），不会自动跟着仓库走。`hooks/hooks.json` 或 `.mcp.json` 改了之后要重新拷一次再重启会话；版本号不变时 `claude plugin update` 只会说"已是最新"而什么都不拷，所以直接卸了重装。重装前先删掉插件缓存：旧版本的缓存目录还在时，实测重装后缓存里仍是旧版的 `hooks.json`（从 0.1.0 直接装 0.3.39，拿到的是 0.1.0 的 hooks），删掉缓存就整份从仓库重拷：

```bash
claude plugin uninstall ortg@ortg && rm -rf ~/.claude/plugins/cache/ortg && claude plugin install ortg@ortg
```

装完核对缓存里的 hooks 与仓库一致（在仓库根执行，输出 `True`）：

```bash
python3 -c "import json,glob;print(all(json.load(open(f))==json.load(open('hooks/hooks.json')) for f in glob.glob('$HOME/.claude/plugins/cache/ortg/ortg/*/hooks/hooks.json')))"
```

（或者把四处版本号一起提一级——`.claude-plugin/plugin.json`、`.claude-plugin/marketplace.json`、`.codex-plugin/plugin.json` 与 `logic.go` 的 `version`，`manifest_test` 盯着它们一致——再 `claude plugin marketplace update ortg && claude plugin update ortg@ortg`。）

Codex（codex-cli 0.154 实测）：同样把本仓库当插件市场装上，在 ortg 仓库根目录执行

```bash
codex plugin marketplace add "$PWD"
codex plugin add ortg@ortg
```

之后第一次启动 `codex` 会提示 "Hooks need review"，选"Trust all and continue"，否则 hook 不会运行。信任绑定在 hooks 文件的内容上（Codex 用的是 `hooks/codex.json`：命令式 hook 与 Claude 相同，另有一条按 Codex 的 MCP 名 `ortg` 写的纲要闸门）：以后插件更新改了它（比如新增一条 hook），第一次启动还会再问一次，照样选信任。Codex 的改文件工具是 `apply_patch`，一次可改多个文件：hook 会从补丁头里取出每个文件，逐个注入改动前的纲要、改动后催提交；补丁删掉的文件会被要求用 `ortg_target` 标记删除。MCP 仍要在 `~/.codex/config.toml` 加上：

```toml
[mcp_servers.ortg]
command = "ortg"
args = ["mcp"]
env = { ORTG_AGENT = "codex" }
```

并在同一文件开头（任何 `[表]` 之前）加两行：

```toml
model_context_window = 1000000
tool_output_token_limit = 500000
```

Codex 把工具结果存进对话时另有上限，默认约 1.2 万 token（模型的 truncation_policy × 1.2），超出就从中间截掉、只留首尾，`@exec` 的 `max_output_tokens` 抬不过它；不加 `tool_output_token_limit`，稍大的纲要就被截成半份；`model_context_window` 把上下文调到模型允许的最大值（Codex 会截到模型的 max_context_window，gpt-6-astra 是 87.2 万 token），装得下整份纲要。`env` 让 MCP 进程知道自己跑在 Codex 下（Codex 不给 MCP 进程设 `PLUGIN_*` 变量），纲要大小才按 Codex 的 token 计。`tool_output_token_limit` 对所有工具生效，但 `exec_command` 等工具各有自己的单次默认上限，普通输出不会因此变大。

插件清单声明了 hook 和 MCP，不改用户其他配置。不要再把 hook 抄进任何 `settings.json`，也不要用 `claude mcp add` 单独注册 ortg：宿主不去重，多一份注册就是每条钩子跑两遍、两套同名工具。仓库里没有 `ortg.tsv` 时插件对该目录零动作。

## 初始化一个仓库

对模型说"初始化 ortg"，它会调用 `ortg_overview`：根目录有 `overview.txt`（人手写的纲要文档）就整份导入为目标纲要，并把它改名为 `overview.txt.bak`，否则按模板建空表；然后首扫。纲要只存在 `ortg.tsv` 里，不导出文本文件：模型只经 `ortg_overview` 取纲要，人要看整份可在会话里让模型调用它。命令行等价：`ortg scan`。

按模板新建的表，纲要头的【部署】【系统】还是占位文字，这时按「文档 → 纲要头 → 源码」的顺序建纲要，ORTG 逐步拦着：先只收文档文件（`*.md`、`*.rst`、`*.adoc`、`*.txt` 或 `docs/`、`doc/`、`guide/` 下的文件）的纲要，`ortg_overview` 开头列出还没纲要的文档，要求逐份读全文写纲要（Constraints 写文档规定的约定、约束与设计决定，按 1200 字配额）；文档纲要没齐之前 `ortg_header` 不让把纲要头写成正式内容（用不上的文档可同时放进观察档）；纲要头写好后才收源码纲要与 `ortg_target` 计划。文档只作背景，纲要按代码实际怎么做来写，两者不一致时按代码写并在那份文档的纲要里记下。没有文档的仓库直接读入口文件与目录结构写纲要头。跨模块、不靠调用关系的约定（同一概念由多层共同实现、协议、单位、兼容性、HTTP/WebSocket 路由与消息、配置键）在纲要头登记 `#【契约】键：正文`（正文只写这一处），参与的文件在纲要行末尾加 `| Keys:键(角色)`；改动涉及某个键时，`ortg_review` 列出契约正文与全部参与者，路由、消息、配置类的键还会在源码里查出用了字面量却没声明的文件。需求与守护测试冲突时按证据裁决（需求点名该测试或说旧行为错→改测试；需求说测试权威、行为不变或只是迁移→保持；有验收例→按实跑；都没有→先求两全）：`ortg_review` 给出正文的契约，每条要在下一次核对的 `rulings` 里写一行「契约键：裁决」，缺一条计划就不收敛，收敛时裁决记进该契约正文末尾的【裁决】段；职责从一个文件迁到另一个文件时，新承担者要单独通过原守护测试。测试也调用的入口在 API 里写"测试也调用"，不写"单测入口"，改动时不得加 `cfg(test)` 或收窄可见性（改后提示会检查）；同一个参数或限额能由多层执行时，写明唯一执行点。给文件新增对外接口（Rust `pub` 项、Go 导出名、JS/TS `export`）必须写进 API：提交时与 git HEAD 版本比，新增却没写的会被拒，所以会新增接口的文件在计划里不要写 `=`。Uses 一律写仓库相对路径（整个目录或包写目录路径并以 `/` 结尾，如 `lib/storage/`；第三方库不写），提交时逐项校验，写成包名、模块名或裸文件名会被拒——依赖方按路径反查，写不成路径的依赖会被漏掉。写源码纲要时一并读它的单元测试与相关集成测试，按**测试契约**把被测试锁定的对外行为写进 Constraints：写成"必须保持 X（测试名）"并写清期望结果，而不是"现状是 X"；跨模块的汇总进纲要头。所有文件都有纲要后，ORTG 在最后一次 `ortg_update` 的回复里提示收尾：逐条核对文档纲要里的每条约定、以及观察档里测试锁定的行为，已写进纲要头或某个源码纲要（没有的先补上），再把文档放进观察档（`observe_files`/`observe_dirs`），避免同一内容在纲要里写两遍；之后文档改了，对账会报「观察变动」。改代码时，`ortg_review` 的核对清单与改动后的提示都会要求运行纲要里点名的守护测试；测试失败时先回到需求确认，需求没有明确要求改变那个行为，就改实现、不改测试期望值。

写源码纲要须阅读源码正文（长文件分段读完）及可见的相关测试。AST、导入和符号清单可辅助定位，不能代替语义概括；Uses 按职责与契约的重要性选择强依赖，不能按导入顺序机械截取前五项。API/Constraints 无内容不伪填，不把未提供的固定文案猜写成已知约束。条目齐全和状态对齐不等于语义质量已核验；遇到需求、源码、测试或实际运行结果冲突时，核查对应源码与测试并修正纲要。

从旧纲要文档迁移：导入时（`migrate` 包）自动把索引换成纲要、英文字典键换成中文键、`#未来:` 换成 `#目标:`，所以只是措辞老旧、格式仍合协议的文档可直接导入。格式也不合的（如旧的数据库表行、多套标签体系），导入会失败但保留空表并给出迁移步骤，由模型把旧文档（可能是多个文件）合写成一份合协议的文档，再用 `ortg_target` 的 `document` 参数整份提交。从 ORTG v1 分卷索引迁移则全自动：`overview.txt` 是 v1 根清单、分卷（overview.meta.txt 代码字典、overview.code.txt 条目）还在时，`ortg scan` 直接升级建表，存在的文件进纲要、缺失的进目标纲要；模块卷不迁移。

## 用法

每次开一个新会话（Claude Code 或 Codex），按这个顺序说：

1. 第一句问：

   ```
   纲要对齐了吗？
   ```

   模型会实时重新对账（调用 `ortg_status` 或 `ortg_overview`，两者都会重扫），报告对齐、过期、无纲要各多少。有未对齐的文件时，程序会同时给出三个处置选项，回数字即可（1 立刻逐个补纲要 / 2 人工已核对只前移指纹 / 3 先不处理）。

2. 第二句说：

   ```
   先用ortg全局纲要理解系统；需要时读取对应源码与测试，核实阻断目标的依赖缺口。
   ```

3. 然后写你的实际需求，比如新增功能、修复 bug 等。

模型据整份纲要定位要动的文件，只读纲要没写到的具体细节；改完一个文件会顺手提交它的新纲要，让纲要跟着代码保持对齐；改动后还会列出可能受影响的候选文件（全局纲要里 Uses 写到它的、CBM 调用图里调用它的——改动后先同步更新调用图再查，并保留更新前的调用方，改名时要跟着改的文件不会漏），并让模型通读全局纲要找出语义相关的（同一概念、配置、数据格式或约定），逐个判断哪些要跟着改代码、哪些只需重写纲要。会话一开始，hook 只提示模型先调 `ortg_overview`：整份纲要同时过两道线才一次全给：估算的 token 不超过上下文窗口的一半，大小不超过宿主单次截断值减一点余量（Claude 约 48 万字、Codex 约 48 万 token）。token 按实测拟合的公式估：Claude 约 1.08×中文字符 + 0.53×其他字符，Codex 约 0.91×中文字符 + 0.25×其他字符（固定的「字/token」比例在中文多和代码多的纲要上会差三成）。窗口：Claude 取 `CLAUDE_CODE_AUTO_COMPACT_WINDOW`，没设时看模型是否带 `[1m]`（`ANTHROPIC_MODEL`，或会话开始的 hook 记下的模型；只有交互会话的 hook 带模型，`claude -p` 不带）——带就是 100 万、否则 20 万；Codex 读 `~/.codex/config.toml` 的 `model_context_window`（截到模型上限）或 `models_cache.json` 里模型的窗口，再乘 Codex 实际可用的比例，读不到就只看截断值。纲要一直留在上下文里、压缩后还会重发，超过窗口一半就没多少空间干活了；超过时先给纲要头和模块清单，模型再按问题取相关模块。大小按宿主自己的算法计：Claude Code 按 JS 字符串长度（UTF-16 码元，汉字算 1），Codex 按 UTF-8 字节 ÷ 4 估 token。纲要只发一次：同一会话里已发给模型的条目不再重发，再取只补还没有或已变动的；上下文被压缩或清空（`/compact`、`/clear` 等）后自动重发；以 `--resume` 续接同一对话（宿主换了新进程）时沿用已发记录，不重发。单次超过上限（Claude 50 万字、Codex 50 万 token，各减一点余量）照发（可能被截断），开头提醒模型按拆分原则（按业务域与职责、沿 Uses 依赖稀疏处切、公共底层单独成模块、每模块宜在单次上限的十分之一以内）拟一份拆分方案给用户，用户同意后改 B 标签并登记 #B 字典。

任务执行发现仓库缺失符号、导入失败或依赖缺口时，按实际调用路径与可见测试判断是否阻断目标功能或验收：阻断的纳入修复计划，不能仅因文件未点名或改动前已缺失而排除。为绕过仓库缺失实现而临时注入或替换的函数、对象只算诊断；提交或结束任务前须移除这些替代，在新进程里用最终持久源码运行验收。同一契约由 setter、预检、包装或快捷分支处理时，按入口与触发条件分别验证已规定的异常类型、词句和输出格式，不只测底层函数。

会话里还没加载纲要就去读、搜、改代码时（比如上下文压缩之后，或一上来就 grep），第一次会被拦下并提示先调 `ortg_overview`；只拦这一次，之后每个文件附一句提醒；已加载就不打扰，大仓库只在碰到还没取的模块时提醒去取那个模块。这个检查跑在本会话的 ortg MCP 进程里，不另起进程（Claude Code 与 Codex 都有）。

只改一个文件时直接改，改完提交它的纲要（第一次写时 ORTG 附一句提醒）。要改多个文件（含新建、删除）时必须先定计划并自查：用 `ortg_target` 的 `items` 一次写全这次要改、新建、删除的每个文件的目标纲要（每项 `路径 目标纲要行`，纲要不变写 `=`，计划删除写 `删除`），调用 `ortg_review` 按全局纲要逐项核对（接口与调用方、数据与约定、测试与文档……；它还会按目标纲要里新出现的标识符，列出计划外纲要里同名概念的「必须保持」条目，要求逐条说明新实现是否仍满足），补改后再核对，连续两次核对计划不变、且给了正文的契约都写了裁决（`rulings`）才解锁这些文件。核对给正文的契约：计划里非枢纽文件（声明的契约键不超过 8 个）声明的全部键，加上正文命中计划新引入标识符的键，至多 15 条；只被枢纽文件（如解析全部参数的文件）声明的键只列名。判定方法：计划外已写、还没提交纲要的文件只能有一个，再写第二个（包括 `sed -i`、`>` 重定向、`rm` 等 shell 写入）就拦下，要求先列计划；前一个文件提交纲要（`ortg_update`）后，下一个文件又算单文件改动。观察档文件（如测试目录）不写纲要，不参与判定。这一步是模型自查，不给用户看、不等同意，核对完直接动手。只改实现、改完纲要不变的文件（重构、格式化、修 lint），目标纲要传 `=` 即可，不必把整行纲要再抄一遍，纲要里只显示一行 `#目标:同现在`。改测试文件时 ORTG 会提醒一次：只在需求明确要求时改测试期望值。同一计划的后续核对只回差量：目标没变的文件只列名，已给过正文的契约只列键，冲突三问与核对清单每个计划只发一次。提交（`git commit`）前要有一次不收窄的整包测试：自最近一次改代码以来没跑过按仓库自己配置、不加 `-k`、不只跑单个文件或子目录、不覆盖 `addopts` 等收集配置的整包测试，ORTG 会把这次提交拦一次（同一提交点再提交即放行）。被拦的是整条命令，同一条命令里的 `git add` 等暂存也没执行，拦截提示会说明这一点；同一提交点再提交时命令既不暂存、其间也没单独暂存过，就再拦一次，提醒先把要提交的改动暂存上——否则只有此前已暂存的内容进入提交。测试配置（conftest.py，限 testpaths 之内）里按依赖版本整类跳过测试的分支，ORTG 会自动检出，随纲要一起列出（在条目之后、对账报告之前）；每道门控提交前至少要在临时去掉它的情况下跑过一次整包，条件在本环境不成立的，被拦后再提交一次即记为已确认。测试运行选中的用例全部被跳过时，改后提示会立即提醒"跳过不等于通过"；去掉门控后跑出失败时，提醒改动前就失败的也要处理（CI 上同样失败），不能只看新增失败。改了依赖清单里已有依赖的版本（与 git HEAD 比，新增依赖不算）时，改后提示会提醒：需求没点名的升级先改回去，在现有版本里实现。整包测试放到后台或脱离会话启动、还没跑完且改动未提交时，结束回合会被 Stop 钩子拦一次，提醒在本回合里等它跑完（非交互运行时回合一结束会话就退出）。Stop 钩子需要 0.5.29 及以上的 `ortg` 二进制：升级时先重新构建并安装二进制，再重装插件——旧二进制对 Stop 不输出 JSON，Codex 会在每个回合结束时报错。删掉的顶层名字若还被测试导入或调用，改后提示与 `ortg_update` 回复会列出名字与测试文件：需求没点名删除的默认保留（不再调用的辅助函数或弃用别名都行）。测试配置按依赖版本或平台整类跳过、本地跑不到的测试，在被测文件纲要里写成「本地验证盲区」。纲要的 Constraints 字数配额按重要度：C9 2000 字、C8 1200 字、C7–4 500 字、C3–1 100 字。

## 三档角色：认知 / 观察 / 排除

每个文件落在三档之一，判定顺序是**内置结构性排除 → keep_files → observe_* → ignore_***：

| 档 | 配置 | 行为 |
| --- | --- | --- |
| 认知 | 默认，或 `keep_files` 捞回 | 要写纲要；没写就一直在"无纲要"里催 |
| **观察** | `observe_dirs` / `observe_files` | 记指纹、报变动，**不要求纲要**、不进渲染出的整份纲要。内容变了报"观察变动"，`ortg_align` 确认即可，不阻断任何事 |
| 排除 | `ignore_dirs` / `ignore_files` | 完全不看，表里的行也清掉 |

测试、fixture、生成的配置放"观察"档：你想知道它们变了，但不想为每个都维护一行纲要。想为观察档文件建立正式认知，把它加进 `keep_files`（keep 压过 observe）。

**怎么改这些配置**（本节与下一节的 JSON 片段都是 `ortg.tsv` 里那个 JSON 对象的键，不是能单独执行的东西）。两种做法：

- 对模型说，例如"把 testdata 放进观察档""忽略 node_modules"，它会调用 `ortg_header` 改好。
- 手工编辑：打开仓库根的 `ortg.tsv`，找到纲要头下面那个 `{ … }` 对象，把片段里的键合并进去（新表是 `{}`，要补上外层大括号，键之间加逗号），保存后执行 `ortg scan` 让它生效。

### 目录纲要：整个文件夹一条

第三方库、vendored 代码这类**不改内容**的目录，不必逐文件写纲要：把目录放进观察档，再给**目录本身**写一行纲要。它就是一个普通条目，只是文件名以 `/` 结尾。在会话里对模型说一句（换掉目录名、描述和模块）：

```
把 vendored 配成目录纲要：第三方库 foo v1.2，只读不改，升级走 go get 后 go mod vendor；它给 K 模块用
```

模型会把 `vendored` 加进 `observe_dirs`，再对路径 `vendored` 提交一行：

```
vendored/[3 L K T]: Role:第三方库 foo v1.2 | Uses:- | API:- | Constraints:只读不改,改行为去上游提 PR,升级走 go get 后 go mod vendor
```

- **并入用到它的模块**：目录纲要的内容往往自成一体，但不单开模块——B 标签填用到它的那个模块（上例是 K）。于是它算进模块清单里 K 的文件数，模型取 K 模块时一起拿到，增量发送、变动重发也和普通条目一样。这条也写进了注入给模型的规则里。
- **推荐观察档而不是排除档**：观察档照样记指纹，有人升级或手改了库，下次对账会报"观察变动"。只有 `node_modules` 这种体积巨大、变了也无所谓的才用 `ignore_dirs`；排除档的目录同样可以写目录纲要。
- 描述写清**是什么、版本、能不能改、怎么升级**，模型据此决定要不要读具体文件。模型真要改目录里的某个文件时，改动前会被注入离它最近的目录纲要，同会话同文件一次。
- 目录纲要行不会过期（目录本身没有内容指纹），目录还在就算对齐；目录删了会变成墓碑，和文件一样。
- `dirs`（`ortg_header` 的参数）只是纲要里目录段的标题，和目录纲要无关。

## 忽略与白名单

`ignore_dirs`/`ignore_files` 是减法，`keep_files` 是加法：整个目录忽略掉、只留里面几个用得到的文件，就写

```json
"ignore_dirs": ["node_modules", "vendor", "composetp"],
"keep_files": ["composetp/docker-compose.yml", "composetp/.env"]
```

`keep_files` 压过 `ignore_dirs` 与 `ignore_files`，但压不过内置的结构性排除（`ortg.tsv`、`overview*.txt`、`build/`）。

**每份名单内部按 gitignore 的规矩来**：从上往下扫，**后匹配者胜**，`!` 前缀取反。所以整棵留下再挖洞不用拆成几十条：

```json
"ignore_dirs": ["composetp"],
"keep_files": ["composetp/**", "!composetp/vendor/**", "composetp/vendor/critical.yml"]
```

整个 composetp 留下 → vendor 那块挖掉（落回 ignore 判定）→ 洞里的 critical.yml 再打回来。同一档内先扫目录名单再扫文件名单，所以 `ignore_files: ["!vendor/keep.go"]` 能在 `ignore_dirs: ["vendor"]` 上打一个洞。**档与档之间的优先级是固定的，不受顺序影响**。带 keep 的目录即使被忽略也照样遍历——否则里面的文件根本不会被列出来，白名单也就无从生效；被忽略的子仓库同理。

## 多仓库工作区

根目录下嵌套独立子仓库时，每个仓库用它自己的 `git ls-files` 枚举，再合并——git 不下钻嵌套工作树，只问根仓库会让子仓库整个消失。根 `.gitignore` 藏起子目录不影响住在里面的仓库；但子仓库自己忽略的目录（vendor/、node_modules/）里的仓库会连子树一起跳过。

## 可选：CBM 调用图

装了 [codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp)（二进制在 PATH 里）就自动启用，不用配置：改文件前的注入会多出一段"调用图关联文件"，列出调用它和被它调用的文件（最多 20 个，只给文件名、标签和 F 字段）。图谱由 ORTG 在会话开始和每次 `ortg_update` 后于后台增量刷新，项目名为 `ortg-<8位hex>`。没装、未建图或查询超过 4 秒都静默跳过。设 `ORTG_CBM=off` 关闭。细节见 `docs/rules.md`。

### 新机器安装

1. 只装二进制，不让它改各 agent 的配置（ORTG 只用它的 `cli` 模式；要它自己的 MCP 工具就去掉 `--skip-config`）：

   ```bash
   curl -fsSL https://raw.githubusercontent.com/DeusData/codebase-memory-mcp/main/install.sh | bash -s -- --skip-config
   command -v codebase-memory-mcp   # 默认装到 ~/.local/bin，必须在启动 Claude/Codex 的那个 PATH 里
   ```

2. 常驻进程与图谱不用手动配置，由 ORTG 自己管：每次会话开始，session hook 在后台跑 `ortg graph`，先 `codebase-memory-mcp daemon start`（幂等，已在跑就直接返回），再为本仓库增量建图。重启机器、重启容器之后，下一个会话会把常驻进程重新拉起来，不需要 systemd 或 cron。

   常驻进程不能缺：没有它，每次查询都要冷启动（快机器约 2.7 秒，4 核服务器超过 4 秒），会超过 pre hook 的 4 秒查询时限，关联段会静默消失；有它约 1–2 秒。顺序也不能反：先建图的话，建图命令会自带一个临时常驻进程，`daemon start` 撞上它只回"已在运行"，建完图就跟着退了。

   想不开会话就手动预热，在仓库里跑：

   ```bash
   ortg graph                          # 拉起常驻进程并刷新本仓库调用图
   codebase-memory-mcp daemon status   # 应显示 active (permanent)
   ```

3. 验证：在已启用 ORTG 的仓库开一个新会话，然后

   ```bash
   codebase-memory-mcp cli list_projects --limit 1000 2>/dev/null | grep -o "\"name\":\"ortg-[0-9a-f]*\",\"root_path\":\"$PWD\""   # 在该仓库根目录执行，应输出一行 ortg-xxxxxxxx
   ```

   之后让模型 Edit 一个有调用关系的文件，注入里应出现"调用图关联文件"一段。

升级 CBM 用 `codebase-memory-mcp update`：升级会停掉常驻进程，下一个会话开始时自动拉起；升级后要重开 agent 会话。

## ortg.tsv 格式

三段：首行 `#ORTG-TSV: 2`，随后以 `#` 开头的纲要头；一个 JSON 对象（`ignore_dirs`、`ignore_files`、`keep_files`、`observe_dirs`、`observe_files`、`dirs`）；列头加十一列 TSV：`path crc32 ignored deleted mtime module outline outline_time target_outline target_time target_delete`。`module` 是纲要行 B 标签，落盘时从 outline 算出，不单独维护；核心只认这一种格式，列头不符即 `ErrFormat`；旧版写的表（版本 1：十列无 module、`future_*` 列名、纲要行的 F/R/A/S 字段与紧凑标签、纲要头里的"未来"字样）由同一二进制里的 `migrate` 包在会话开始或写入口就地升级，原文件留在 `ortg.tsv.old`；认不出的（多半是比 ortg 新的表）停用本会话并提示升级 ortg。单元格不含制表符与换行，按 path 排序，原子落盘；每次内容变化前先把旧文件存为 `ortg.tsv.bak`（与 `.lock`、`.tmp*` 一起由 `.git/info/exclude` 排除，不进表）。损坏的 `ortg.tsv` 会被拒绝加载而不是覆盖，可从 `.bak` 恢复。旧格式兼容全部在 `migrate/`：核心不 import 它，只有 logic.go 在 `ErrFormat` 时调 `migrate.Table`、建表时调 `migrate.Import`；哪天不要某种旧格式，只删 `migrate/` 里对应代码。与代码不一致时以 `base/tsv.go` 为准。

## 本仓库自用

`ortg.tsv` 的 `ignore_dirs` 需包含 `纲要设计`；`overview.txt` 与 `ortg.tsv` 本身内置排除。

本仓库不自己注册 hook（`manifest_test` 守着）：hook 由装好的插件提供，再抄一份就是跑两遍。根目录 `.mcp.json` 是插件的 MCP 声明，在本仓库里又会被当成项目 MCP 与插件那份重复，所以 `.claude/settings.json` 用 `disabledMcpjsonServers` 把它关掉。

命令（直接执行 `ortg` 看完整用法）：`ortg scan`、`ortg status`、`ortg align --all` 或 `ortg align 路径...`、`ortg target --path P --outline L`、`ortg target --path P --delete`、`ortg target --document FILE`、`ortg graph`、`ortg hook --event E`（宿主调用）、`ortg mcp`（宿主调用）。策略规则表见 `docs/rules.md`。

## 作者与许可证

作者：Fei ding。

本项目以 Apache License 2.0 发布，全文见 [LICENSE](LICENSE)：可以自由使用、修改、商用和再分发；再分发或发布衍生作品时，须保留 LICENSE 与 [NOTICE](NOTICE) 中的版权与作者署名（协议第 4 条）。
