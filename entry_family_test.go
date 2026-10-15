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
		for _, requirement := range []string{"需求明确覆盖多个注册、插件或表示实现时", "可见注册表、文档及调用方", "任务所需且受支持的集合", "适用的转换往返", "逐项对照该集合", "未执行项保留未验证", "不扩为无关插件或全库测试", "工厂或装饰器返回可调用对象时", "公开签名契约与工厂/包装器自身分别核对并记录", "位置参数槽、变参、仅关键字参数、默认值和可省略性", "接口元数据或调用预检 不得相对公开契约额外收窄合法调用", "内容/输出选项不隐含改变签名", "公开实际调用形状", "未执行的调用形状保留未验证"} {
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
