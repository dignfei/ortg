package main

import "strconv"

// All model-facing texts live here and only here.

const semanticInitRule = "语义初始化：阅读源码正文（长文件分段读完）及可见的相关测试，AST、导入和符号清单只辅助定位，不能代替正文阅读与语义概括。Role 写实际职责；Uses 按职责与契约的重要性选择强依赖，不按导入顺序截取前五项；API/Constraints 只写有依据的契约与约束，无内容不伪填，不把未提供的固定文案猜写成已知约束。条目齐全不等于语义质量已核验。"
const outlineConflictRule = "纲要与需求、源码、测试或实际运行结果冲突时，读取对应源码与测试核实，并修正不准确的纲要；状态对齐不证明代码正确或语义完整。"
const dependencyBlockerRule = "任务依赖闭环：发现仓库缺失符号、导入失败或其他依赖缺口，按实际调用路径与可见测试判断是否阻断目标功能或验收。阻断的纳入修复计划，不能仅因文件未点名或改动前已缺失而排除；确与任务无关的按证据说明。"
const finalSourceRule = "最终验证：为绕过仓库缺失实现而临时注入或替换的函数、对象只作诊断。提交或结束任务前，移除这类替代，在新进程中按最终持久源码运行验收；仅替代后的自测通过不算最终源码通过。"
const entryContractRule = "多入口契约：同一行为由 setter、预检、包装或快捷分支处理时，按入口与触发条件核对可见契约；提前校验或迁移后，异常类型、已明确给出的词句与输出格式仍须满足各入口约定，并分别覆盖各入口，不只验证底层函数。"

const rulesBlock = `## ORTG 纲要规则
本仓库由 ORTG 维护一份纲要：每个源码文件一行 "文件名[标签]: Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束"，模型通过纲要头规则加全部条目理解整个系统。
三条硬规则：
1. 开工先调用 ortg_overview（不带参数）取纲要：整份不超过整份预算（估算的 token 不超过上下文窗口的一半，大小不超过约 48 万（单次截断值 50 万留出余量，Claude 按字计、Codex 按 token 计））就整份返回；超过时只返回纲要头与模块清单，再按问题用 module 参数取相关模块的纲要（可多次；module="*" 取全部），再决定下一步。发过的条目一直在上下文里，不再重发（再取只补还没有或已变动的，全都有就只回对账报告；上下文压缩或清空后会自动重发）——不要和 Grep/Read/Glob 并行发出。纲要就是本仓库的认知：能从纲要回答的问题直接回答，不重复读源码；只有纲要没写的具体事实（某个值、某段实现、某个行号），或遇到需求、测试及运行结果冲突时，才读对应文件核查并修正纲要。
2. 读到没有纲要或纲要过期的文件，读完顺便按纪律生成一行纲要，调用 ortg_update 提交。
3. 改代码：只改一个文件时直接改。要改多个文件（含新建、删除）时先定计划、核对、再动手：用 ortg_target 的 items 一次列出这次要改、要新建、要删的每个文件的目标纲要，调用 ortg_review 按全局纲要逐项核对，补改后再核对，直到连续两次核对计划不变——在此之前改第二个文件会被拦；这是自查，不用给用户看、不用等同意，核对完直接动手。改完一个文件，立即为它提交新纲要（ortg_update）——这是改动本身的一部分：不征求同意、不留到最后、不当成待办报告给用户；纲要没提交，这个文件就不算改完。所有字段重写完整，不只写增量。再判断这次改动影响到哪些文件（Uses 依赖它的、调用图里调用它的、全局纲要里语义相关的）：要跟着改代码的就改，纲要描述随之不准的就重写它们的纲要提交；纲要头里的全局描述（依赖层次、模块划分、系统约定）随之不准的，用 ortg_header 改。
状态规则：纲要是否对齐以 ortg_status 的实时对账为准；被问到"对齐了吗 / 有哪些文件要处理"时重新扫描再答，不引用开工时的报告——那只是快照，之后文件可能已经变了。
骨架规则：纲要中只有裸文件名没有纲要的行，是尚未建立认知的文件清单；下次读取该文件时必须顺便生成一行纲要并调用 ortg_update 提交。
目录纲要规则：第三方库等不逐文件写纲要的目录，给目录本身写一行纲要（文件名以 / 结尾，如 vendored/[标签]: ...），目录放进 observe_dirs。它的内容往往自成一体，但不要为它单开一个模块：B 标签填用到它的那个模块，它就并入那个模块、随那个模块一起发送。
目标规则：条目下一行 "#目标:" 是该文件的目标纲要——这个文件改完之后应有的纲要，表示要改代码去达到它；"#目标:删除" 表示计划删除；"#计划新建" 表示文件尚不存在。改完后用 ortg_update 提交，目标纲要自动清空。只改实现、改完纲要不变的文件（重构、格式化、修 lint），ortg_target 的 outline 传 "=" 即可，不必把整行纲要再抄一遍，纲要里显示为 "#目标:同现在"。目标纲要只写要改代码去达成的状态：待核查、待实测的结论和疑问不是目标，也不写进当前纲要——先核查，再把事实写进当前纲要。
高屋建瓴模式：只迭代目标纲要、纲要头、忽略规则与目录描述，不改代码，用 ortg_target 与 ortg_header 提交。` + "\n" + semanticInitRule + "\n" + outlineConflictRule + "\n" + dependencyBlockerRule + "\n" + finalSourceRule

// codexBudgetRule is injected only under the Codex host: it wraps MCP calls in
// functions.exec and cuts the middle out of a tool result that overruns the
// budget, keeping head and tail, without any error. The pragma (equal to
// codexLimits.max) raises the exec budget for the one call; what history keeps
// is capped by tool_output_token_limit in config.toml. A reply arrived whole
// when its last line is the closing line every ortg_overview reply ends with
// (all start with "以上") and no "tokens truncated" marker sits in between.
const codexBudgetRule = `Codex 宿主补充：本宿主用 functions.exec 包装 MCP 调用，工具结果超过预算会被从中间截掉、只留首尾——不报错，被截掉的文件你根本不知道存在。故调用 ortg_overview 时，外层命令第一行必须是 // @exec: {"max_output_tokens": 500000}；它只抬高这一次工具结果的预算，不是模型输出上限，也不改纲要内容。ortg_overview 的每种回复（模块或文件的纲要、模块清单、短回复）最后一行都是以"以上"开头的收尾句，正文里也不会有 "tokens truncated" 字样；最后一行不是这样，或中间出现 "…N tokens truncated…"，就是被截断了：照此重发一次并传 force=true（本会话已计为发过，不传只会拿到短回复）。重发仍被截断，多半是 ~/.codex/config.toml 没设 tool_output_token_limit = 500000（不设时 Codex 存进对话的工具结果只留约 1.2 万 token，@exec 抬不过它）或 [mcp_servers.ortg] 没设 env = { ORTG_AGENT = "codex" }：告诉用户按 ortg 的 README 补上后重启 Codex，在此之前按模块分次取。不要拿半份纲要开工，也不要用 Grep/Read 去补缺失的部分。`

