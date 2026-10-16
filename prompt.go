package main

import "strconv"

// All model-facing texts live here and only here.

const semanticInitRule = "语义初始化：阅读源码正文（长文件分段读完）及可见的相关测试，AST、导入和符号清单只辅助定位，不能代替正文阅读与语义概括。Role 写实际职责；Uses 按职责与契约的重要性选择强依赖，不按导入顺序截取前五项；API/Constraints 只写有依据的契约与约束，无内容不伪填，不把未提供的固定文案猜写成已知约束。文档或可见测试规定的契约与当前源码实现须分清；已确认的占位、缺失定义或名称绑定缺口及其实际消费者须如实写明，未核实的实现状态不得描述为已实现。条目齐全不等于语义质量已核验。"
const taskOutlineRule = "任务纲要生成：普通任务先显式用 ortg_header mode=task。先读原任务和可见测试，列出相关仓库相对路径及关联理由，再逐份读正文，仅生成或修订任务和测试有关文件的纲要。沿合法公共对象的构造或获取、目标调用、输出及实际依赖，将必要 helper 和消费者动态纳入。已有有据且对齐的纲要复用，不为凑全库覆盖补无关文件；未相关裸行保持未知，不宣称全库 ready。可见测试缺失时，依据公开接口和可见调用方形成候选并标未验证，不用不可见资料、gold 或历史失败答案推范围。相关名单只限定纲要生成，不缩小任务、源码、角色或测试评测范围；保留原库存与已有真实纲要，不通过改忽略/观察角色隐藏无关文件。只有用户明确要求全库初始化才用 mode=build。"
const stagedInitRule = "仅用户明确要求全库初始化(mode=build)时，首次初始化顺序：核心文档与源码入口 → 初始纲要头 → 补齐全部文档纲要 → 根据完整文档理解修订纲要头和受影响的已有纲要 → 优先建立核心代码纲要（入口、基础模块与关键执行链） → 根据核心代码理解再次修订头与受影响纲要 → 建立普通文件纲要 → 通读全库纲要，最后修订头及仍需更新的文件纲要。每轮检查职责、接口、约束和关系描述是否随新增理解变化；头用 ortg_header、文件纲要用 ortg_update 更新，不只更新头。所有未覆盖文件继续保留待建纲要状态。"
const outlineModeRule = "维护模式：普通任务显式用 ortg_header mode=task；只有用户明确要求全库初始化才用 mode=build，按全库初始化阶段推进。模式持久化，不根据自然语言自动猜测；未传 mode 保持当前配置，传空串恢复旧兼容行为。task 仍须有依据的纲要头，仍按完整字段、路径、Uses/API/Constraints/Keys和目标复审校验；只改变纲要生成范围，不改变文件库存、源码、角色或测试评测范围，不以模式切换、伪填纲要或 ortg_align 声称全库已初始化。" + " " + taskOutlineRule
const taskModeNote = "ortg: 当前为任务维护模式(task)。无需先补齐全部文档；未覆盖文件继续保留裸行与未知状态，实时对账仍报告无纲要，不代表全库初始化完成。" + " " + taskOutlineRule
const headerTaskFirstNote = "ortg: 当前为任务维护模式(task)，纲要头仍是模板。先按原任务和可见测试列相关路径及理由，读相关文档与合法源码入口，用 ortg_header 写有依据的初始头，再提交相关已读源码纲要与目标计划；不要求先补齐全部文档。" + " " + taskOutlineRule + " " + semanticInitRule
const headerBadMode = "mode 只接受 build、task 或空串（恢复旧兼容行为），配置未写入"
const outlineConflictRule = "纲要与需求、源码、测试或实际运行结果冲突时，读取对应源码与测试核实，并修正不准确的纲要；状态对齐不证明代码正确或语义完整。"
const dependencyBlockerRule = "任务依赖闭环：发现缺失符号、导入或依赖缺口时，沿公开合法构造或获取→目标调用→输出的最短必要链，结合可见测试判断是否阻断任务；有依据的阻断纳入计划，不能仅因未点名或早已缺失就排除。“不要求完整子系统”不等于“禁止必要 helper”；显式禁止须遵守，不凭假想外围流程扩展恢复范围，确与任务无关的按证据说明。恢复或迁移缺失符号后，全库搜索实际使用点（含函数对象传递/注册，不能只搜调用括号），逐个消费者核对导入、名称绑定与调用契约；不能只补定义或一个分支，遗漏消费者也纳入计划验证。"
const finalSourceRule = "最终验证：为绕过仓库缺失实现而临时注入或替换的函数、对象只作诊断。提交或结束任务前，移除这类替代，在新进程中按最终持久源码运行验收；仅替代后的自测通过不算最终源码通过。"
const entryContractRule = "多入口契约：同一行为由 setter、预检、包装或快捷分支处理时，按入口与触发条件核对可见契约；提前校验或迁移后，异常类型、已明确给出的词句与输出格式仍须满足各入口约定，并分别覆盖各入口，不只验证底层函数。本次改动涉及公开兼容、弃用、异常或告警契约且细节未明时，先读任务相关项目政策与可见消费者，区分强制规定、适用示例及相邻惯例并记录依据和适用边界；不凭习惯猜类型或文案，不将示例升为强制规定。公开包装器或适配层与内部回调的签名、参数可省略性和默认值须分别按可见文档、真实调用方及相关测试核对，不直接照抄内部必填签名；未核实的实现假设不得写成已确认契约或「必须保持」。工厂或装饰器返回可调用对象时，将其公开签名契约与工厂/包装器自身分别核对并记录，依公开任务核对位置参数槽、变参、仅关键字参数、默认值和可省略性；展示用 接口元数据或调用预检 不得相对公开契约额外收窄合法调用，内容/输出选项不隐含改变签名。用公开实际调用形状验收现有调用方及省略可选参数的路径，显式空值不能替代省略验证；未执行的调用形状保留未验证。公开契约明确值来源、对象字段或语义单位时，在 API/Constraints 和目标计划中保留这些约定；沿公开合法构造或获取→实际调用实参→输出/消费的最短传值路径，逐个核对相关调用点的值来源及转换，区分原始值与变换后值。本次改动涉及值表示时，在 API/Constraints 和目标计划中保留来源、允许转换与公开契约决定的比较标准（如语义等价或原始字节/文本一致）；公开契约要求底层、原始或线格式表示时，不能默认以解码后再编码替代，即使业务值相等。缺失与存在但内容为 null、空字符串、0 或 false 是否等价须按已有公开契约核对，未核实等价前不以真值判断合并；自测沿已有合法公开构造或获取链使用真实类型，按该比较标准断言契约独立确定的期望，不以待验证的同一转换链生成期望，替身通过不代替真实入口验证。函数名、类型或调用边存在不证明传值符合契约；用实际调用方路径断言契约结果，不能只测 helper。未核实的来源或契约保持未知，不从参数名或不可见资料猜填，不扩为无关调用点或全库审计。本次改动涉及跨文件或后端传值时，按可见依据核对实参与载荷两侧的单位、表示（如 dtype/端序）、允许范围及相关元数据，将转换映射记入 API/Constraints 或共享契约，并以实际生产值断言消费者结果；名称或语言类型相同不证明表示相同。本次改动涉及影响输出或跨层分支的控制选项时，用能区分公开行为的合法载荷经实际入口断言最终结果；空或默认载荷使分支等价时，不算验证选项效果。调用路径经过适配器或中间层时，将相关默认重试、缓存及异常转换的行为和信号归属纳入 API/Constraints 和目标计划；沿公开生产者→适配/中间层→实际调用方→可观察消费者的任务相关路径，依据可见配置、依赖实现或测试核对实际生效的默认行为，确认状态、返回值或异常由哪层处理、转换或消耗，以及公开契约要求的队列、回调或日志等消费者能观察到什么。类型与调用边一致不能替代层间行为核验；经实际入口断言消费者按公开契约可观察的结果，不能只验证底层结果。未核实的默认行为或信号归属保持未知，不凭依赖名称猜测，不扩为无关层或全库审计。公开接口对同一参数规定多种值形态或重载时，在 API/Constraints 和目标计划中保留适用上下文、判别依据、整体/逐项语义及返回形态，不能仅凭容器类型一律拆分。对本次改动涉及的适用形态经真实构造与调用，断言公开规定的返回结果或异常；未验证形态保持未知。只打印诊断且命令退出 0 不算语义验收，公开反例须转为实际断言后复验；不扩为无关形态、全组合或全库测试。按任务实际使用的后端与通信入口运行验收；涉及 HTTP、SQLite 等不同实现时分别检查，另一后端或分支通过不能替代任务实际入口的验证。需求明确覆盖多个注册、插件或表示实现时，依据可见注册表、文档及调用方列出任务所需且受支持的集合，将各实现的合法调用和适用的转换往返验证逐项对照该集合；子集通过不代表全集，未执行项保留未验证，不扩为无关插件或全库测试。公开契约涉及可选、尽力或非致命的附带步骤时，明确主操作结果、附带失败及恢复边界；经本次涉及的全部公共入口验证，已明确可恢复的附带失败不改变已成功主操作的约定结果，并保留取消、退出等控制流语义；未核实的边界保持未知，不将可选直接等同于失败可忽略。"

