package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ortg/base"
)

func TestEntryFamilyDelivery(t *testing.T) {
	root := updateRepo(t, "a.go", "b.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"path": "a.go", "outline": updateLine("a.go", "fixture")}); failed {
		t.Fatal(msg)
	}
	checkText := func(stage, text string) {
		t.Helper()
		for _, requirement := range []string{
			"公开契约涉及可选、尽力或非致命的附带步骤时",
			"明确主操作结果、附带失败及恢复边界",
			"经本次涉及的全部公共入口验证，已明确可恢复的附带失败不改变已成功主操作的约定结果",
			"保留取消、退出等控制流语义",
			"未核实的边界保持未知，不将可选直接等同于失败可忽略",
			"相关默认重试、缓存及异常转换的行为和信号归属纳入 API/Constraints 和目标计划",
			"公开生产者→适配/中间层→实际调用方→可观察消费者的任务相关路径",
			"依据可见配置、依赖实现或测试核对实际生效的默认行为",
			"状态、返回值或异常由哪层处理、转换或消耗",
			"类型与调用边一致不能替代层间行为核验",
			"经实际入口断言消费者按公开契约可观察的结果，不能只验证底层结果",
			"未核实的默认行为或信号归属保持未知，不凭依赖名称猜测，不扩为无关层或全库审计",
			"公开契约明确值来源、对象字段或语义单位时，在 API/Constraints 和目标计划中保留这些约定",
			"公开合法构造或获取→实际调用实参→输出/消费的最短传值路径",
			"逐个核对相关调用点的值来源及转换，区分原始值与变换后值",
			"保留来源、允许转换与公开契约决定的比较标准（如语义等价或原始字节/文本一致）",
			"公开契约要求底层、原始或线格式表示时，不能默认以解码后再编码替代，即使业务值相等",
			"缺失与存在但内容为 null、空字符串、0 或 false 是否等价须按已有公开契约核对",
			"未核实等价前不以真值判断合并",
			"自测沿已有合法公开构造或获取链使用真实类型",
			"按该比较标准断言契约独立确定的期望，不以待验证的同一转换链生成期望",
			"替身通过不代替真实入口验证",
			"函数名、类型或调用边存在不证明传值符合契约",
			"用实际调用方路径断言契约结果，不能只测 helper",
			"未核实的来源或契约保持未知，不从参数名或不可见资料猜填，不扩为无关调用点或全库审计",
			"公开合法构造或获取→目标调用→输出的最短必要链", "“不要求完整子系统”不等于“禁止必要 helper”", "显式禁止须遵守", "不凭假想外围流程扩展恢复范围", "含函数对象传递/注册，不能只搜调用括号", "需求明确覆盖多个注册、插件或表示实现时", "可见注册表、文档及调用方", "任务所需且受支持的集合", "适用的转换往返", "逐项对照该集合", "未执行项保留未验证", "不扩为无关插件或全库测试", "工厂或装饰器返回可调用对象时", "公开签名契约与工厂/包装器自身分别核对并记录", "位置参数槽、变参、仅关键字参数、默认值和可省略性", "接口元数据或调用预检 不得相对公开契约额外收窄合法调用", "内容/输出选项不隐含改变签名", "公开实际调用形状", "未执行的调用形状保留未验证", "公开接口对同一参数规定多种值形态或重载时", "在 API/Constraints 和目标计划中保留适用上下文、判别依据、整体/逐项语义及返回形态", "不能仅凭容器类型一律拆分", "真实构造与调用，断言公开规定的返回结果或异常", "未验证形态保持未知", "只打印诊断且命令退出 0 不算语义验收", "公开反例须转为实际断言后复验", "不扩为无关形态、全组合或全库测试"} {
			if !strings.Contains(text, requirement) {
				t.Fatalf("%s did not deliver public entry contract requirement %q: %s", stage, requirement, text)
			}
		}
	}
	check := func(stage string, r updateMCPReply) {
		t.Helper()
		if r.Result.IsError || len(r.Result.Content) != 1 {
			t.Fatalf("%s native reply: %+v", stage, r)
		}
		checkText(stage, r.Result.Content[0].Text)
	}
	replies := nativeMCP(t, root,
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_overview", "arguments": map[string]any{}}},
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_gate", "arguments": map[string]any{"tool": "Edit", "path": filepath.Join(root, "a.go"), "dir": root}}},
	)
	check("initial overview", replies[0])
	check("unplanned edit gate", replies[1])
	replies = nativeMCP(t, root,
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_target", "arguments": map[string]any{"items": []string{"a.go " + updateLine("a.go", "new target"), "b.go " + updateLine("b.go", "new target")}}}},
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_review", "arguments": map[string]any{}}},
	)
	check("review checklist", replies[1])
	writeFile(t, filepath.Join(root, "a.go"), "updated fixture")
	checkText("edit hook", HookReply("post", base.HookInput{Cwd: root, SessionID: "entry-contract-hook", ToolName: "Edit", FilePath: filepath.Join(root, "a.go")}))
}