// The ortg_overview replies. Each ends with its own closing line that starts
// with "以上" — codexBudgetRule tells Codex that a reply whose last line does
// not is truncated. overviewPartFooter, closing the outline entries, restates
// rule 1 at the point where the model decides whether to open files.
//
// overviewAlreadyRead: every requested entry is already in the context; %s
// is the scope (整份纲要 / 模块 X), %d the entry count.
const overviewAlreadyRead = "ortg: 你要的%s共 %d 条纲要本会话都已发给你，仍在你的上下文里，直接用它们，不要再取。下面只给实时对账报告（这是短回复，不是被截断）。上下文压缩或清空后会自动重发；若你确实看不到这些条目，调用 ortg_overview 并传 force=true。"
const overviewShortFooter = "以上是实时对账报告，纲要条目本身仍在你的上下文里。"

// sessionReportNote follows the alignment report in the session-start
// context; sessionFetchNote replaces everything when reconciliation timed out.
const sessionReportNote = "第一次回复用户时，先用一句话报告上面的对账结果（对齐/过期/无纲要各多少），再谈正事。"
const sessionFetchNote = "开工前先调用 ortg_overview（不带参数）取纲要（超过整份预算时先给纲要头与模块清单，再按问题取相关模块）。"

// overviewModulesHead opens the module list of a bare call on an outline
// over the whole budget: its size (overviewSizeBoth under Claude Code, the
// characters and the estimated tokens; overviewSizeEst under Codex, whose own
// unit is already tokens, the estimate alone), the file count, and which
// budget it overran.
const overviewModulesHead = "ortg: 上面是纲要头，下面是模块清单（整份纲要%s、%d 个文件，超过%s）。按用户的问题用 ortg_overview 的 module 参数取相关模块的纲要（可多次，只发上下文里还没有的条目）；module=\"*\" 取全部。模块清单（B 标签 / 文件数 / 大小 / 已在上下文的条目数，模块名见纲要头 #B 字典）："

// overviewBudgetWindow (%d the window in tokens) and overviewBudgetCap (%d
// the cap, %s its unit) name the budget a bare call overran.
const overviewSizeBoth = "约 %d %s、估约 %d token"
const overviewSizeEst = "估约 %d token"
const overviewBudgetWindow = "上下文窗口（%d token）的一半"
const overviewBudgetCap = "单次返回上限减余量（%d %s）"
const overviewModulesFooter = "以上是模块清单，纲要正文还没发；按问题用 module 参数取。"

// overviewTooLarge leads a fetch that exceeds the host limit even with the
// raised threshold, i.e. the module split is too coarse; the body follows
// anyway. %s scope, %d its size, %d the limit.
const overviewTooLarge = "ortg: %s的纲要约 %d %s，超过单次返回上限 %d %s——说明模块划分不够细。下面照发正文，可能被截断、你只看到前面一部分：据你看到的条目拟一份拆分方案给用户（新模块名、各含哪些文件、各约多少字），用户同意后再改。拆分原则：①按业务域与职责划分，不按目录——一个模块只管一组相关的概念、数据和接口；②高内聚低耦合——沿 Uses 依赖稀疏处切，模块内互相依赖多、跨模块依赖少；③公共底层（工具、配置、基础设施）单独成模块，不塞进业务模块；目录纲要随用到它的模块走，不单独成模块；④每个模块宜在单次上限的十分之一以内，一个问题取两三个模块合计也远低于上限。落实：改相关纲要行的 B 标签用 ortg_update 提交，并在纲要头 #B 字典登记新模块（ortg_header）。这次不计为已发，拆好后按新模块重取，被截掉的条目也就取得到了。"

// overviewIncrementNote precedes the closing line of an incremental send: %d
// sent now, %d already in the context.
const overviewIncrementNote = "ortg: 本次只发了 %d 条还没发过或已变动的纲要，另有 %d 条本会话已发过、仍在你的上下文里。"

// overviewPartFooter closes a module fetch; %s is the scope.
const overviewPartFooter = "以上是%s的纲要。能据此回答的问题直接回答，不重复读源码；具体事实没写或出现冲突时读对应文件核实；别的模块的问题再按模块取。" + outlineConflictRule

const skeletonRule = `ortg: 该文件尚无纲要。读完后按纪律生成一行纲要并调用 ortg_update 提交——直接做，不询问。`

// The S budgets are the one text here not written by hand: quotaLine renders
// logic.go's sQuota, the same table checkOrError enforces, so the discipline and
// the header template cannot drift from the checker.
var updateDiscipline = `纲要行纪律：单行；格式 文件名[标签]: Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束；标签 [C A B D… E] 是重要度数字在前、其后层级、模块、特征(0到多个)、规模，以空格分隔，每个都是纲要头字典里的大写字母(模块可两个)；Role 一句话写这个文件的职责，测试文件只写一句测什么，不逐项罗列，文档文件一句话说它讲什么；跨模块、不靠调用关系的约定不写进 Uses：在纲要头登记 #【契约】键：正文，本文件参与就在行末加 | Keys:键(角色),…，键须已登记(未登记会被拒)；Uses 只列本文件依赖的跨文件强依赖(≤5)，一律写仓库相对路径：文件写全路径(如 lib/storage/cache.c)，整个目录或包写目录路径并以 / 结尾(如 lib/storage/)，可加括注写用到什么，包名、模块名、裸文件名都不行，第三方库与标准库不写；API 只列跨包或对外的契约(命令、工具名、给别的包用的导出名，以及测试代码要调用的入口——写"测试也调用"，不写"单测入口"：它们照样是对外契约，不得加 cfg(test)/条件编译或收窄可见性，判分或别的测试可能整段替换测试代码)，包内标识符不列，无则写 -；同一个参数或限额能由多层执行时，在接线文件的纲要或【契约】正文里写明由哪一层执行、其他层不再执行；给文件新增的对外接口(Rust pub 项、Go 导出名、JS/TS export)必须写进 API；Constraints 只写不读代码就会做错的约束、不变量与陷阱，不复述 Role 与 API，代码里一眼可见的实现细节不写，按 C 配额 ` + quotaLine() + `(「必须保持」子句另计，不超过该档配额的一半、至少 100 字，不占其余约束的字数)；测试契约：本文件的单元测试或别处的集成测试锁定了的对外行为（输出格式、参数组合的结果、边界值、错误语义），写成"必须保持 X（测试名）"，写清期望结果，不写成"可能/现状是 X"——这是改动时不许改变的东西；这种约束另有模块依赖时，在依赖方的纲要里也写上；测试配置按依赖版本或平台整类跳过、本地跑不到的测试（CI 或别的环境可能不跳过），在被测文件的 Constraints 里写成「本地验证盲区：X（跳过条件）」——它们锁定的行为本地无法验证，改动碰到时要逐行核对实现，不能当作已通过；文档文件的 Constraints 写它规定的约定、约束与设计决定，按 ` + strconv.Itoa(docQuota) + ` 字配额；禁时间维度；所有字段重写完整。` + " " + semanticInitRule + " " + entryContractRule