const rulesBlock = `## ORTG 纲要规则
本仓库由 ORTG 维护一份纲要：相关源码文件一行 "文件名[标签]: Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束"，模型复用已有真实纲要并按任务维护相关条目，未相关裸行保持未知。
三条硬规则：
1. 开工先调用 ortg_overview（不带参数）取纲要：整份不超过整份预算（估算的 token 不超过上下文窗口的一半，大小不超过约 48 万（单次截断值 50 万留出余量，Claude 按字计、Codex 按 token 计））就整份返回；超过时只返回纲要头与模块清单，再按问题用 module 参数取相关模块的纲要（可多次；module="*" 取全部），再决定下一步。发过的条目一直在上下文里，不再重发（再取只补还没有或已变动的，全都有就只回全库计数摘要，完整路径按需用 ortg_status 取；上下文压缩或清空后会自动重发）——不要和 Grep/Read/Glob 并行发出。纲要就是本仓库的认知：能从纲要回答的问题直接回答，不重复读源码；只有纲要没写的具体事实（某个值、某段实现、某个行号），或遇到需求、测试及运行结果冲突时，才读对应文件核查并修正纲要。
2. 普通任务中，对任务或可见测试相关的无纲要/过期文件，读完正文按纪律生成或修订一行纲要，调用 ortg_update 提交；筛选判为无关的文件不补纲要；用户明确全库初始化时，全部范围内文件都相关，按全库阶段维护。
3. 改代码：只改一个文件时直接改。要改多个文件（含新建、删除）时先定计划、核对、再动手：用 ortg_target 的 items 一次列出这次要改、要新建、要删的每个文件的目标纲要，调用 ortg_review 按全局纲要逐项核对：核对列出要补、要核实的地方时，补改后再核对，直到连续两次核对计划不变；计划全写 =（只改实现）或核对没发现要补的地方，一次核对即收敛——收敛前改第二个文件会被拦；这是自查，不用给用户看、不用等同意，核对完直接动手。改完一个文件，立即为它提交新纲要（ortg_update）——这是改动本身的一部分：不征求同意、不留到最后、不当成待办报告给用户；纲要没提交，这个文件就不算改完。所有字段重写完整，不只写增量。再判断这次改动影响到哪些文件（Uses 依赖它的、调用图里调用它的、全局纲要里语义相关的）：要跟着改代码的就改，纲要描述随之不准的就重写它们的纲要提交；纲要头里的全局描述（依赖层次、模块划分、系统约定）随之不准的，用 ortg_header 改。
状态规则：纲要是否对齐以 ortg_status 的实时对账为准；被问到"对齐了吗 / 有哪些文件要处理"时重新扫描再答，不引用开工时的报告——那只是快照，之后文件可能已经变了。
骨架规则：纲要中只有裸文件名没有纲要的行，是尚未建立认知的文件清单；普通任务按任务和可见测试确定其相关后，读取正文并生成一行纲要、调用 ortg_update 提交，不为补齐裸行清单扫描全库；用户明确全库初始化时，全部范围内文件都相关，按全库阶段维护。
目录纲要规则：第三方库等不逐文件写纲要的目录，给目录本身写一行纲要（文件名以 / 结尾，如 vendored/[标签]: ...），目录放进 observe_dirs。它的内容往往自成一体，但不要为它单开一个模块：B 标签填用到它的那个模块，它就并入那个模块、随那个模块一起发送。
目标规则：条目下一行 "#目标:" 是该文件的目标纲要——这个文件改完之后应有的纲要，表示要改代码去达到它；"#目标:删除" 表示计划删除；"#计划新建" 表示文件尚不存在。改完后用 ortg_update 提交，目标纲要自动清空。只改实现、改完纲要不变的文件（重构、格式化、修 lint），ortg_target 的 outline 传 "=" 即可，不必把整行纲要再抄一遍，纲要里显示为 "#目标:同现在"。目标纲要只写要改代码去达成的状态：待核查、待实测的结论和疑问不是目标，也不写进当前纲要——先核查，再把事实写进当前纲要。
高屋建瓴模式：只迭代目标纲要、纲要头、忽略规则与目录描述，不改代码，用 ortg_target 与 ortg_header 提交。` + "\n" + outlineModeRule + "\n" + semanticInitRule + "\n" + outlineConflictRule + "\n" + dependencyBlockerRule + "\n" + finalSourceRule

