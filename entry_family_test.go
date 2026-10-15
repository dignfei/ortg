package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestEntryFamilyDelivery(t *testing.T) {
	root := updateRepo(t, "a.go", "b.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"path": "a.go", "outline": updateLine("a.go", "fixture")}); failed {
		t.Fatal(msg)
	}
	check := func(stage string, r updateMCPReply) {
		t.Helper()
		if r.Result.IsError || len(r.Result.Content) != 1 {
			t.Fatalf("%s native reply: %+v", stage, r)
		}
		text := r.Result.Content[0].Text
		for _, requirement := range []string{
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
			"函数名、类型或调用边存在不证明传值符合契约",
			"用实际调用方路径断言契约结果，不能只测 helper",
			"未核实的来源或契约保持未知，不从参数名或不可见资料猜填，不扩为无关调用点或全库审计",
			"公开合法构造或获取→目标调用→输出的最短必要链", "“不要求完整子系统”不等于“禁止必要 helper”", "显式禁止须遵守", "不凭假想外围流程扩展恢复范围", "含函数对象传递/注册，不能只搜调用括号", "需求明确覆盖多个注册、插件或表示实现时", "可见注册表、文档及调用方", "任务所需且受支持的集合", "适用的转换往返", "逐项对照该集合", "未执行项保留未验证", "不扩为无关插件或全库测试", "工厂或装饰器返回可调用对象时", "公开签名契约与工厂/包装器自身分别核对并记录", "位置参数槽、变参、仅关键字参数、默认值和可省略性", "接口元数据或调用预检 不得相对公开契约额外收窄合法调用", "内容/输出选项不隐含改变签名", "公开实际调用形状", "未执行的调用形状保留未验证", "公开接口对同一参数规定多种值形态或重载时", "在 API/Constraints 和目标计划中保留适用上下文、判别依据、整体/逐项语义及返回形态", "不能仅凭容器类型一律拆分", "真实构造与调用，断言公开规定的返回结果或异常", "未验证形态保持未知", "只打印诊断且命令退出 0 不算语义验收", "公开反例须转为实际断言后复验", "不扩为无关形态、全组合或全库测试"} {
			if !strings.Contains(text, requirement) {
				t.Fatalf("%s did not deliver public entry contract requirement %q: %s", stage, requirement, text)
			}
		}
	}
	replies := nativeMCP(t, root,
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_overview", "arguments": map[string]any{}}},
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_gate", "arguments": map[string]any{"tool": "Edit", "path": filepath.Join(root, "a.go"), "dir": root}}},
	)
	check("unplanned edit gate", replies[1])
	replies = nativeMCP(t, root,
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_target", "arguments": map[string]any{"items": []string{"a.go " + updateLine("a.go", "new target"), "b.go " + updateLine("b.go", "new target")}}}},
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_review", "arguments": map[string]any{}}},
	)
	check("review checklist", replies[1])
}