const decisionText = `未对齐清单怎么处置由用户决定，三个选项由程序在用户发出第一句话时直接显示给他，你不要再主动提问，也不要把清单复述一遍。用户回「1」= 逐个读文件、按纪律生成纲要行并用 ortg_update 提交；「2」= 人工已核对，调用 ortg_align mode=all 前移指纹；「3」或没提 = 现在什么都不做，读到或改到哪一个再顺手提交。本条只管这份存量清单：改完某个文件后立即为它提交新纲要仍按硬规则 3 执行，不询问。`

// decisionPrompt goes to the user verbatim through the systemMessage of the
// UserPromptSubmit hook: whether the choice is offered, and in what words, must
// depend neither on the model relaying it nor on the host rendering a
// resume-time SessionStart message, which it does not reliably do.
const decisionPrompt = `未对齐的文件怎么处理？回数字即可：
1、立刻让模型逐个更新纲要
2、人工已核对，纲要就是最新的（只前移指纹）
3、（默认）先不处理，读到或改到哪个再顺手补`

var headerTemplate = `#====项目纲要====
#【部署】(写本项目的部署/构建/运行形态与关键命令)
#【系统】(一句话：系统是什么 | 核心架构与技术栈 | 不可破坏的顶层原则)
#【整体规范】
#纲要格式：文件名[标签]: Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束 ；文件名不带路径；四字段必填,无内容写-
#骨架规则：只有裸文件名没有纲要的行是尚未建立认知的文件,读取时顺便生成纲要并调用 ortg_update 提交
#目标规则：条目下一行 #目标: 是目标纲要(改完后应有的纲要,不写待核查事项)；#目标:删除 计划删除；#计划新建 文件尚不存在
#测试契约规则：被测试锁定的对外行为写成"必须保持 X(测试名)"并写清期望结果；只涉及一个文件的写进它的 Constraints,跨模块的登记进【契约】；需求与守护测试冲突时按证据裁决:需求点名该测试或说旧行为错→改测试,需求说测试权威/行为不变/只是迁移→保持,有命令与期望输出的验收例→按实跑结果,都没有→先求两全,不能两全按需求实现,并把选择与理由记进纲要
#契约规则：跨模块、不靠调用关系的约定(同一概念由多层共同实现、协议与消息格式、单位、兼容性、HTTP/WebSocket 路由与消息类型、配置键)在纲要头登记一行 #【契约】键：正文(正文写约定与守护测试,只写这一处)；每个参与文件在纲要行末尾加 | Keys:键(角色),… 声明它；路由、消息、事件、配置、环境变量类的键写成 route:/ws:/msg:/event:/topic:/cfg:/env: 加字面值
#===代码纲要规范===
#【标签】[C A B D… E] 重要度数字在前,其后层级、模块、特征(0到多个)、规模,空格分隔,每个是一个大写字母(模块可两个)
#A层级: E入口 C核心 S服务 D数据 I集成 U工具 T测试 B构建 X配置 O文档
#B模块: G通用(请按本项目业务域替换,每个模块一个大写字母,如 A认证 U用户 O订单)
#C重要度: 9核心 8高频 7业务 5常规 3辅助 1边缘
#D特征: (按技术特征定义,可为空,如 J-JWT T事务 A异步)
#E规模: L大>400 M中200-400 S小100-200 T微<100
#Constraints配额: ` + quotaLine() + `
#===代码纲要规范完毕===`

const targetModeText = `高屋建瓴模式：本轮只改目标纲要、纲要头、忽略规则与目录描述，不改代码；请输出完整目标纲要而非差异。`

var toolDescriptions = map[string]string{
	"ortg_overview": "对账并返回纲要与对账报告。不带参数时整份不超过整份预算（估算的 token 不超过上下文窗口的一半，大小不超过约 48 万（单次截断值 50 万留出余量，Claude 按字计、Codex 按 token 计））就整份返回，超过时只返回纲要头与模块清单（不含条目正文）；再用 module（B 标签）取模块，module=\"*\" 取全部。同一会话只发上下文里还没有或已变动的条目，全都发过就只回对账报告；上下文压缩或清空后自动重发；确实看不到时传 force=true 重发。一次超过单次上限时照发正文（可能被截断），开头提醒把模块拆细。仓库无 ortg.tsv 时：根目录有 overview.txt 则先备份再整份导入建表，否则建空表；再首扫。开工先调用。",
	"ortg_status":   "重新扫描并对账，只返回对账报告（无纲要/过期/观察变动/对齐各多少及路径），不返回纲要正文。被问到纲要是否对齐、有哪些文件要处理时用它——会话开始的报告只是当时的快照，不要引用。",
	"ortg_update":   "提交一个文件的纲要行（文件名[标签]: Role:.. | Uses:.. | API:.. | Constraints:..）。校验格式与字典后写入并前移指纹；该文件若有目标纲要则同时清空。第三方库等不逐文件建纲要的目录，给目录本身写一行目录纲要：path 填目录，文件名以 / 结尾（vendored/[标签]: ...），B 标签填用到它的模块，它就随那个模块一起发送；目录本身放进 observe_dirs，其下文件就不再要求逐个写纲要。",
	"ortg_review":   "核对改动计划（参数 rulings：对上一次核对给了正文的每条契约写一行「契约键：裁决」，缺了不收敛）：列出所有目标纲要（要改、新建、删除的文件）的现在→目标字段变化、不合格的目标行、Uses 依赖它们却还没有目标纲要的文件，并给出逐项核对清单。要改多个文件时，写完计划后调用；连续两次调用看到的计划一样，计划里的文件才解锁可改。只改一个文件不需要。只给自己核对用，不用给用户看。",
	"ortg_target":   "写目标纲要（这个文件改完之后应有的纲要，用来指导接下来改代码；待核查、待实测的事项不是目标，不要写进来）：给 path 与 outline 写单个文件的目标纲要，delete=true 标记计划删除；或传 document 整份导入（高屋建瓴模式）。允许尚不存在的路径。改完代码后用 ortg_update 提交，目标纲要自动清空。只改实现、改完纲要不变时 outline 传 \"=\"（等于现在的纲要，不必抄整行）。一次改多个文件时用 items 一次传全部：每项 \"路径 目标纲要行\"，纲要行写 = 表示纲要不变，写 删除 表示计划删除。只改一个文件不需要写目标纲要。",
	"ortg_align":    "人工已核对纲要对齐时调用：mode=all 前移全部文件指纹，mode=paths 只前移 paths。不改纲要文本。",
	"ortg_header":   "整块替换纲要头（必须含 #A/#B/#E 字典行），并可设置忽略目录、忽略文件、白名单、观察目录与目录标题（keep_files 压过忽略规则；dirs 为 \"目录=标题\" 列表，只作纲要里目录段的标题）。传了的列表整体替换，不传的字段保持原值。",
}