// codexBudgetRule is injected only under the Codex host: it wraps MCP calls in
// functions.exec and cuts the middle out of a tool result that overruns the
// budget, keeping head and tail, without any error. The pragma (equal to
// codexLimits.max) raises the exec budget for the one call; what history keeps
// is capped by tool_output_token_limit in config.toml. A reply arrived whole
// when its last line is the closing line every ortg_overview reply ends with
// (all start with "以上") and no "tokens truncated" marker sits in between.
const codexBudgetRule = `Codex 宿主补充：本宿主用 functions.exec 包装 MCP 调用，工具结果超过预算会被从中间截掉、只留首尾——不报错，被截掉的文件你根本不知道存在。故调用 ortg_overview 时，外层命令第一行必须是 // @exec: {"max_output_tokens": 500000}；它只抬高这一次工具结果的预算，不是模型输出上限，也不改纲要内容。ortg_overview 的每种回复（模块或文件的纲要、模块清单、短回复）最后一行都是以"以上"开头的收尾句，正文里也不会有 "tokens truncated" 字样；最后一行不是这样，或中间出现 "…N tokens truncated…"，就是被截断了：照此重发一次并传 force=true（本会话已计为发过，不传只会拿到短回复）。重发仍被截断，多半是 ~/.codex/config.toml 没设 tool_output_token_limit = 500000（不设时 Codex 存进对话的工具结果只留约 1.2 万 token，@exec 抬不过它）或 [mcp_servers.ortg] 没设 env = { ORTG_AGENT = "codex" }：告诉用户按 ortg 的 README 补上后重启 Codex，在此之前按模块分次取。自定义 CODEX_HOME 要在 MCP 配置设 env_vars = ["CODEX_HOME"]；Linux 未转交时 ORTG 只沿当前进程祖先链找该变量，再回退 ~/.codex。不要拿半份纲要开工，也不要用 Grep/Read 去补缺失的部分。`

// The ortg_overview replies. Each ends with its own closing line that starts
// with "以上" — codexBudgetRule tells Codex that a reply whose last line does
// not is truncated. overviewPartFooter, closing the outline entries, restates
// rule 1 at the point where the model decides whether to open files.
//
// overviewAlreadyRead: every requested entry is already in the context; %s
// is the scope (整份纲要 / 模块 X), %d the entry count.
const overviewAlreadyRead = "ortg: 你要的%s共 %d 条纲要本会话都已发给你，仍在你的上下文里，直接用它们，不要再取。下面只给实时全库计数摘要（这是短回复，不是被截断）。上下文压缩或清空后会自动重发；若你确实看不到这些条目，调用 ortg_overview 并传 force=true。"
const overviewShortFooter = "以上是实时全库计数摘要，纲要条目本身仍在你的上下文里。"

// sessionReportNote follows the alignment report in the session-start
// context; sessionFetchNote replaces everything when reconciliation timed out.
// A resumed context keeps its outline. Known current discipline needs only
// the concise note; changed or unknown discipline is delivered with its own note.
const resumeNote = "ortg: 会话已恢复；本上下文的规则分页及完成确认已记录，已取纲要与许可继续沿用，不必重复取送；文件有变动时 hook 会提示。"

const resumeRulesNote = "ortg: 会话已恢复；已取纲要与许可继续沿用，当前规则分页尚未完成确认。"

const rulesNeeded = "ortg: 当前规则尚未完成 MCP 分页确认。先单独调用 ortg_overview rules=\"\"，逐段核对收尾后按返回的 rules cursor 续取，末段后再回传完成 cursor；不要并行取页。只有回到“规则分页已确认”才继续写入或提交最终声明；这不证明语义理解或宿主完整消费。普通纲要调用不变。"
const rulesNoSession = "ortg: 本次调用没有可靠的上下文标识，不能记录规则分页；请通过宿主的 MCP 会话调用。"
const rulesBadCursor = "ortg: 规则 cursor 不属于本上下文的当前连续页链，或内容/上下文已重置；用 ortg_overview rules=\"\" force=true 从首段重新获取，不沿用旧 cursor。"
const rulesMixedArgs = "ortg: rules 规则分页不能与 module 或 init 同传；空串取首段，省略 rules 才是普通纲要调用。"
const rulesBadType = "ortg: rules 必须是字符串；空串取首段，null 不代表空串或省略。"
const rulesArgumentDescription = "规则分页 cursor；省略为普通纲要，空串取首段，核对完整标题/收尾后逐段回传返回的 cursor，末段后的回传确认完成；缺文以空串和force=true重取。每页最多4096 UTF-8字节，不与module/init同传。"
const rulesPageOverflow = "ortg: 规则页超过固定 UTF-8 预算，本段未记录；请报告此错误。"
const rulesPageHead = "ORTG %s · 规则第 %d/%d 段\n---规则正文---\n"
const rulesPageEnd = "\n---规则正文结束---\n以上是规则第 %d 段。确认本段从标题到此收尾完整且没有截断提示，再单独调用 ortg_overview rules=%q；末段也须回传 cursor 才确认完成。若缺文，用 rules=\"\" force=true 重取。\n"
const rulesComplete = "ortg: 规则分页已确认：原生 MCP 已按序输出全部当前规则段，并收到完成 cursor；不证明宿主完整消费、语义理解或任务通过。已有纲要与许可继续沿用；尚未取纲要时再普通调用 ortg_overview。\n以上是规则分页确认。"

const sessionReportNote = "第一次回复用户时，先用一句话报告上面的对账结果（对齐/过期/无纲要各多少），再谈正事。"
const sessionFetchNote = "开工前先调用 ortg_overview（不带参数）取纲要（超过整份预算时先给纲要头与模块清单，再按问题取相关模块）。"
const sessionCountsReport = "## 对账报告\n%s · 已清除 %d\n此处只列全库计数；完整路径清单用 ortg_status 按需查看。"

// A path index, not document contents or evidence of a behavioral contract.
const policyCandidatesHead = "\nortg: 项目政策候选（按标准路径名从活跃认知库存发现，未读取正文）。仅在与原任务相关时读正文，核实规范、示例及适用边界并写事实纲要；候选或已有摘要不证明行为契约，不扩大任务，也不要求补齐全库文档：\n"
const policyCandidateLine = "- %s（%s）\n"
const policyCandidateKnown = "已有纲要（摘要）"
const policyCandidateUnknown = "无纲要（未知）"
const policyCandidatesCount = "本次列出 %d 条，另 %d 条候选因条数或字节上限未列；路径完整引用、不截断。\n"

const taskDependenciesHead = "ortg: 下面是本次已交付条目的直接 Uses 中待核验的本地依赖（只看一跳，不递归或展开目录）。相关者须读公开源码及可见测试，补缺或修订真实纲要；与任务无关者按证据说明。没有纲要或纲要过期表示认知未确认，不表示源码必错；这是线索，不是全库初始化门禁或正式修复范围限制：\n"
const taskDependencyLine = "- %s → %s（%s）\n"
const taskDependenciesMore = "另有 %d 条尚未提示的一跳待核验依赖本次未展开；再取本范围可续取，也可按相关模块缩小范围核验，不要求补齐全库。\n"

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

const skeletonRule = "ortg: 该文件尚无纲要。普通任务先确认与任务或可见测试的关系，相关文件读完正文后生成纲要并调用 ortg_update，判为无关则保留裸行未知；用户明确全库初始化时，全部范围内文件都相关，按全库阶段维护。"

