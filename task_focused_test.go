package main

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTaskFocusedDelivery(t *testing.T) {
	root := updateRepo(t, "README.md", "wanted.go", "unrelated.go")
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	beforeConfig := mustOpen(t, root).Config()
	beforeBodies := map[string][]byte{}
	for _, p := range []string{"README.md", "wanted.go", "unrelated.go"} {
		beforeBodies[p], _ = os.ReadFile(filepath.Join(root, p))
	}
	check := func(stage, text string) {
		t.Helper()
		for _, requirement := range []string{"先读原任务和可见测试", "相关仓库相对路径及关联理由", "合法公共对象的构造或获取", "必要 helper 和消费者动态纳入", "已有有据且对齐的纲要复用", "未相关裸行保持未知", "可见测试缺失时", "不缩小任务", "只有用户明确要求全库初始化才用 mode=build"} {
			if !strings.Contains(text, requirement) {
				t.Fatalf("%s did not deliver focused task policy %q: %s", stage, requirement, text)
			}
		}
	}
	replies := nativeMCP(t, root,
		map[string]any{"method": "tools/list"},
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_overview", "arguments": map[string]any{}}},
	)
	found := 0
	for _, tool := range replies[0].Result.Tools {
		if tool.Name == "ortg_overview" || tool.Name == "ortg_header" {
			check(tool.Name+" description", tool.Description)
			found++
		}
	}
	if found != 2 || replies[1].Result.IsError || len(replies[1].Result.Content) != 1 {
		t.Fatalf("native policy channels: %+v", replies)
	}
	check("first overview", replies[1].Result.Content[0].Text)
	if !strings.Contains(replies[1].Result.Content[0].Text, "#骨架规则：只有裸文件名没有纲要的行是尚未建立认知的文件；普通任务仅对任务/可见测试相关文件") {
		t.Fatal("persistent header skeleton rule must use the same focused scope")
	}
	r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_header", "arguments": map[string]any{"header": testHeader, "mode": "task"}}})[0]
	if r.Result.IsError {
		t.Fatalf("task mode header: %+v", r)
	}
	check("task header", r.Result.Content[0].Text)
	text, failed := nativeUpdate(t, root, map[string]any{"path": "wanted.go", "outline": updateLine("wanted.go", "task related")})
	if failed || strings.Contains(text, initDocsCompleteNote) || strings.Contains(text, initCompleteNote) {
		t.Fatalf("task update must not require unrelated docs or declare full readiness: %s", text)
	}
	x := mustOpen(t, root)
	cfg := x.Config()
	if cfg.Mode != "task" || !slices.Equal(beforeConfig.IgnoreDirs, cfg.IgnoreDirs) || !slices.Equal(beforeConfig.IgnoreFiles, cfg.IgnoreFiles) || !slices.Equal(beforeConfig.KeepFiles, cfg.KeepFiles) || !slices.Equal(beforeConfig.ObserveDirs, cfg.ObserveDirs) || !slices.Equal(beforeConfig.ObserveFiles, cfg.ObserveFiles) || !maps.Equal(beforeConfig.Dirs, cfg.Dirs) || len(x.Rows()) != 3 {
		t.Fatalf("focused maintenance changed inventory or roles: %+v", x.Config())
	}
	for _, p := range []string{"README.md", "unrelated.go"} {
		row, _ := x.Row(p)
		if row.Outline != "" || row.Ignored || row.Deleted || x.Observed(p, reconcileOpts) || x.Excluded(p, reconcileOpts) {
			t.Fatalf("unrelated cognitive row must stay bare and unknown: %+v", row)
		}
	}
	for p, before := range beforeBodies {
		after, _ := os.ReadFile(filepath.Join(root, p))
		if !bytes.Equal(before, after) {
			t.Fatalf("outline selection altered actual source %s", p)
		}
	}
	// Explicit full initialization retains the existing full-document gate.
	r = nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_header", "arguments": map[string]any{"mode": "build"}}})[0]
	if r.Result.IsError || mustOpen(t, root).Config().Mode != "build" {
		t.Fatalf("explicit build mode did not persist: %+v", r)
	}
	if text, failed := nativeUpdate(t, root, map[string]any{"path": "wanted.go", "outline": updateLine("wanted.go", "explicit build")}); !failed || !strings.Contains(text, "README.md") {
		t.Fatalf("explicit full build must still require its remaining docs: %s", text)
	}

}