const (
	prefixDeleted      = "#已删除 "
	prefixPlanned      = "#计划新建 "
	prefixTarget       = "#目标: "
	prefixTargetDelete = "#目标:删除"
	prefixTargetSame   = "#目标:同现在(只改实现,改完纲要不变)"
	bodyMarker         = "#代码纲要"
)

// tableFormatNote stops a session on a table whose format this ORTG does not
// know and migrate could not upgrade — most often a table a newer ORTG wrote;
// tableBrokenNote stops it on any other unreadable table. Both end with the
// error text.
const tableFormatNote = "ortg: ortg.tsv 的格式本版 ortg 不认识（多半是 ortg 比这张表旧，请升级 ortg；表本身没坏，不要从 .bak 恢复），本会话 ORTG 已停用: "
const tableBrokenNote = "ortg: ortg.tsv 无法加载，本会话 ORTG 已停用，请先修复或从 ortg.tsv.bak 恢复: "

// emptyTableNote and migrateHint cover bootstrapping from legacy outline documents.
// The first build of a fresh table runs docs first, then the header, then
// the source: headerFirstNote (%s the documents still without an outline, %d
// the document quota) leads an overview of a fresh table and refuses anything
// but a document's outline; headerFromDocsNote takes over once every document
// has one, headerNoDocsNote when the repository has none; headerDocsMissingNote
// (%s the documents) refuses a written header while documents still lack one.
const headerFirstNote = "ortg: 纲要头还没写（【部署】【系统】仍是模板占位）。建纲要的第一步是给文档写纲要，源码纲要与目标纲要要等纲要头写好才收。还没纲要的文档：%s。逐份读全文（不要用 head、sed 截断），用 ortg_update 提交：Role 一句话说它讲什么，Constraints 写它规定的约定、约束与设计决定（文档按 %d 字配额，不按重要度）。教程、翻译、大批参考页这类可以放进观察档（ortg_header 的 observe_dirs、observe_files），或给所在目录写一行目录纲要。文档纲要齐了，再据此与目录结构写纲要头（ortg_header）：系统是什么、部署与运行方式、架构与模块划分、依赖层次、数据流、不可破坏的约定（含被测试锁定、跨模块的对外行为），并登记 #B模块 字典；然后逐个读源码写纲要，同时读它的单元测试和相关的集成测试，按测试契约把它们锁定的行为写进 Constraints。文档只作背景，纲要写代码实际怎么做；文档与代码不一致时按代码写，并在那份文档的纲要里记下不一致之处。" + " " + semanticInitRule
const headerFromDocsNote = "ortg: 文档纲要已经齐了，纲要头还没写（【部署】【系统】仍是模板占位）。根据文档纲要与目录结构写纲要头（ortg_header）：系统是什么、部署与运行方式、架构与模块划分、依赖层次、数据流、不可破坏的约定（含被测试锁定、跨模块的对外行为），并登记 #B模块 字典；写好之后才收源码纲要与目标纲要。文档只作背景，与代码不一致时按代码写。"
const headerNoDocsNote = "ortg: 纲要头还没写（【部署】【系统】仍是模板占位），仓库里也没有文档。先读入口文件与目录结构，写纲要头（ortg_header）：系统是什么、部署与运行方式、架构与模块划分、依赖层次、数据流、不可破坏的约定（含被测试锁定、跨模块的对外行为），并登记 #B模块 字典；写好之后才收文件纲要与目标纲要。"
const headerDocsMissingNote = "ortg: 还有文档没写纲要，先不写纲要头：%s。逐份读全写文档纲要（用不上的放进观察档），再写纲要头；只改忽略、观察等配置不受此限。"

// headerDepNote (%s the file) follows an ortg_update whose Uses is new or
// changed: the header's description of dependencies and layers does not
// follow the outlines by itself.
const headerDepNote = "ortg: %s 的 Uses（依赖）是新写的或变了。看纲要头里描述依赖与分层的部分（如【依赖层次】）是否随之不准，不准就用 ortg_header 改——纲要头不会自动跟着变。"

// distillNote (%s the documents still in the cognitive tier) follows the
// first outline of the last file without one: the first build ends by moving
// the documents into the observe tier so the table does not say it twice,
// after checking point by point that each of their conventions has a place in
// the header or a source outline — the program cannot judge that coverage.
const distillNote = "ortg: 本仓库的文件都有纲要了。若这是首次建纲要，还剩收尾一步：把还在认知档的文档放进观察档（ortg_header 的 observe_files、observe_dirs），纲要里不再重复同一内容。放之前逐份逐条核对：列出这份文档纲要 Constraints 里的每条约定、约束与设计决定，各自现在写在纲要头的哪一段，或哪个源码文件纲要的 Constraints 里；找不到落点的先补上（纲要头用 ortg_header，源码纲要用 ortg_update 重写整行），文档与代码不一致的记录也落到对应源码纲要里。全部有了落点再放进观察档——放进去后文档纲要不再发给模型，没落下的就丢了。测试同理：在观察档里的测试（含整个测试目录）只记指纹、不发给模型，逐个确认它们锁定的对外行为已按测试契约写进被测源码的纲要或纲要头，没写的补上。要留在纲要里的文档（如顶层 README）可以不放、不用核对。文档以后改了，对账会报「观察变动」，届时重读它、同步纲要头与源码纲要再 ortg_align。还在认知档的文档：%s。（子代理收到这段，转告主会话。）"

const emptyTableNote = "已创建 ortg.tsv（空表）。若本仓库已有旧纲要文档（例如 ORTG v1 的 overview.txt 分卷，可能在被忽略的目录里），按下面的迁移步骤合写后用 ortg_target 的 document 参数整份导入。"