// The S budgets are the one text here not written by hand: quotaLine renders
// logic.go's sQuota, the same table checkOrError enforces, so the discipline and
// the header template cannot drift from the checker.
var updateDiscipline = `纲要行纪律：单行；格式 文件名[标签]: Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束；标签 [C A B D… E] 是重要度数字在前、其后层级、模块、特征(0到多个)、规模，以空格分隔，每个都是纲要头字典里的大写字母(模块可两个)；Role 一句话写这个文件的职责，测试文件只写一句测什么，不逐项罗列，文档文件一句话说它讲什么；跨模块、不靠调用关系的约定不写进 Uses：在纲要头登记 #【契约】键：正文，本文件参与就在行末加 | Keys:键(角色),…，键须已登记(未登记会被拒)；Uses 只列本文件依赖的跨文件强依赖(≤5)，一律写仓库相对路径：文件写全路径(如 lib/storage/cache.c)，整个目录或包写目录路径并以 / 结尾(如 lib/storage/)，可加括注写用到什么，包名、模块名、裸文件名都不行，第三方库与标准库不写；API 只列跨包或对外的契约(命令、工具名、给别的包用的导出名，以及测试作为外部消费者调用的公开入口——先按实际生产调用、外部消费者和项目测试结构核实用途；真实公开入口写"测试也调用"并保持签名与可见性，仅供测试的辅助代码可按项目约定条件编译，不因被测试调用就列为公开 API)，包内标识符不列，无则写 -；同一个参数或限额由多层处理时，在接线文件的纲要或【契约】正文里写明各层职责、参数转换和相互作用；是否单点执行按任务契约决定，避免重复扣减或错误提前终止；给文件新增的对外接口(Rust pub 项、Go 导出名、JS/TS export)必须写进 API；Constraints 只写不读代码就会做错的约束、不变量与陷阱，不复述 Role 与 API，代码里一眼可见的实现细节不写，按 C 配额 ` + quotaLine() + `(「必须保持」子句另计，不超过该档配额的一半、至少 100 字，不占其余约束的字数)；测试契约：本文件的单元测试或别处的集成测试锁定了的对外行为（输出格式、参数组合的结果、边界值、错误语义），写成"必须保持 X（测试名）"，写清期望结果，不写成"可能/现状是 X"——这是改动时不许改变的东西；这种约束另有模块依赖时，在依赖方的纲要里也写上；测试配置按依赖版本或平台整类跳过、本地跑不到的测试（CI 或别的环境可能不跳过），在被测文件的 Constraints 里写成「本地验证盲区：X（跳过条件）」——它们锁定的行为本地无法验证，改动碰到时要逐行核对实现，不能当作已通过；文档文件的 Constraints 写它规定的约定、约束与设计决定，按 ` + strconv.Itoa(docQuota) + ` 字配额；禁时间维度；所有字段重写完整。` + " " + semanticInitRule + " " + entryContractRule + " " + taskOutlineRule

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
#骨架规则：只有裸文件名没有纲要的行是尚未建立认知的文件；普通任务仅对任务/可见测试相关文件读正文后生成纲要并调用 ortg_update，无关裸行保持未知；用户明确全库初始化时全部范围内文件均相关，按全库阶段维护
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
	"ortg_overview": "可选 rules 字符串走独立规则分页：省略 rules 是普通纲要，rules=空串取首段，后续逐段回传返回的 cursor，末段后的 cursor 确认整链；每段最多4096 UTF-8字节。须看到本段完整标题/收尾且没有截断提示才续取；缺文用 rules=空串 force=true 重来。分页不与 module/init 混用，不改变纲要已发或写入许可；旧hook回执不算完成。对账并返回纲要与全库计数摘要，完整路径清单用 ortg_status 按需查看。不带参数时整份不超过整份预算（估算的 token 不超过上下文窗口的一半，大小不超过约 48 万（单次截断值 50 万留出余量，Claude 按字计、Codex 按 token 计））就整份返回，超过时只返回纲要头与模块清单（不含条目正文）；再用 module（B 标签）取模块，module=\"*\" 取全部。同一会话只发上下文里还没有或已变动的条目，全都发过就只回计数摘要；上下文压缩或清空后自动重发；确实看不到时传 force=true 重发。一次超过单次上限时照发正文（可能被截断），开头提醒把模块拆细。本仓库启用了 ORTG（会话开始注入过 ORTG 纲要规则）时开工先调用。仓库没有 ortg.tsv 就是没启用 ORTG：不要调用 ortg 工具；只有用户明确要求创建（初始化）或对齐纲要时，才传 init=true 建表（根目录有 overview.txt 则先备份再整份导入，否则建空表；再首扫）。" + " " + taskOutlineRule,
	"ortg_status":   "重新扫描并对账，只返回对账报告（无纲要/过期/观察变动/对齐各多少及路径），不返回纲要正文。被问到纲要是否对齐、有哪些文件要处理时用它——会话开始的报告只是当时的快照，不要引用。",
	"ortg_update":   "提交当前文件纲要：用 path+outline 提交一个文件，或用 items 批量提交（每项为\"路径 完整纲要行\"，路径和文件名可含空格及括号，按纲要文件名与标签识别边界）。给了 items 就忽略 path/outline；空批、规范化后重复路径均拒绝。全批共用一个事务，任一项失败全部回滚并指出路径与原因；成功逐项报告，统一保存与刷新。每项仍校验格式、字典、Uses、Keys、API、角色与目录类型，前移指纹并清空目标纲要。全库初始化(build或旧兼容首次建设)按批前文档状态检查，不能同批补文档并首次提交源码；任务维护(task)只维护当前已读文件，未覆盖仍未知。全库建设成功后按批次最终状态提示阶段修订。批量只合并已阅读文件的完整当前纲要，不代替正文阅读与每文件改完立即提交；全库初始化仍按文档→修订→核心→修订→普通→最终修订流程。第三方库等不逐文件建纲要的目录，给目录本身写一行目录纲要：path 填目录，文件名以 / 结尾（vendored/[标签]: ...），B 标签填用到它的模块，它就随那个模块一起发送；目录本身放进 observe_dirs，其下文件就不再要求逐个写纲要。",
	"ortg_review":   "可选 final=true：读取本上下文实写后的当前源码代际；用 generation 加非空 report 数组提交本代际审查声明（有效 UTF-8、合计至多4096字节）。这是模型声明，不是原生语义验收；源码或新识别失败使旧声明失效。" + " " + finalReviewContractGuide + " 默认参数仍核对改动计划（参数 rulings：对上一次核对给了正文的每条契约写一行「契约键：裁决」，缺了不收敛）：列出所有目标纲要（要改、新建、删除的文件）的现在→目标字段变化、不合格的目标行、Uses 依赖它们却还没有目标纲要的文件，并给出逐项核对清单。要改多个文件时，写完计划后调用；核对列出要补、要核实的地方时，补改后连续两次看到同一计划才解锁，计划全写 = 或核对没发现要补的地方时一次即解锁。只改一个文件不需要。只给自己核对用，不用给用户看。",
	"ortg_target":   "写目标纲要（这个文件改完之后应有的纲要，用来指导接下来改代码；待核查、待实测的事项不是目标，不要写进来）：给 path 与 outline 写单个文件的目标纲要，delete=true 标记计划删除；或传 document 整份导入（高屋建瓴模式）。允许尚不存在的路径。改完代码后用 ortg_update 提交，目标纲要自动清空。只改实现、改完纲要不变时 outline 传 \"=\"（等于现在的纲要，不必抄整行）。一次改多个文件时用 items 一次传全部：每项 \"路径 目标纲要行\"，纲要行写 = 表示纲要不变，写 删除 表示计划删除。路径和文件名可含空格及括号，完整行按纲要文件名与标签识别边界，= 和 删除 从末尾识别。只改一个文件不需要写目标纲要。",
	"ortg_align":    "人工已核对纲要对齐时调用：mode=all 前移全部文件指纹，mode=paths 只前移 paths。不改纲要文本。已确认的内容改动须提交新纲要，不能用对齐替代。",
	"ortg_header":   "整块替换纲要头（必须含 #A/#B/#E 字典行），并可设置忽略目录、忽略文件、白名单、观察目录与目录标题（keep_files 压过忽略规则；dirs 为 \"目录=标题\" 列表，只作纲要里目录段的标题）。传了的列表整体替换，不传的字段保持原值。可选 mode=build 表示全库初始化，文档未齐时拒绝源码纲要；mode=task 表示部分纲要下的任务维护，只解除全部文档前置，其他校验与复审保留，未覆盖仍未知；空串恢复旧兼容行为，未传保持当前模式。模式持久化，不自动猜用户意图。" + " " + taskOutlineRule,
}