func TestOverviewRuleDelivery(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", host)
			// These real host settings reach the separately executed MCP child.
			t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "100")
			profile := t.TempDir()
			writeFile(t, filepath.Join(profile, "config.toml"), "model_context_window = 100\n")
			t.Setenv("CODEX_HOME", profile)
			root := updateRepo(t, "a.go", "unknown.go")
			if _, err := scanWithHeader(); err != nil {
				t.Fatal(err)
			}
			outline := updateLine("a.go", "fixture")
			if _, err := Update("a.go", outline); err != nil {
				t.Fatal(err)
			}
			get := func(module, thread string) string {
				t.Helper()
				r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{
					"name": "ortg_overview", "arguments": map[string]any{"module": module}, "_meta": map[string]any{"threadId": thread},
				}})[0]
				if r.Result.IsError || len(r.Result.Content) != 1 {
					t.Fatalf("overview: %+v", r)
				}
				return r.Result.Content[0].Text
			}
			check := func(out string, wantRules bool) {
				t.Helper()
				for _, rule := range []string{"纲要行纪律：", "公开契约涉及可选、尽力或非致命的附带步骤时", "公开契约决定的比较标准", "任务依赖闭环："} {
					want := 0
					if wantRules {
						want = 1
					}
					if strings.Count(out, rule) != want {
						t.Fatalf("want rules=%v for %q:\n%s", wantRules, rule, out)
					}
				}
			}
			bare := get("", testMCPSession)
			check(bare, true)
			if !strings.HasSuffix(bare, overviewModulesFooter) || strings.Contains(bare, outline) {
				t.Fatal("rules must not bypass the whole-outline budget")
			}
			check(get("", testMCPSession), false) // another server, same context
			module := get("ST", testMCPSession)
			check(module, false)
			if !strings.Contains(module, outline) {
				t.Fatal("rules must not mark unsent outline rows as delivered")
			}
			// Old binaries had no Rules field; changed content has a different
			// fingerprint. Both keep the already-delivered header and rows.
			for _, old := range []string{"", "previous neutral rule content"} {
				_, saved := base.OverviewMark(root, sentMark(testMCPSession))
				var cache map[string]any
				if err := json.Unmarshal([]byte(saved), &cache); err != nil {
					t.Fatal(err)
				}
				if old == "" {
					delete(cache, "Rules")
				} else {
					cache["Rules"] = old
				}
				wire, err := json.Marshal(cache)
				if err != nil {
					t.Fatal(err)
				}
				base.MarkOverview(root, sentMark(testMCPSession), string(wire))
				base.MarkOverview(root, "disc-"+sessionKey(testMCPSession), "") // legacy hook receipt
				short := get("ST", testMCPSession)
				check(short, true)
				if !strings.HasSuffix(short, overviewShortFooter) || strings.Contains(short, outline) {
					t.Fatal("rule upgrades must keep the outline's short-reply cache")
				}
				check(get("ST", testMCPSession), false)
			}
			status, err := Status()
			if err != nil || !strings.Contains(status, "unknown.go") || strings.Contains(status, "纲要行纪律：") {
				t.Fatalf("Status must retain its detailed inventory report: %q, %v", status, err)
			}
			markReset(root, testMCPSession, "", "compact")
			check(get("", testMCPSession), true)
			if host == "codex" {
				check(get("", "forked-neutral-context"), true)
				check(get("", "forked-neutral-context"), false)
			}
		})
	}
}