const migrateHint = `overview.txt 不合本协议（%v），已建空表。旧文档已重命名为 %s。请这样迁移：
1. 找齐旧纲要文档，可能是多个文件，也可能在被忽略的目录里：上面那份重命名后的备份是其中一份；ORTG v1 是 overview.txt 清单加 overview.meta.txt(标签字典)、overview.modules.txt(模块)、overview.code*.txt(代码条目)分卷。
2. 合写成一份符合本协议的完整文档。导入时已自动把索引换成纲要，措辞不必手工替换；要动手的是结构：
   - 纲要头必须含 #A/#B/#E 字典行，多套标签体系(前后端、数据库)要合并成一套；
   - 目录段头 ===描述 /绝对路径/=== 单独一行，路径要写本仓库的真实绝对路径，不是 v1 的 ===/仓库名/===；
   - 条目 文件名[标签]: Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束，四段必填、无内容写 -；
   - 非文件条目(v1 的模块行、旧的数据库表行)不是文件，删掉或改写成对应文件的条目。
3. 调用 ortg_target 传 document 参数整份提交，条目全部进目标纲要；逐个实现后用 ortg_update 应用。`

// relatedHead introduces the call-graph neighbours appended to a pre-edit
// injection; relatedCalls/relatedCalledBy mark each line's direction.
const relatedHead = `ortg: 调用图关联文件(本机 CBM 图谱推出，只作改动的审查范围参考，不是纲要 R，不要据此改 R)。改动若影响这些文件的调用约定，一并检查：`
const relatedCalls = "→调用"
const relatedCalledBy = "←被调"

// folderPreText is the pre-edit injection for a file under a folder outline:
// the file itself has no outline, the directory's outline line stands for it.
// %s file, %s directory, %s the directory's outline line.
const folderPreText = "ortg: %s 属于目录纲要 %s/(整目录一条纲要,不逐文件建纲要)，改动前请遵循其中约束:\n%s"

// gateDeny is the reason the gate gives when it refuses the first code read or
// write of a context that has not loaded the outline; gateRemind (%s the file)
// follows every later one, once per file; gateModule (%s the file, %s its
// module) asks for the module of a file whose entry the context lacks. They
// name the call to make, since the model re-plans on them. gateBadOutline (%s
// the file) is for an entry whose tag yields no module: no fetch can reach
// it, and the entry itself is what is broken.
const gateDeny = "ortg: 本会话还没加载纲要，这一步先拦下（只拦这一次）。先不带参数调用 ortg_overview 取纲要——纲要就是本仓库的认知，能从纲要回答的不用读源码——再重做这一步。"
const gateRemind = "ortg: 仍未加载纲要，%s 的认知在纲要里。先不带参数调用 ortg_overview，再读改代码。"
const gateModule = "ortg: %s 所在模块的纲要还没取，先调用 ortg_overview（module=%s）——纲要没写的具体细节再读文件。"
const gateBadOutline = "ortg: %s 的纲要行取不到模块（标签解析不出 B 段），这行纲要有问题。直接读这个文件，按纪律重写它的纲要，用 ortg_update 提交。"

// The write gate and ortg_review. gateWriteUnloaded refuses a write before
// the outline is loaded; gateMultiFile (%s the files) refuses a write that
// makes a second unplanned file, gatePlanUnreviewed (%s the file) one whose
// target is written but the plan not yet checked to a fixpoint. Review
// replies: reviewEmpty, reviewHead (%d the call, %d the files), the list of
// affected files without targets, the checklist, and either reviewAgain or
// reviewConverged (%d the files unlocked).
const gateWriteUnloaded = "ortg: 还没加载纲要，不能改文件。先不带参数调用 ortg_overview 取纲要再改；要改多个文件时，先用 ortg_target 的 items 一次写全目标纲要、用 ortg_review 核对。"
const gateMultiFile = "ortg: 这一步会让改动涉及多个计划外的文件（%s），是多文件改动，拦下（每次都拦）。先用 ortg_target 的 items 一次写全这次要改、要新建、要删的每个文件的目标纲要（改完后应有的纲要；只改实现、纲要不变的写 =），再调用 ortg_review 按全局纲要逐项核对；连续两次核对计划不变才解锁，之后照常改代码。若前一个文件其实已改完、是另一件事，先用 ortg_update 提交它的纲要再改这个。这是自查，不用给用户看、不用等用户同意，核对无误直接动手。"

// gateSoloEdit (%s the file) rides along the first write to a file outside
// any plan: a single-file change needs no plan, but its outline is due.
const gateSoloEdit = "ortg: %s 不在改动计划里，按单文件改动放行。改完立即用 ortg_update 提交它的新纲要；若还要改别的文件，先用 ortg_target 的 items 一次列全计划再核对。" + " " + dependencyBlockerRule + " " + entryContractRule
const gatePlanUnreviewed = "ortg: %s 的目标纲要已写，但计划还没核对到收敛（连续两次 ortg_review 看到的计划一样才解锁）。调用 ortg_review。"
const reviewSame = "- %s：只改实现，改完纲要不变（纲要见全局纲要）\n"

// Later reviews of one plan: reviewUnchanged (%s the file) stands for a
// file whose target did not move since the last review, reviewKeySeen (%s
// the key) for a contract quoted in full before, reviewAffectedSeen and
// reviewContractsSeen (%d how many) for affected files and fallback entries
// listed before. The conflict questions and the checklist go out once per
// plan.
const reviewUnchanged = "- %s：目标与本计划上次核对时相同\n"
const reviewKeySeen = "- 【%s】（正文与参与者见本计划上次核对）"
const reviewAffectedSeen = "（另有 %d 个依赖计划文件、没有目标纲要的文件，上次核对已列出）\n"
const reviewContractsSeen = "（另有 %d 条同名「必须保持」条目，上次核对已列出）\n"

// gateTestEdit (%s the file) rides along the first write to a planned test
// file: a failing guard test is a question about the requirement, not an
// expectation to bend to the new code.
const gateTestEdit = "ortg: %s 是测试文件。改它的期望值前按证据裁决：需求点名了这个测试或说旧行为是错的，才改期望；需求说既有测试权威、行为不变或只是重构迁移，就改实现、不改期望；需求给了命令与期望输出的验收例，以实跑结果为准；都没有，先找两全的实现，不能两全才按需求改，并把选择与理由记进纲要。新增测试照常写。依赖库版本不同造成的输出表示漂移（浮点位数、repr、警告文本）按实际输出更新期望，不属于行为裁决。"
const reviewEmpty = "ortg: 还没有任何目标纲要。先为这次要改、要新建、要删的每个文件用 ortg_target 写目标纲要，再调用 ortg_review。"
const reviewHead = "ortg: 第 %d 次核对改动计划，涉及 %d 个文件：\n"
const reviewAffected = "\n按全局纲要，下列文件的 Uses 依赖计划里的文件，却没有目标纲要——逐个判断：要跟着改的补目标纲要，不受影响的不用管：\n"