const updateItemsDescription = "一次提交多个当前纲要：每项为\"路径 完整纲要行\"，路径和文件名可含空格及括号；给了 items 就忽略 path/outline，空批与重复路径拒绝，任一项失败全部回滚"
const updateNeedArgs = "需要 path 和 outline，或用 items 批量提交完整当前纲要"
const updateEmptyItems = "items 为空，本批全部未提交"
const updateBadItem = "%q 不是\"路径 完整纲要行\"，本批全部未提交"
const updateDuplicate = "%s：重复路径，本批全部未提交"
const updateFailed = "%s：%v；本批全部未提交"
const updateBatchFailed = "提交 %s 失败：%v；本批全部未提交。若最终落盘失败，.bak 可能已更新为批前完整表；当前表与目标字段未提交，会话缓存不变。"
const updateSubmitted = "已提交 %s 的纲要，指纹已前移，状态：对齐"

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
// A fresh table first needs a grounded project header, not every document's
// outline. Unfinished documents retain their cognitive role and remain pending.
const headerFirstNote = "ortg: 纲要头还没写（【部署】【系统】仍是模板占位）。普通任务先用 ortg_header mode=task，依据任务与可见测试列相关路径及理由，读相关核心文档和合法源码入口，写有依据的初始头并登记 #B模块。只在用户明确全库初始化时补齐全部文档，再按全库阶段修订；不知道的事实先核查，不猜填。还没纲要的文档：%s；普通任务不要求补齐无关文档。相关文档 Constraints 按 %d 字配额。文档与代码不一致时按代码写并记录差异。" + " " + taskOutlineRule + " " + stagedInitRule + " " + semanticInitRule
const headerFromDocsNote = "ortg: 文档纲要已经齐了，纲要头仍是模板。普通任务先读任务与可见测试、列相关路径及理由，再依据相关文档与合法入口用 ortg_header mode=task 写有据头；用户明确全库初始化时，依据完整文档与目录结构写头并按全库阶段推进。写好头后才收源码与目标纲要；文档与源码不一致时按源码写。" + " " + taskOutlineRule + " " + stagedInitRule
const headerNoDocsNote = "ortg: 纲要头仍是模板，仓库没有文档。普通任务先读任务和可见测试、列相关路径及理由，再读相关合法入口写有据头并用 mode=task；用户明确全库初始化时，读入口与目录结构写头并按全库阶段推进，无文档则跳过文档阶段。写好头后才收源码与目标纲要。" + " " + taskOutlineRule + " " + stagedInitRule
const headerDocsPendingNote = "ortg: 初始纲要头已写。普通任务先显式用 ortg_header mode=task，只维护任务和可见测试相关文件；用户明确要求全库初始化时才补齐以下文档的纲要：%s，随后按全库阶段修订。无关文档继续保留裸行未知，初始头写好不表示全库初始化完成。" + " " + taskOutlineRule + " " + stagedInitRule
const initDocsCompleteNote = "ortg: 文档纲要阶段已完成。根据完整文档的最新理解，用 ortg_header 修订纲要头，并用 ortg_update 更新受影响的已有纲要；随后先读核心代码正文及相关测试、建立核心代码纲要，再次修订头与受影响纲要，最后处理普通文件。"
const initCompleteNote = "ortg: 所有认知档文件已有纲要，进入初始化最后一轮修订。通读全库纲要，核对职责、接口、约束、关系与整体架构是否一致；用 ortg_header 更新纲要头，用 ortg_update 更新所有描述已不准确的文件纲要。条目齐全不等于语义完整，不以状态对齐替代这轮核查。"

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

// noTableReply answers the ortg MCP tools in a repository without ortg.tsv:
// ORTG is not enabled there, and nothing is created unless the user asks.
const noTableReply = "ortg: 本仓库没有 ortg.tsv，ORTG 未启用：不要再调用 ortg 工具，照常工作。只有用户明确要求创建（初始化）或对齐纲要时，才用 ortg_overview 传 init=true 建表。"

// disabledReply is all the model hears from ORTG in a repository with an
// ortg.disable file — the MCP server's instructions, with no tools — and the
// answer to whatever still reaches it there (a file created mid-session, the
// CLI): ORTG never runs there, whatever is asked.
const disabledReply = "ortg: 本仓库有 ortg.disable，ORTG 已停用：不要调用 ortg 工具，照常工作。"

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

// gateDenyLost (%s resetWhy) follows gateDeny when this session had the
// outline and lost it to its own compaction or clear.
const gateDenyLost = "（%s，之前加载的纲要已不在上下文里。）"

// resetWhy (%s the time, %s resetCompact or resetClear) says when and how
// this session's context lost the outline; overviewResent leads the outline
// sent again for it, so that a repeat in the transcript is not taken for a
// needless resend.
const resetWhy = "本会话的上下文在 %s %s"
const resetCompact = "被压缩"
const resetClear = "被清空"
const overviewResent = "ortg: 重发整份纲要的原因：%s，之前发过的条目已不在上下文里。别的会话压缩或清空不会让本会话重发。"
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
const gateMultiFile = "ortg: 这一步会让改动涉及多个计划外的文件（%s），是多文件改动，拦下（每次都拦）。先用 ortg_target 的 items 一次写全这次要改、要新建、要删的每个文件的目标纲要（改完后应有的纲要；只改实现、纲要不变的写 =），再调用 ortg_review 按全局纲要逐项核对，收敛后解锁（全写 = 或没发现要补的地方一次即收敛，否则补改后连续两次计划不变），之后照常改代码。若前一个文件其实已改完、是另一件事，先用 ortg_update 提交它的纲要再改这个。这是自查，不用给用户看、不用等用户同意，核对无误直接动手。"

