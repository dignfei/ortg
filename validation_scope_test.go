package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ortg/base"
)

func TestTaskValidationScope(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	useTestMCPSession(t)
	conftest := "def pytest_collection_modifyitems(config, items):\n    if dep_version >= parse_version(\"2\"):\n        for item in items:\n            item.add_marker(pytest.mark.skip(reason=\"dependency output\"))\n"
	root, _ := gitRepo(t, map[string]string{"a.py": "x = 1\n", "conftest.py": conftest})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header("", nil, nil, nil, nil, nil, nil, "task"); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("a.py", updateLine("a.py", "neutral source")); err != nil {
		t.Fatal(err)
	}
	if doc, err := Overview("", false); err != nil || strings.Contains(doc, "本地验证盲区（自动检出）") {
		t.Fatalf("task overview must not prescribe unrelated global gates: %s %v", doc, err)
	}
	finishRules(t)
	x := mustOpen(t, root)
	if len(requiredGates(x)) != 1 {
		t.Fatal("fixture must have a real version gate for the legacy policy")
	}
	writeFile(t, filepath.Join(root, "a.py"), "x = 2\n")
	const failedCommand = "python -m pytest -q tests"
	HookReply("post", base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", Command: failedCommand, ToolOutput: "=== 1 failed, 2 passed in 0.1s ==="})
	failed := loadFailedTests(root, testMCPSession)
	if len(failed.Runs) == 0 {
		t.Fatal("fixture must retain an observed failure")
	}
	// A package run is still not a whole-repository run. Task mode does not
	// relabel it or pretend to know whether it covered the task's closure.
	Gate("Bash", "", "", "go test ./pkg/...", "")
	got, _ := Gate("Bash", "", "", "git add a.py && git commit -m scope", "")
	if !strings.Contains(got, deniedMark) || !strings.Contains(got, "任务维护提交前核查") || !strings.Contains(got, "完整受影响包") || !strings.Contains(got, "仓库明确要求全仓命令时仍须执行") || !strings.Contains(got, "暂存步骤也没有执行") || strings.Contains(got, "还没跑过整包测试") {
		t.Fatalf("task commit must give a scope check and preserve staging advice: %s", got)
	}
	if got, _ := Gate("Bash", "", "", "git commit -m scope", ""); !strings.Contains(got, "git status") {
		t.Fatalf("task retry must retain the owed-staging check: %s", got)
	}
	if got, _ := Gate("Bash", "", "", "git add a.py && git commit -m scope", ""); got != "" {
		t.Fatalf("scope reminder is bounded, without claiming tests passed: %s", got)
	}
	v := loadVerify(root)
	if !v.BroadRun.IsZero() || len(v.GateRuns) != 0 || len(v.DeniedGates) != 0 || !reflect.DeepEqual(failed, loadFailedTests(root, testMCPSession)) {
		t.Fatal("task reminder or retry fabricated validation or cleared failure evidence")
	}
	// A task reminder must not acknowledge the legacy policy when the mode
	// changes at the same HEAD, or change inventory/configured test roles.
	if _, err := Header("", nil, nil, nil, nil, nil, nil, "build"); err != nil {
		t.Fatal(err)
	}
	got, _ = Gate("Bash", "", "", "git commit -am scope", "")
	if !strings.Contains(got, "还没跑过整包测试") || !strings.Contains(got, "conftest.py:2") {
		t.Fatalf("explicit build retains the whole-suite and version-gate policy: %s", got)
	}
	if len(mustOpen(t, root).Rows()) != 2 || !reflect.DeepEqual(failed, loadFailedTests(root, testMCPSession)) {
		t.Fatal("scope policy changed inventory or observed failures")
	}
}

func TestTaskValidationMCPDelivery(t *testing.T) {
	root := finalFixture(t)
	if _, err := TargetItems([]string{"value.go =", "peer.go ="}); err != nil {
		t.Fatal(err)
	}
	finalWrite(t, root, testMCPSession, "", "scope-source", "value.go", "package sample\nconst Value = 2\n")
	replies := nativeMCP(t, root,
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_review", "arguments": map[string]any{}}},
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_review", "arguments": map[string]any{"final": true}}},
		map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_status", "arguments": map[string]any{}}},
	)
	for i, reply := range replies {
		if reply.Result.IsError || len(reply.Result.Content) != 1 {
			t.Fatalf("MCP scope response %d: %+v", i, reply)
		}
		text := reply.Result.Content[0].Text
		for _, want := range []string{"公开合法构造或获取→实际调用→输出", "必要 helper 与消费者闭环", "完整受影响包", "仓库明确要求全仓命令时仍须执行", "不人为缩小用户要求和项目规定的验收范围", "守护测试和已观察失败仍按证据覆盖", "本次回归或任务阻断纳入修复，其余按证据报告", "不因保留失败记录扩大任务范围"} {
			if !strings.Contains(text, want) {
				t.Fatalf("MCP channel %d omitted %q: %s", i, want, text)
			}
		}
		if strings.Index(text, "先沿公开") >= strings.Index(text, "再按仓库") || strings.Contains(text, "不只跑单个文件或子目录") {
			t.Fatalf("MCP channel %d must prioritize the real chain without prescribing unrelated suites: %s", i, text)
		}
	}
}