// reviewContracts heads, as a fallback for what the 【契约】 table misses, the guarded behaviour outside the plan that shares a
// name with it: a guard may sit in a module the plan never touches.
// usesNotPath (%s the items) refuses a Uses that names no repository path.
const usesNotPath = "Uses 里的 %s 不是仓库内的路径，纲要未写入。Uses 一律写仓库相对路径：文件写全路径，整个目录或包写目录路径并以 / 结尾（如 lib/storage/），可加括注；包名、模块名、裸文件名都不行，第三方库与标准库不写——依赖方按路径反查，写不成路径的依赖会被漏掉。改好整行重提。"

// reviewKeys heads the registered contracts the plan's files take part in;
// reviewKeyMissing (%s the literal, %s the files) names files using a
// literal key without declaring it; keysUnregistered (%s the keys) refuses a
// line declaring an unregistered key; keysOrphan (%s the keys) rides along a
// header whose table registers keys nobody declares.
const reviewKeys = "\n计划里的文件参与了下列契约（纲要头【契约】登记表）——逐条说明新实现是否仍满足；契约要改变，就同时改登记正文、全部参与者与守护测试，并确认需求明确要求；参与者里没进计划的，判断要不要跟着改：\n"

// reviewGuardTests / impactGuardTests (%s the tests) name the tests the
// outlines cite for the plan's or the edited files, so the model runs those
// and not a vague "the guard tests".
const reviewGuardTests = "\n守护测试（从计划文件与上列契约的「必须保持」提取，改完逐个运行；失败先回到需求确认，需求没有明确要求改变就改实现、不改期望值；需求里给了命令与期望输出的验收例也要实跑并逐字比对）：%s\n"
const impactGuardTests = "ortg: 这次改动涉及的守护测试（从纲要的「必须保持」提取）：%s。逐个运行；失败先回到需求确认，需求没有明确要求改变就改实现、不改期望值。"

// testOnlyWording warns an outline calling an entry "单测入口": read as
// "only tests use it", it invites hiding the entry behind cfg(test).
const testOnlyWording = "纲要里写了\"单测入口\"：测试也调用的入口照样是对外契约，写成\"测试也调用\"，并保持它的签名与可见性"

// sameWithKeysNote (%s the file, %s its keys) rides along "=" on a file that
// takes part in contracts: an implementation change there may move where a
// parameter is enforced, which the outline and the contract must then say.
const sameWithKeysNote = "ortg: %s 参与契约 %s。若这次改动改变了参数流向或执行点（哪一层执行限额、校验、默认值），不要用 =：写完整目标纲要，并用 ortg_header 同步改契约正文。"

// apiCfgTestNote / apiGoneNote (%s the file, %s the names) follow an edit
// that hid an API-listed function behind #[cfg(test)] or removed a name the
// outline lists; testRefNote (%s the file, %s each name with the test files
// using it) follows one that removed a top-level name tests still import or
// call.
const apiCfgTestNote = "ortg: %s 里纲要 API 列出的 %s 现在在 #[cfg(test)] 下。测试也调用的入口不得这样收起：判分会替换测试代码并删掉全部 cfg(test) 项，入口随之消失、测试编译失败。要消除未使用警告，就让生产代码继续调用它或去掉警告，不要加 cfg(test)。"
const apiGoneNote = "ortg: %s 里已找不到纲要 API 列出的 %s。对外列出的名字默认保留：需求没有点名删除或改名的，留着它（不再调用的辅助函数、或指向新实现的弃用别名都行）；需求确实要删或改名，调用方（含测试）都跟着改并更新纲要；若只是纲要没写全名，忽略本条。"
const removedUnchecked = "（另有 %d 个删掉的名字没有逐个查测试引用，删之前自己确认。）"
const testRefNote = "ortg: %s 删掉了仍被测试导入或调用的名字：%s。测试导入不到一个名字，整个测试文件收集或编译失败，其中所有测试都记失败——包括与这个名字无关的；之后加入的测试也会从同一处导入。需求没有点名删除或改名的，默认保留（不再调用的辅助函数、或指向新实现的弃用别名都行）；需求确实要删，就把引用它的测试一并改掉，并在那些测试文件里确认没有别的用处。"

// reviewConflict follows the contracts a review quotes: the three questions
// per contract and the order of evidence that settles a conflict between the
// requirement and a guard test — no side wins by rule, so a requirement that
// rightly changes old behaviour is not blocked.
const reviewConflict = `
上面每条契约（含兜底条目）逐条回答，写进回复：① 需求有没有直接讲到这个场景（同一参数组合或输入）？② 讲到的话期望是什么，与守护测试一致还是相反？③ 相反时按证据裁决，命中即停：a 需求点名了该测试或说旧行为是错的/以前是 X 现在改为 Y → 改测试与纲要；b 需求说既有测试权威、行为不变、只是重构或迁移 → 保持测试，改实现；c 需求给了命令与期望输出的验收例 → 实跑，以结果为准；d 都没有 → 先找同时满足两者的实现，不能两全才按需求实现。
下一次调用 ortg_review 时，在 rulings 参数里为上面每条给了正文的契约写一行「契约键：裁决」（例如「缓存过期：需求只讲容量上限，没讲过期时间；守护测试锁定过期后返回空，按 b 保持」），缺一条计划就不收敛；收敛时裁决会写进该契约正文，后面的改动据此一致。
`

// reviewMigration (%s the items) names duties moving into a file: its target
// declares a contract its outline does not. The new holder must pass the old
// guards on its own — the old layer kept on would mask a wrong new one.
const reviewMigration = "\n职责迁移：%s。原参与者的守护契约就是新承担者的验收标准；验证时让新承担者单独生效（暂时关掉原参与者那一层再跑守护测试），别让旧层兜底掩盖新实现的差别。两层都保留是允许的设计，但要分别测得过。\n"

// reviewRulingsMissing (%s the keys) keeps a plan from converging while a
// contract quoted in full has no ruling.
const reviewRulingsMissing = "\n还缺这些契约的裁决，计划不收敛：%s。再调用 ortg_review，在 rulings 里每条写一行「契约键：裁决」。\n"

// sameApiNote rides along "=": the outline only stays when no public
// interface is added; ortg_update refuses an outline whose API leaves out a
// name the file newly exports.
const sameApiNote = "（只改实现才能用 =：这次若给文件新增对外接口——Rust 的 pub 项、Go 的导出名、JS/TS 的 export——要写完整目标纲要并在 API 里列出，否则 ortg_update 会拒收。）"

// newAPINotListed (%s the file, %s the names) refuses an outline whose API
// leaves out a name the file newly exports.
const newAPINotListed = "%s 新增了对外接口 %s（git HEAD 里没有），纲要 API 里却没写，纲要未写入。把它们写进 API，并按新接口改写 Role/Constraints（新增的接口往往意味着职责或执行点变了，相关契约也要核对），再提交整行。"