// gateSoloEdit (%s the file) rides along the first write to a file outside
// any plan: a single-file change needs no plan, but its outline is due.
const gateSoloEdit = "ortg: %s 不在改动计划里，按单文件改动放行。改完立即用 ortg_update 提交它的新纲要；若还要改别的文件，先用 ortg_target 的 items 一次列全计划再核对。" + " " + disciplineSeen
const gatePlanUnreviewed = "ortg: %s 的目标纲要已写，但计划还没核对到收敛（全写 = 或没发现要补的地方一次即收敛，否则补改后连续两次 ortg_review 看到同一计划才解锁）。调用 ortg_review。"
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
const gateTestEdit = "ortg: %s 是测试文件。改它的期望值前按证据裁决：需求点名了这个测试或说旧行为是错的，才改期望；需求说既有测试权威、行为不变或只是重构迁移，就改实现、不改期望；需求给了命令与期望输出的验收例，以实跑结果为准；都没有，先找两全的实现，不能两全才按需求改，并把选择与理由记进纲要。新增测试照常写。依赖库版本不同造成的输出差异也须核对项目支持版本与公开契约。本机实际输出本身不是修改期望或测试选择的依据。"

// Final review records a model declaration for a source generation, not
// machine verification of the public task or unknown tests.
const finalReviewContractGuide = "按原公开任务的显式条件（允许/禁止、特殊值、明确组合、输出含义），逐项对应最终实际消费入口，写出真实断言及结果，或未验证的依据；接口摘要、自测通过计数不能代替逐项依据。"
const finalFlagDescription = "实写后的独立final阶段；不带generation/report时只读当前代际，默认false仍核对改前计划"
const finalGenerationDescription = "final声明绑定的当前代际，必须来自本调用上下文的Status或final回复"
const finalReportDescription = "final审查声明，各项非空、合计至多4096字节UTF8。" + " " + finalReviewContractGuide
const finalReviewNone = "ortg: 本调用上下文没有已归属的源码实写 final 审查义务；这不表示其他上下文或整个任务已验收。"
const finalReviewNoSession = "final 审查需要宿主提供当前会话身份，不能按整个仓库代替本调用上下文"
const finalReviewBadReport = "final 提交需要当前 generation 和非空 report；各项须非空，整份 report 须为有效 UTF-8 且不超过 4096 字节，不作截断"
const finalReviewMismatch = "final 代际已变化或不属于本调用上下文；先以 final=true 读取当前代际，再核查最终源码"
const finalReviewUnknown = "当前有 %d 个跟踪路径无法取得内容指纹，不能记录 final 声明；先核实可读取的最终持久源码"
const finalReviewPending = "待提交声明"
const finalReviewDone = "已有本代际声明"
const finalReviewHead = "ortg: 最终源码审查，generation=%q（%s）。这与改前计划 Review、纲要对齐和测试启动分开：\n"
const finalReviewPath = "- %s\n"
const finalReviewCount = "已跟踪 %d 个源码路径，本次列出 %d 个，另 %d 个因展示上限未列；未列路径仍绑定此代际。\n"
const finalReviewInstruction = finalReviewContractGuide + "仅核对任务明确的条件，不扩为所有参数组合或全库测试，不猜任务未规定的错误文案。report上限4096字节，放不下时明确已核对范围及其余未验证边界，不截断后声称全覆盖。用 ortg_review final=true、上述 generation 和非空 report 数组提交本代际声明。report 是模型的核查说明，不是原生语义验证或未知测试全通过证明。源码新变动或新识别失败会使旧声明失效。\n"
const finalReviewRecorded = "ortg: 已记录当前代际的 final 审查声明"
const finalReviewAccepted = finalReviewRecorded + "（%s）。这是本上下文的模型报告，不代表原生验证了语义正确或测试全通过。"
const finalFailureUnknown = "ortg: 此失败结果缺少宿主操作身份，本次保守使 final 声明失效；无法区分重放与同输出新运行，不把它当新增测试执行证据。"
const finalReviewStateWarning = "ortg: final 审查状态未能可靠读取或持久化，本次不能视为已记录或已结案；请检查状态目录后重试。"

const reviewEmpty = "ortg: 还没有任何目标纲要。先为这次要改、要新建、要删的每个文件用 ortg_target 写目标纲要，再调用 ortg_review。"
const reviewHead = "ortg: 第 %d 次核对改动计划，涉及 %d 个文件：\n"
const reviewAffected = "\n按全局纲要，下列文件的 Uses 依赖计划里的文件，却没有目标纲要——逐个判断：要跟着改的补目标纲要，不受影响的不用管：\n"

// reviewContracts heads, as a fallback for what the 【契约】 table misses, the guarded behaviour outside the plan that shares a
// name with it: a guard may sit in a module the plan never touches.
// usesNotPath (%s the items) refuses a Uses that names no repository path.
const usesNotPath = "Uses 里的 %s 不是仓库内的路径，纲要未写入。Uses 一律写仓库相对路径：文件写全路径，整个目录或包写目录路径并以 / 结尾（如 lib/storage/），可加尾部括注；路径中的空格和括号保留，完整真实路径优先，否则只剥尾部括注；包名、模块名、裸文件名都不行，第三方库与标准库不写——依赖方按路径反查，写不成路径的依赖会被漏掉。改好整行重提。"

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
const testOnlyWording = "纲要里写了\"单测入口\"：先按实际生产调用、外部消费者和项目测试结构核实用途；真实公开入口写成\"测试也调用\"并保持签名与可见性，仅供测试的辅助代码按项目约定记录，不能仅凭测试调用认定为公开 API"

// sameWithKeysNote (%s the file, %s its keys) rides along "=" on a file that
// takes part in contracts: an implementation change there may move where a
// parameter is enforced, which the outline and the contract must then say.
const sameWithKeysNote = "ortg: %s 参与契约 %s。若这次改动改变了参数流向或执行点（哪一层执行限额、校验、默认值），不要用 =：写完整目标纲要，并用 ortg_header 同步改契约正文。"