const reviewKeysRest = "其余也涉及的契约（括号里是参与者数，正文见纲要头【契约】，改动碰到时同样要守）：%s"
const reviewKeyMissing = "源码里还有这些文件含 %s 却没在 Keys 里声明这个键，逐个核实：确实参与就补声明并判断要不要跟着改：%s"
const keysUnregistered = "Keys 里的 %s 不在纲要头的【契约】登记表里，纲要未写入。先用 ortg_header 在纲要头登记一行 #【契约】键：正文（约定内容与守护测试只写在这一处），再提交本行。"
const keysOrphan = "登记表里的契约键 %s 还没有任何文件在 Keys 里声明：把参与它的文件纲要补上 Keys，或删掉不再存在的登记。"

const reviewContracts = "\n按概念同名，计划外的纲要与纲要头里有下列「必须保持」条目——改动可能改变它们锁定的行为（同一概念常由别的模块打印、校验或兜底）。逐条说明新实现是否仍满足；不满足就改方案，除非需求明确点名要改变它（那就同时改测试与纲要）；涉及的守护测试改完要运行：\n"

const reviewChecklist = `
逐项核对（对照全局纲要，不只看上面列出的）：
1. 接口与调用方：改了名字、参数、返回值或行为的，所有调用方都在计划里吗？
2. 数据与约定：数据格式、配置项、协议、文件格式的两端是否同改？
3. 纲要头约定：标签字典、Constraints 配额、模块归属守住了吗？
4. 新建与删除：新文件有人引用吗？删掉的文件还有人用吗？
5. 测试与文档：要跟着改的测试、README、规则文档在计划里吗？
6. 目标纲要本身：每行写的是改完后的 Role/Uses/API/Constraints，而不是待核查的事项吗？
7. 守护测试：计划里文件的纲要 Constraints 点名了哪些测试（"必须保持 X（测试名）"）？改完要逐个运行；会改变被测试锁定的行为的，回到需求确认——需求没有明确要求改变，就改实现，不改测试期望值。
8. 测试调用的入口：纲要 API 里测试也调用的函数，改动后签名与可见性还在吗？不得为消除未使用警告给它加 cfg(test)/条件编译或收窄可见性。
9. 执行点：同一个参数或限额会不会被多层同时执行（一层改了，另一层还在兜底，本地测试因此测不出差别）？计划里写明唯一执行点了吗？改了参数流向的文件不要用 = 。
10. 收尾验证：全部改完、提交之前，按仓库自己的测试配置跑一次不收窄的整包测试（不加 -k、不只跑单个文件或子目录、不用 -o addopts 等改掉收集范围），新出现的失败修掉；删掉或改名的名字，确认没有测试还在导入它；按依赖库版本整类跳过的测试临时去掉门控跑一遍，跑不了的记成「本地验证盲区」，不算通过；测试另起进程调用的构建产物，确认是按测试所用的配置从当前源码新构建的。
` + "\n" + dependencyBlockerRule + "\n" + entryContractRule + "\n" + finalSourceRule

// The commit gate (verifyCommit). gateVerifyCore refuses a commit of code
// no broad test run has seen since it last changed; gateVerifyVersionHint
// follows it when no version gate was detected (the generic advice);
// gateVerifyGates (%s the gates never run lifted) follows it when some were,
// gateVerifyGatesOnly refuses a commit for those alone, both then followed
// by gateLiftHowTo; gateVerifyGatesNudge (%s the gates in force) follows a
// refusal for a stale run when every gate was run lifted once.
const gateVerifyCore = "ortg: 自最近一次改代码以来还没跑过整包测试，这次提交先拦下（每个提交点只拦一次，再提交即放行）。提交前按仓库自己的测试配置跑一次不收窄的整包测试——不加 -k/-m，不只跑单个文件或子目录，不用 -o addopts、-c、-p no:doctest 之类改掉收集范围；太慢就加并行，输出写进日志文件，在本回合里等它跑完再看（给足超时，或用阻塞循环等日志里的结束标记）——非交互运行时回合一结束会话就退出，放到后台的测试等不到通知，还可能被一并终止。测试若另起进程调用构建产物（可执行文件、服务、插件），先查测试支持代码按什么变量、配置或目录找它，按那套配置从当前源码重新构建——产物比最近一次改动旧，测出的通过不算数。看新出现的失败：拿不准是不是这次改出来的，就在改动前的提交上对照着跑那几个测试（用 git worktree add 到临时目录，不要 stash 或切走当前改动）；是这次改出来的先修好。" + " " + finalSourceRule
const gateVerifyVersionHint = "测试配置按依赖库版本整类跳过的测试（CI 或别的环境可能不跳过），临时去掉这道版本门控把那一类跑一遍、跑完恢复；改动前就失败、只差第三方库的输出表示（浮点位数、repr、警告文本）的，按实际输出更新期望——这不算改变行为；按平台、网络或可选依赖跳过的不要硬跑，在纲要里记成「本地验证盲区」，改动碰到它们锁定的行为时逐行核对实现。"
const gateVerifyGates = "另外，测试配置里有按依赖版本整类跳过测试的分支，本地还从没在去掉它之后跑过整包：\n%s\n"
const gateVerifyGatesOnly = "ortg: 整包已经跑过，但测试配置里有按依赖版本整类跳过测试的分支，本地还从没在去掉它之后跑过整包，这次提交先拦下（每个提交点只拦一次，再提交即放行）：\n%s\n"
const gateLiftHowTo = "先用 python -c 打印那个依赖的版本，看条件在本环境是否成立。不成立：本地整包本来就在跑这一类，直接再提交即可（ortg 记为已确认）。成立：这一类在本地一条都没跑过，而 CI 或换了测试配置的环境会跑它们——用一条单独的命令把那一行临时改成不成立（如在条件前加 False and），再跑一次同样不收窄的整包，逐条看失败——不能只与改动前对照、只看新增的：这一类改动前就失败的，在 CI 上同样失败。新出现的先修；改动前就失败、只差第三方库输出表示（浮点位数、repr、警告文本）的，按实际输出更新期望——这不算改变行为。收尾：若已把这一类的期望更新到本环境、去掉门控后能全部通过，这道门控已没有必要，保留改动一并提交；否则把那一行恢复原样（需求要求改这道门控的，按需求）。"
const gateVerifyGatesNudge = "测试配置里仍有按依赖版本整类跳过测试的分支，本地整包不跑这一类：\n%s\n这次改动若碰到它们锁定的行为（例如改了带示例输出的文档串），照之前的做法临时去掉门控再跑一次。"

// gateVerifyWhole follows a refusal of a command that also stages: the whole
// command was refused, its git add included. gateVerifyRestage refuses, once,
// the commit that follows at that commit point without staging anything.
const gateVerifyWhole = "注意：被拦的是这一整条命令，其中的 git add 等暂存步骤也没有执行。再提交时把暂存和提交一起重跑（或先单独执行暂存），不要只重发 git commit——否则只有之前已暂存的内容进入提交，其余改动会漏掉。"
const gateVerifyRestage = "ortg: 上一条被拦的提交命令里有暂存步骤（git add 等），它们随整条命令一起没有执行，而这条命令没有暂存，提交会漏掉那些改动。先用 git status 看清哪些改动还没暂存，把要提交的暂存上（或把暂存与提交写在同一条命令里）再提交。"

// Version gates: branches of the test configuration that skip a whole
// category of tests by a dependency's version, found at run time; the facts
// (file:line, condition, reason) are filled in from the repository, never
// written here. versionGateLine (%s file, %d line, %s condition, %s category,
// %s versionGateReason or "") renders one; versionGateLifted marks one
// lifted in the work tree; versionGateMore (%d) stands for the rest.
// versionGateOverview goes out with the outline header; allSkippedNote (%d
// skipped) and allSkippedGates follow a test run that skipped everything it
// selected; gatedFailuresNote (%d failed or erred) follows a test run with a
// gate lifted whose summary has failures.
const versionGateLine = "- %s:%d `%s` → 条件成立时整类跳过%s%s"
const versionGateReason = "（原因：%s）"
const versionGateLifted = "（工作树里这一行已改掉）"
const versionGateMore = "- 另有 %d 处\n"
const gateCatDoctest = " doctest"
const gateCatHook = "该钩子标记的测试"
const gateCatIgnored = "收集时忽略的文件"
const versionGateOverview = "ortg: 本地验证盲区（自动检出）——测试配置里按依赖版本整类跳过测试的分支如下。条件在本环境成立时，这一类在本地整包里一条都不跑，跑出来的\"全部通过\"不包括它们。改动碰到它们锁定的行为时，用一条单独的命令把那一行临时改成不成立再跑，跑完恢复（期望已更新到本环境、去掉门控后能全部通过的，可以保留改动）；提交前 ortg 会检查每道门控至少这样跑过一次：\n%s"
const allSkippedNote = "ortg: 这次测试运行选中的用例全部被跳过（%d skipped），没有一条真正执行，不等于通过。用 -rs 查看跳过原因。"
const gatedFailuresNote = "ortg: 这次运行去掉了按依赖版本整类跳过测试的门控，汇总里有 %d 个失败或错误。这一类在 CI（门控条件不成立的环境）里照常运行，改动前就失败的也会在那里失败——只与改动前对照、只修新增的，查不出它们。逐条看原因：只差第三方库输出表示（浮点位数、repr、警告文本）的，按这里的实际输出更新期望，或写成新旧输出都接受的形式；需求点名的照修；确属本地环境问题（网络、缺数据文件、缺可选依赖）的不动，在回复里说明。修完再去掉门控跑一次，直到只剩环境问题。"

// stopBgRunNote (%s the test run's command) blocks a Stop while a broad test
// run left in the background is still going and code changes are
// uncommitted.
const stopBgRunNote = "ortg: 你放到后台或脱离会话启动的整包测试（%s）还在运行，代码改动也还没提交。非交互运行时回合一结束会话就退出，等候的通知不会到来，测试还可能被一并终止。在本回合里用前台阻塞的方式等它跑完（例如循环检查它的日志里是否出现结束标记，给足超时），读完结果、处理好失败，再按任务要求提交或收尾。若这是交互会话、确实要先把控制交还给用户，说明测试仍在后台运行即可。"

// depBumpNote (%s the moved versions, "manifest：name old → new", with
// depChangeMore %d for the rest) follows an edit that moved an existing
// dependency's version.
const depBumpNote = "ortg: 这次改动改了已有依赖的版本——%s。需求点名要升级的，照需求做；新增依赖时锁文件里随之变动的间接依赖不用管；这些变化若不是你这次做的（用户或之前留下的未提交改动），不要动。其余没有点名的，不要顺手升：升级改变的是整个工作区的依赖解析，影响面远超这次需求；构建、测试与发布环境也可能仍按原来的清单和锁文件解析，那样用到新版本接口的代码直接编译不过。先把版本改回去，在现有版本里实现：新版本才有的接口，找现有版本里的替代写法，或在本仓库补一小段实现；现有版本确实做不到的那一小部分，在回复里写明缺什么、需要哪个版本，交给需求方决定，不要自行升级。"
const depChangeMore = "另有 %d 处"
const allSkippedGates = "测试配置里有按依赖版本整类跳过测试的分支，跳过原因若是它们，用一条单独的命令把那一行临时改成不成立再跑：\n%s"
const reviewAgain = "\n有要补、要改的，用 ortg_target 改完再调用 ortg_review；核对没发现问题，也再调用一次 ortg_review 确认——连续两次核对看到的计划一样，才解锁这些文件开始改代码。"
const reviewConverged = "\n计划与上次核对一致，已收敛：以上 %d 个文件解锁，可以开始改代码（改完每个文件照常用 ortg_update 提交纲要）。计划之外再动多个文件仍会被拦，要改就补目标纲要再核对。"

// impactNote follows every post-edit reminder: %s the file, %s its candidate
// dependents (from R and the CBM call graph) or "无". Neither source sees
// semantic ties — a shared concept, config key or data format — so the model
// is also sent to the global outline. It asks for a judgement per file, not a
// blanket rewrite.
const impactNote = "ortg: 判断这次改动的影响面。候选依赖方（Uses=全局纲要里 Uses 写到 %s，调用=CBM 调用图里调用它）：%s。再通读全局纲要做语义判断：Role/API/Constraints 里涉及同一概念、配置项、数据格式、协议或约定的文件，即使不在候选里也可能受影响（相关模块不在上下文就先用 ortg_overview 取）。逐个判断——接口或约定变了、代码要跟着改的，改代码并提交它的新纲要；代码不用改但纲要描述已不准的（如 Uses、API、Constraints 里提到的名字或行为变了），只重写那行纲要用 ortg_update 提交；不受影响的不动。改动涉及的文件纲要里点名的守护测试（「必须保持 X（测试名）」）要运行；它们失败时先回到需求确认，需求没有明确要求改变那个行为，就改实现，不改测试期望值。直接做，不询问。" + " " + dependencyBlockerRule + " " + entryContractRule

// dirRowMismatch refuses an outline whose trailing "/" disagrees with the
// path: a folder outline names a directory ("vendored/[标签]: ..."), a file's
// outline names a file.
const dirRowMismatch = "%s：目录要写目录纲要（文件名以 / 结尾，如 vendored/[标签]: Role:.. | Uses:.. | API:.. | Constraints:..），普通文件的纲要文件名不带 /"