// apiCfgTestNote / apiGoneNote (%s the file, %s the names) follow an edit
// that hid an API-listed function behind #[cfg(test)] or removed a name the
// outline lists; testRefNote (%s the file, %s each name with the test files
// using it) follows one that removed a top-level name tests still import or
// call.
const apiCfgTestNote = "ortg: %s 里纲要 API 列出的 %s 现在在 #[cfg(test)] 下。先核实实际生产调用、外部消费者和项目测试结构：真实公开入口应在普通构建中可用，不得仅为消除警告而收窄；若确实只是测试辅助代码，按项目约定保留条件编译并修正纲要 API，不把它误记为公共契约。"
const apiGoneNote = "ortg: %s 里已找不到纲要 API 列出的 %s。先核实真实公共契约与实际消费者：兼容要求不变时保留或提供兼容迁移；需求允许删除或改名时同步调用方与纲要。仅供测试的辅助代码或内部实现可按任务和项目约定调整，不因纲要误列就冻结名称；若只是纲要没写全名，修正记录。"
const removedUnchecked = "（另有 %d 个删掉的名字没有逐个查测试引用，删之前自己确认。）"
const testRefNote = "ortg: %s 删掉了仍被测试导入或调用的名字：%s。缺失引用可能使测试收集或编译失败。先核实真实公共契约与测试用途：公开入口按兼容要求保留或迁移，测试辅助代码和内部实现可按任务调整，但须同步实际消费者并验证没有引入回归，不因被测试引用就冻结名称。"

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
8. 测试调用的入口：按实际生产调用、外部消费者和项目测试结构区分真实公开入口与测试辅助代码。公开入口的签名与普通构建可见性是否保留？仅供测试的辅助代码可按项目约定条件编译。
9. 执行点：计划是否写明各层职责、参数转换和相互作用，避免重复扣减、错误提前终止或旧层掩盖迁移缺陷？是否单点执行按任务契约决定，合法多层校验不受禁止。改了参数流向的文件不要用 = 。
10. 收尾验证：全部改完、提交之前，按仓库自己的测试配置跑一次不收窄的整包测试（不加 -k、不只跑单个文件或子目录、不用 -o addopts 等改掉收集范围），新出现的失败修掉；删掉或改名的名字，确认没有测试还在导入它；按项目支持环境核实整类跳过的原因，仅在任务相关且项目允许时以隔离副本或正常改动计划诊断，未执行的记成「本地验证盲区」，不算通过；测试另起进程调用的构建产物，确认是按测试所用的配置从当前源码新构建的。
` + "\n" + disciplineSeen + "\n" + finalSourceRule

// The commit gate (verifyCommit). gateVerifyCore refuses a commit of code
// no broad test run has seen since it last changed; gateVerifyVersionHint
// follows it when no version gate was detected (the generic advice);
// gateVerifyGates (%s the gates never run lifted) follows it when some were,
// gateVerifyGatesOnly refuses a commit for those alone, both then followed
// by gateLiftHowTo; gateVerifyGatesNudge (%s the gates in force) follows a
// refusal for a stale run when every gate was run lifted once.
const gateVerifyCore = "ortg: 自最近一次改代码以来还没跑过整包测试，这次提交先拦下（每个提交点只拦一次，再提交即放行）。提交前按仓库自己的测试配置跑一次不收窄的整包测试——不加 -k/-m，不只跑单个文件或子目录，不用 -o addopts、-c、-p no:doctest 之类改掉收集范围；太慢就加并行，输出写进日志文件，在本回合里等它跑完再看（给足超时，或用阻塞循环等日志里的结束标记）——非交互运行时回合一结束会话就退出，放到后台的测试等不到通知，还可能被一并终止。测试若另起进程调用构建产物（可执行文件、服务、插件），先查测试支持代码按什么变量、配置或目录找它，按那套配置从当前源码重新构建——产物比最近一次改动旧，测出的通过不算数。看新出现的失败：拿不准是不是这次改出来的，就在改动前的提交上对照着跑那几个测试（用 git worktree add 到临时目录，不要 stash 或切走当前改动）；是这次改出来的先修好。" + " " + finalSourceRule
const gateVerifyVersionHint = "测试配置按依赖版本、平台或可选依赖整类跳过时，先核对项目支持环境与跳过原因。仅在任务相关且项目允许时，在隔离副本或正常改动计划中诊断；未经验证的保持「本地验证盲区」。本机实际输出本身不是修改期望或测试选择的依据；配置与兼容策略的永久修改须有需求或项目契约依据。"
const gateVerifyGates = "另外，检测到按依赖版本整类跳过测试的分支，尚无诊断运行或核查确认记录；条件是否在本环境成立须核实：\n%s\n"
const gateVerifyGatesOnly = "ortg: 整包已经跑过，但检测到按依赖版本整类跳过测试的分支尚无诊断运行或核查确认记录；条件是否在本环境成立须核实。这次提交先拦下（每个提交点只拦一次，再提交即放行）：\n%s\n"
const gateLiftHowTo = "先核实条件在本地是否成立，以及项目是否要求在该环境运行这一类测试。不成立时，原配置已覆盖这一类；成立时，未运行的保持未验证。仅在任务相关且项目允许时，在隔离副本或正常改动计划中临时诊断，完成后恢复诊断改动；永久调整门控或测试期望须有需求或项目契约依据。本机实际输出本身不是修改期望或测试选择的依据。失败按本次回归、任务阻断、既有无关或环境问题分别核证，本次回归或任务阻断纳入修复，其余按证据报告。再次提交只确认已收到本次提醒，不证明运行或验收通过。"
const gateVerifyGatesNudge = "测试配置里仍有按依赖版本整类跳过测试的分支：\n%s\n是否在本环境跳过须核实；改动涉及其锁定行为时，按项目支持环境核实跳过原因。必要诊断在隔离副本或正常改动计划中进行，未执行的保持未验证，不默认删除门控或修改期望。"

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
const versionGateOverview = "ortg: 本地验证盲区（自动检出）——测试配置里按依赖版本整类跳过测试的分支如下。条件在本环境成立时，运行结果不覆盖这一类。先按项目支持环境和任务范围核实跳过原因；必要诊断在隔离副本或正常改动计划中进行，临时改动完成后恢复，永久修改须有需求或项目契约依据。build/兼容模式的提交核查会提醒尚未诊断或确认的门控；再次提交仅记录确认，不证明测试通过：\n%s"
const allSkippedNote = "ortg: 这次测试运行选中的用例全部被跳过（%d skipped），没有一条真正执行，不等于通过。用 -rs 查看跳过原因。"
const gatedFailuresNote = "ortg: 这次运行去掉了按依赖版本整类跳过测试的门控，汇总里有 %d 个失败或错误。先核实项目支持环境、公开契约和本次任务范围，不推定其他环境会有相同结果。逐项区分本次回归、任务阻断、既有无关失败及环境或兼容问题；本次回归或任务阻断纳入修复，其余按证据报告并保留失败记录。本机实际输出本身不是修改期望或测试选择的依据；不以清空失败记录为由扩大任务范围。临时诊断改动完成后恢复，永久修改须有需求或项目契约依据。"

// stopBgRunNote (%s the test run's command) blocks a Stop while a broad test
// run left in the background is still going and code changes are
// uncommitted.
const stopBgRunNote = "ortg: 你放到后台或脱离会话启动的整包测试（%s）还在运行，代码改动也还没提交。非交互运行时回合一结束会话就退出，等候的通知不会到来，测试还可能被一并终止。在本回合里用前台阻塞的方式等它跑完（例如循环检查它的日志里是否出现结束标记，给足超时），读完结果、处理好失败，再按任务要求提交或收尾。若这是交互会话、确实要先把控制交还给用户，说明测试仍在后台运行即可。"

const stopFailedTestsNote = "ortg: 本会话仍有 %d 条 pytest 运行的失败没有被完整通过证据覆盖，代码改动尚未提交：\n%s\n只复测部分失败节点、纲要对齐或没有新的报错摘要都不能证明这些失败已修复。按每条记录要求验证最终持久源码；需排除的混合失败，按首个异常和实际调用路径分组核证，不能用一个外围或环境缺口替整批其他首因免责。沿可见堆栈核实阻断目标功能的缺失符号与协作入口，阻断的纳入计划并修复。确与任务无关、环境阻断或当前无法修复的，按证据明确报告仍未通过及原因，不当成验收成功。解除失败记录不证明整包或全部契约通过；独立的提交前整包检查仍执行。本提醒对每次记录的失败只拦一次，不能代替实际测试。"
const stopFailedTestsRun = "- %s 下 `%s`: %s"
const stopFailedNodesNote = "\n  已保存全部 %d 个真实失败节点（显示 %d 个）：%s。可在同会话、目录、解释器和配置下用 -v 只复测全部这些节点；输出须保留相同的运行配置头、逐节点实际 PASSED 与数量一致的纯 passed 终端摘要，不接受 skipped/xfailed/xpassed。也可按原命令完整范围复测。"
const stopFailedNodesMore = "清单有省略，须从原真实 short test summary 取齐其余节点；只复测显示的部分不能结案。"
const stopFailedWholeNote = "\n无完整节点或运行配置证据的条目：仍须同工作目录、同原命令完整通过，测试数量不得缩小，不能用局部通过结案。收集中断的 error 数不代表选中的用例数，不能用局部节点通过覆盖；静态单条原命令可仅调整 -q/-v 以保留完整 passed 终端摘要，其余参数、环境和选择须相同，进度行不证明通过。后续新失败运行用 -v 保留解释器与运行配置头、实际逐节点结果和完整 short test summary，才可能建立节点证据；旧记录不能事后补猜。"
const stopFailedExcerptNote = "\n  字段过长已省略，以上是摘要、非完整命令；复测须从原运行记录取回完整目录和命令。"
const stopFailedTestsMore = "\n本次显示 %d 条，另 %d 条本次未列（可能含此前已提醒项）；未列条目仍保留，不视为已解决。"

// depBumpNote (%s the moved versions, "manifest：name old → new", with
// depChangeMore %d for the rest) follows an edit that moved an existing
// dependency's version.
const depBumpNote = "ortg: 这次改动改了已有依赖的版本——%s。先核实版本变化是否为任务或项目支持契约所必需，以及构建、测试与发布环境是否兼容；新增依赖带动的间接变化按任务范围核查，用户或此前留下的改动不要擅自撤回。与任务无关的顺手升级应恢复；任务确需升级时，说明原版本限制、替代方案及兼容影响，按项目规则实施并验证。未核实的支持环境或兼容性保持未验证，不仅因本机缓存里有新版本就升级。"
const depChangeMore = "另有 %d 处"
const allSkippedGates = "测试配置里有按依赖版本整类跳过测试的分支。先用 -rs 核实跳过原因及项目支持环境；与本次任务相关且项目允许时，在隔离副本或正常改动计划中诊断，未运行的不能算通过：\n%s"
const reviewAgain = "\n上面列出了要补或要核实的地方：有要补、要改的，用 ortg_target 改完再调用 ortg_review；逐项核实后没有要改的，也再调用一次 ortg_review 确认——连续两次核对看到的计划一样，才解锁这些文件开始改代码。"
const reviewConverged = "\n计划与上次核对一致，已收敛：以上 %d 个文件解锁，可以开始改代码（改完每个文件照常用 ortg_update 提交纲要）。计划之外再动多个文件仍会被拦，要改就补目标纲要再核对。"

// reviewConvergedFirst (%s why, %d the files) settles a plan at its first
// review: reviewQuickSame when every target is "=", reviewQuickClean when the
// review found nothing to fix or weigh.
const reviewConvergedFirst = "\n%s，一次核对即已收敛：以上 %d 个文件解锁，可以开始改代码（改完每个文件照常用 ortg_update 提交纲要）。计划之外再动多个文件仍会被拦，要改就补目标纲要再核对。"
const reviewQuickSame = "这份计划只改实现，每个文件的纲要都不变"
const reviewQuickClean = "核对没有发现要补的地方：目标纲要都合格，没有缺目标纲要的依赖方、要裁决的契约、同概念的「必须保持」条目或职责迁移"

// impactNote follows every post-edit reminder: %s the file, %s its candidate
// dependents (from R and the CBM call graph) or "无". Neither source sees
// semantic ties — a shared concept, config key or data format — so the model
// is also sent to the global outline. It asks for a judgement per file, not a
// blanket rewrite.
const impactNote = "ortg: 判断这次改动的影响面。候选依赖方（Uses=全局纲要里 Uses 写到 %s，调用=CBM 调用图里调用它）：%s。再通读全局纲要做语义判断：Role/API/Constraints 里涉及同一概念、配置项、数据格式、协议或约定的文件，即使不在候选里也可能受影响（相关模块不在上下文就先用 ortg_overview 取）。逐个判断——接口或约定变了、代码要跟着改的，改代码并提交它的新纲要；代码不用改但纲要描述已不准的（如 Uses、API、Constraints 里提到的名字或行为变了），只重写那行纲要用 ortg_update 提交；不受影响的不动。改动涉及的文件纲要里点名的守护测试（「必须保持 X（测试名）」）要运行；它们失败时先回到需求确认，需求没有明确要求改变那个行为，就改实现，不改测试期望值。直接做，不询问。"

// impactRules goes with updateDiscipline the first time a context gets it
// (entryContractRule is in updateDiscipline already); disciplineSeen stands
// for both afterwards — the full text stays in the context until a
// compaction or a clear, after which it is sent again.
var impactRules = dependencyBlockerRule

// graphRefusedNote (%s the root) joins the session report when CBM is
// installed but refuses this root.
const graphRefusedNote = "ortg: 本仓库直接在根目录下（%s），codebase-memory-mcp 拒绝索引这种位置，调用图不可用；改动的影响面只按 Uses 依赖与契约列出。\n"

const disciplineSeen = "（按本上下文已确认的 ORTG 规则页集执行；规则变更或上下文重置后须重新分页确认。）"

// dirRowMismatch refuses an outline whose trailing "/" disagrees with the
// path: a folder outline names a directory ("vendored/[标签]: ..."), a file's
// outline names a file.
const dirRowMismatch = "%s：目录要写目录纲要（文件名以 / 结尾，如 vendored/[标签]: Role:.. | Uses:.. | API:.. | Constraints:..），普通文件的纲要文件名不带 /"

const alignNeedsUpdate = "ortg: %s 已记录尚未提交新纲要的内容改动；请先调用 ortg_update，不能用 ortg_align 替代。"

const alignInFlightWrite = "ortg: %s 有尚未完成配对的写操作且当前内容已变化，暂不能前移指纹；操作完成后请按实际变更调用 ortg_update。"

const writeStateSaveWarning = "ortg: 纲要已提交，但实写维护记录保存失败（%v）；恢复状态目录后请重新提交对应纲要以清除旧记录。"
