package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ortg/base"
)

// The child runs the actual MCP registration and stdio dispatcher, without
// building another binary or replacing any production function.
func TestUpdateMCPProcess(t *testing.T) {
	if os.Getenv("ORTG_UPDATE_MCP_CHILD") != "1" {
		return
	}
	if err := runMCP(nil); err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(2)
	}
	os.Exit(0)
}

type updateMCPReply struct {
	Result struct {
		IsError bool                    `json:"isError"`
		Content []struct{ Text string } `json:"content"`
		Tools   []struct {
			Name        string
			InputSchema struct {
				Required   []string
				Properties map[string]struct {
					Type  string
					Items struct{ Type string }
				}
			}
		} `json:"tools"`
	} `json:"result"`
	Error json.RawMessage `json:"error"`
}

// testMCPSession is the session the MCP child processes name.
const testMCPSession = "ortg-test-session"

// useTestMCPSession makes this process serve testMCPSession too, so the
// cache a test seeds here is the one its MCP children load.
func useTestMCPSession(t *testing.T) {
	callSession.ids, callSession.fromEnv = []string{testMCPSession}, true
	t.Cleanup(func() { callSession.ids, callSession.fromEnv = nil, false })
}

func nativeMCP(t *testing.T, root string, requests ...map[string]any) []updateMCPReply {
	t.Helper()
	var in bytes.Buffer
	for i, request := range requests {
		request["jsonrpc"], request["id"] = "2.0", i+1
		if err := json.NewEncoder(&in).Encode(request); err != nil {
			t.Fatal(err)
		}
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestUpdateMCPProcess$")
	if limit := os.Getenv("ORTG_UPDATE_MCP_LIMIT"); limit != "" {
		cmd = exec.Command("sh", "-c", `ulimit -f "$1"; shift; exec "$@"`, "ortg-mcp-test", limit, bin, "-test.run=^TestUpdateMCPProcess$")
	}
	cmd.Dir, cmd.Stdin = root, &in
	// The child names one fixed session, inside Claude Code or not, so the
	// cache it saves is found under the same mark (testMCPSession).
	cmd.Env = append(os.Environ(), "ORTG_UPDATE_MCP_CHILD=1", "CLAUDE_CODE_SESSION_ID="+testMCPSession)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("native MCP: %v\n%s\n%s", err, out, stderr.String())
	}
	var replies []updateMCPReply
	dec := json.NewDecoder(bytes.NewReader(out))
	for range requests {
		var reply updateMCPReply
		if err := dec.Decode(&reply); err != nil || len(reply.Error) > 0 {
			t.Fatalf("invalid MCP response: %v\n%s", err, out)
		}
		replies = append(replies, reply)
	}
	if dec.More() {
		t.Fatalf("extra MCP output: %s", out)
	}
	return replies
}

func nativeUpdate(t *testing.T, root string, args map[string]any) (string, bool) {
	t.Helper()
	r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_update", "arguments": args}})[0]
	if len(r.Result.Content) != 1 {
		t.Fatalf("update response: %+v", r)
	}
	return r.Result.Content[0].Text, r.Result.IsError
}

func updateRepo(t *testing.T, files ...string) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, file := range files {
		writeFile(t, filepath.Join(root, file), "content")
	}
	t.Chdir(root)
	return root
}

func updateLine(file, role string) string {
	return filepath.Base(file) + "[7 L ST T]: Role:" + role + " | Uses:- | API:- | Constraints:-"
}

func TestUpdateMCPInterfaceAndSingleSave(t *testing.T) {
	root := updateRepo(t, "a.go", "b.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	listed := nativeMCP(t, root, map[string]any{"method": "tools/list"})[0]
	found := false
	for _, tool := range listed.Result.Tools {
		if tool.Name != "ortg_update" {
			continue
		}
		found = true
		props := tool.InputSchema.Properties
		if props["items"].Type != "array" || props["items"].Items.Type != "string" || props["path"].Type != "string" || props["outline"].Type != "string" || len(tool.InputSchema.Required) != 0 {
			t.Fatalf("update schema must allow items or the legacy pair: %+v", tool.InputSchema)
		}
	}
	if !found {
		t.Fatal("ortg_update missing from native tools/list")
	}
	before, err := os.ReadFile(filepath.Join(root, base.TableName))
	if err != nil {
		t.Fatal(err)
	}
	a, b := updateLine("a.go", "甲"), updateLine("b.go", "乙")
	a = strings.Replace(a, "Uses:-", "Uses:b.go", 1)
	b = strings.Replace(b, "Uses:-", "Uses:a.go", 1)
	msg, failed := nativeUpdate(t, root, map[string]any{"items": []string{"a.go " + a, "b.go " + b}, "path": "missing.go", "outline": "ignored"})
	if failed || !strings.Contains(msg, "已提交 a.go") || !strings.Contains(msg, "已提交 b.go") {
		t.Fatalf("batch dispatch: %t %s", failed, msg)
	}
	if strings.Count(msg, "Uses（依赖）") != 1 || !strings.Contains(msg, fmt.Sprintf(headerDepNote, "a.go、b.go")) || strings.Index(msg, "Uses（依赖）") < strings.Index(msg, "已提交 b.go") {
		t.Fatalf("dependency guidance must describe the complete batch once, after item messages: %s", msg)
	}
	// A second save would replace this backup with a partially updated table.
	bak, err := os.ReadFile(filepath.Join(root, base.TableName+".bak"))
	if err != nil || !bytes.Equal(bak, before) {
		t.Fatalf("batch must save once; backup differs from complete pre-batch table: %v", err)
	}
	x := mustOpen(t, root)
	for p, want := range map[string]string{"a.go": a, "b.go": b} {
		if row, _ := x.Row(p); row.Outline != want {
			t.Fatalf("%s outline/fingerprint not aligned: %+v", p, row)
		}
		if fact, _ := x.Inspect(p); fact.FingerprintChanged || fact.OutlineEmpty || !fact.Exists {
			t.Fatalf("%s saved fingerprint must match current bytes: %+v", p, fact)
		}
	}
	legacy := updateLine("a.go", "旧接口修订")
	if msg, failed := nativeUpdate(t, root, map[string]any{"path": "a.go", "outline": legacy}); failed || !strings.Contains(msg, "已提交 a.go") {
		t.Fatalf("legacy pair must remain callable: %t %s", failed, msg)
	}
	if row, _ := mustOpen(t, root).Row("a.go"); row.Outline != legacy {
		t.Fatalf("legacy outline not stored: %+v", row)
	}
}

func TestUpdateMCPRejectsEmptyAndDuplicateInputs(t *testing.T) {
	root := updateRepo(t, "a.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	line := updateLine("a.go", "甲")
	for _, args := range []map[string]any{
		{"items": []string{}, "path": "a.go", "outline": line},
		{"items": nil, "path": "a.go", "outline": line},
		{"items": "a.go " + line},
		{"items": []any{"a.go " + line, 1}},
		{"items": []string{"a.go ="}},
		{"items": []string{"a.go 删除"}},
		{"items": []string{"a.go"}},
		{"path": "a.go"},
		{"path": 1, "outline": line},
		{"path": "a.go", "outline": 1},
	} {
		before, _ := os.ReadFile(filepath.Join(root, base.TableName))
		if msg, failed := nativeUpdate(t, root, args); !failed || msg == "" {
			t.Fatalf("bad arguments must fail: %v => %t %q", args, failed, msg)
		}
		after, _ := os.ReadFile(filepath.Join(root, base.TableName))
		if !bytes.Equal(before, after) {
			t.Fatalf("bad arguments changed the table: %v", args)
		}
	}
	for _, alias := range []string{"a.go", "./a.go", filepath.Join(root, "a.go")} {
		before, _ := os.ReadFile(filepath.Join(root, base.TableName))
		msg, failed := nativeUpdate(t, root, map[string]any{"items": []string{"a.go " + line, alias + " " + line}})
		if !failed || !strings.Contains(msg, "a.go") || !strings.Contains(msg, "重复") {
			t.Fatalf("canonical duplicate must name the path: %t %s", failed, msg)
		}
		after, _ := os.ReadFile(filepath.Join(root, base.TableName))
		if !bytes.Equal(before, after) {
			t.Fatalf("duplicate %q changed the table", alias)
		}
	}
}

func TestUpdateMCPAtomicRollback(t *testing.T) {
	useTestMCPSession(t)
	root := updateRepo(t, "a.go", "b.go", "observed.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"a.go", "b.go"} {
		if _, err := Update(file, updateLine(file, "原纲要")); err != nil {
			t.Fatal(err)
		}
		if _, err := Target(file, updateLine(file, "目标"), false, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Header("", nil, nil, nil, nil, []string{"observed.go"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Overview("", false); err != nil {
		t.Fatal(err)
	}
	overviewMu.Lock()
	c := sentFor(root)
	c.solo["a.go"], c.solo["b.go"] = true, true
	saveSent(root, c)
	overviewMu.Unlock()
	before, _ := os.ReadFile(filepath.Join(root, base.TableName))
	bakBefore, _ := os.ReadFile(filepath.Join(root, base.TableName+".bak"))
	_, sentBefore := base.OverviewMark(root, sentMark(testMCPSession))
	if sentBefore == "" {
		t.Fatal("the seeded cache must be saved under the session the MCP child names")
	}
	for _, bad := range []struct{ item, path, reason string }{
		{"b.go b.go[7 L ST T]: Role: | Uses:- | API:- | Constraints:-", "b.go", "Role"},
		{"b.go b.go[7 L ST T]: Role:乙 | Uses:no/such.go | API:- | Constraints:-", "b.go", "no/such.go"},
		{"b.go b.go[7 L ST T]: Role:乙 | Uses:- | API:- | Constraints:- | Keys:unknown", "b.go", "unknown"},
		{"absent.go " + updateLine("absent.go", "缺失"), "absent.go", "不存在"},
		{"observed.go " + updateLine("observed.go", "观察档"), "observed.go", "keep_files"},
	} {
		msg, failed := nativeUpdate(t, root, map[string]any{"items": []string{"a.go " + updateLine("a.go", "先写有效项"), bad.item}})
		if !failed || !strings.Contains(msg, bad.path) || !strings.Contains(msg, bad.reason) || !strings.Contains(msg, "全部未提交") || strings.Contains(msg, "已提交 a.go") {
			t.Fatalf("failure must identify path, reason and rollback: %t %s", failed, msg)
		}
		after, _ := os.ReadFile(filepath.Join(root, base.TableName))
		bakAfter, _ := os.ReadFile(filepath.Join(root, base.TableName+".bak"))
		_, sentAfter := base.OverviewMark(root, sentMark(testMCPSession))
		if !bytes.Equal(before, after) || !bytes.Equal(bakBefore, bakAfter) || sentBefore != sentAfter {
			t.Fatalf("failed %q changed table, backup or sent cache", bad.item)
		}
	}
	for _, file := range []string{"a.go", "b.go"} {
		if row, _ := mustOpen(t, root).Row(file); !strings.Contains(row.Outline, "原纲要") || !strings.Contains(row.Target, "目标") {
			t.Fatalf("rollback must retain outline and target: %+v", row)
		}
	}
}

func TestUpdateMCPDocumentGateAndBatchStages(t *testing.T) {
	root := updateRepo(t, "README.md", "docs/a.md", "main.go", "core.go", "ordinary.go")
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": []string{"README.md " + updateLine("README.md", "核心文档"), "main.go " + updateLine("main.go", "入口")}}); !failed || !strings.Contains(msg, "main.go") || !strings.Contains(msg, "纲要头还没写") {
		t.Fatalf("template header must reject the whole mixed batch: %t %s", failed, msg)
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": []string{"README.md " + updateLine("README.md", "核心文档")}}); failed || strings.Contains(msg, initDocsCompleteNote) {
		t.Fatalf("core documents remain accepted before the initial header: %t %s", failed, msg)
	}
	if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	doc, core := "docs/a.md "+updateLine("docs/a.md", "补齐文档"), "core.go "+updateLine("core.go", "核心实现")
	for _, items := range [][]string{{doc, core}, {core, doc}} {
		before, _ := os.ReadFile(filepath.Join(root, base.TableName))
		msg, failed := nativeUpdate(t, root, map[string]any{"items": items})
		if !failed || !strings.Contains(msg, "core.go") || !strings.Contains(msg, "docs/a.md") {
			t.Fatalf("batch ordering cannot bypass the document revision boundary: %t %s", failed, msg)
		}
		after, _ := os.ReadFile(filepath.Join(root, base.TableName))
		if !bytes.Equal(before, after) {
			t.Fatal("document gate must roll back earlier documents too")
		}
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": []string{doc, "README.md " + updateLine("README.md", "同批修订")}}); failed || strings.Count(msg, initDocsCompleteNote) != 1 || strings.Contains(msg, initCompleteNote) {
		t.Fatalf("document completion survives later rewrites and is emitted once: %t %s", failed, msg)
	}
	if _, err := Header(testHeader+"\n#【修订】完整文档", nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": []string{"main.go " + updateLine("main.go", "入口"), core}}); failed || strings.Contains(msg, initCompleteNote) || strings.Contains(msg, initDocsCompleteNote) {
		t.Fatalf("core batch must leave the ordinary phase pending: %t %s", failed, msg)
	}
	if _, err := Header(testHeader+"\n#【修订】核心代码", nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	items := []string{"ordinary.go " + updateLine("ordinary.go", "普通文件"), "core.go " + updateLine("core.go", "核心修订")}
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": items}); failed || strings.Count(msg, initCompleteNote) != 1 || strings.Count(msg, fmt.Sprintf(distillNote, "README.md、docs/a.md")) != 1 {
		t.Fatalf("final completion survives later rewrites and is emitted once: %t %s", failed, msg)
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": items}); failed || strings.Contains(msg, initCompleteNote) || strings.Contains(msg, initDocsCompleteNote) {
		t.Fatalf("rewriting a completed batch must not restart initialization: %t %s", failed, msg)
	}
}

func TestUpdateMCPDirectoryTargetsAndSentCache(t *testing.T) {
	useTestMCPSession(t)
	root := updateRepo(t, "a.go", "b.go", "vendored/x.go", "excluded/y.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header(testHeader+"\n#【契约】cache：命中后复用结果", []string{"excluded"}, nil, nil, []string{"vendored"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"a.go", "b.go"} {
		if _, err := Update(file, updateLine(file, "旧")); err != nil {
			t.Fatal(err)
		}
		if _, err := Target(file, "=", false, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Target("b.go", "", true, ""); err != nil {
		t.Fatal(err)
	}
	Overview("", false)
	overviewMu.Lock()
	c := sentFor(root)
	c.solo["a.go"], c.solo["b.go"] = true, true
	c.planned["a.go"] = true
	saveSent(root, c)
	overviewMu.Unlock()
	items := []string{
		"a.go a.go[7 L ST T]: Role:新甲 | Uses:vendored/,b.go(协作) | API:- | Constraints:- | Keys:cache(复用)",
		"b.go " + updateLine("b.go", "新乙"),
		"vendored vendored/[3 L ST T]: Role:第三方目录 | Uses:- | API:- | Constraints:只读",
		"excluded/ excluded/[3 L CL T]: Role:排除目录 | Uses:- | API:- | Constraints:只读",
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": items}); failed {
		t.Fatalf("directory and registered Uses/Keys must share the batch: %s", msg)
	}
	x := mustOpen(t, root)
	for _, file := range []string{"a.go", "b.go", "vendored", "excluded"} {
		if row, _ := x.Row(file); row.Target != "" || row.TargetDelete || row.Outline == "" {
			t.Fatalf("committed targets must clear for every item: %+v", row)
		}
	}
	if c := loadSent(root, testMCPSession); c == nil || c.solo["a.go"] || c.solo["b.go"] || !c.planned["a.go"] {
		t.Fatalf("successful batch must clear solo entries and retain the reviewed plan: %+v", c)
	}
	if got := x.Dependents("vendored/x.go"); len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("submitted directory Uses must refresh dependents: %v", got)
	}
	request := map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_overview", "arguments": map[string]any{}}}
	replies := nativeMCP(t, root, request, request)
	first, second := replies[0].Result.Content[0].Text, replies[1].Result.Content[0].Text
	if !strings.Contains(first, "Role:新甲") || !strings.Contains(first, "Role:新乙") || !strings.Contains(first, "vendored/[") || strings.Contains(first, "#目标:") || strings.Contains(second, "Role:新甲") || !strings.Contains(second, "本会话都已发给你") {
		t.Fatalf("changed rows resend once, directory rows included:\n%s\n%s", first, second)
	}
	for _, item := range []string{"vendored " + updateLine("vendored", "缺目录斜线"), "a.go a.go/[7 L ST T]: Role:错误目录 | Uses:- | API:- | Constraints:-", "vendored vendored/[3 L ST T]: Role:一 | Uses:- | API:- | Constraints:-\nv"} {
		if msg, failed := nativeUpdate(t, root, map[string]any{"items": []string{item}}); !failed || msg == "" {
			t.Fatalf("directory/line mismatch must fail: %t %s", failed, msg)
		}
	}
}

func TestUpdateMCPMatureAndFinalPhasePriority(t *testing.T) {
	t.Run("mature", func(t *testing.T) {
		root := updateRepo(t, "main.go", "another.go")
		if _, err := scanWithHeader(); err != nil {
			t.Fatal(err)
		}
		if _, err := Update("main.go", updateLine("main.go", "已有认知")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "README.md"), "new document")
		if _, err := Scan(); err != nil {
			t.Fatal(err)
		}
		if msg, failed := nativeUpdate(t, root, map[string]any{"items": []string{"another.go " + updateLine("another.go", "新增认知"), "main.go " + updateLine("main.go", "修订")}}); failed {
			t.Fatalf("mature source knowledge must retain document-gate compatibility: %s", msg)
		}
		if row, _ := mustOpen(t, root).Row("README.md"); row.Outline != "" {
			t.Fatal("a missing document remains pending in a mature repository")
		}
	})
	t.Run("documents only", func(t *testing.T) {
		root := updateRepo(t, "README.md", "docs/a.md")
		if _, err := Scan(); err != nil {
			t.Fatal(err)
		}
		if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		items := []string{"README.md " + updateLine("README.md", "总览"), "docs/a.md " + updateLine("docs/a.md", "文档")}
		if msg, failed := nativeUpdate(t, root, map[string]any{"items": items}); failed || strings.Count(msg, initCompleteNote) != 1 || strings.Contains(msg, initDocsCompleteNote) {
			t.Fatalf("final revision must take priority over intermediate document completion: %t %s", failed, msg)
		}
	})
}

func TestUpdateMCPNewAPIRollback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := updateRepo(t, "a.go", "b.go")
	writeFile(t, filepath.Join(root, "b.go"), "package sample\nfunc Existing() {}\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "a.go", "b.go"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "b.go"), "package sample\nfunc Existing() {}\nfunc Added() {}\n")
	before, _ := os.ReadFile(filepath.Join(root, base.TableName))
	items := []string{"a.go " + updateLine("a.go", "有效项"), "b.go " + updateLine("b.go", "遗漏接口")}
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": items}); !failed || !strings.Contains(msg, "b.go") || !strings.Contains(msg, "Added") {
		t.Fatalf("API validation must identify the failing path and export: %t %s", failed, msg)
	}
	after, _ := os.ReadFile(filepath.Join(root, base.TableName))
	if !bytes.Equal(before, after) {
		t.Fatal("new API rejection must roll back earlier valid items")
	}
	items[1] = "b.go b.go[7 L ST T]: Role:包含接口 | Uses:- | API:Existing,Added | Constraints:-"
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": items}); failed {
		t.Fatalf("listing the export must accept the complete batch: %s", msg)
	}
}

func TestUpdateMCPDiskSaveFailure(t *testing.T) {
	useTestMCPSession(t)
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file-size limit unavailable")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := updateRepo(t, "a.go", "b.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"a.go", "b.go"} {
		if _, err := Update(file, updateLine(file, "原纲要")); err != nil {
			t.Fatal(err)
		}
		if _, err := Target(file, updateLine(file, "目标"), false, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Overview("", false); err != nil {
		t.Fatal(err)
	}
	overviewMu.Lock()
	c := sentFor(root)
	c.solo["a.go"] = true
	saveSent(root, c)
	overviewMu.Unlock()
	before, err := os.ReadFile(filepath.Join(root, base.TableName))
	if err != nil || len(before) >= 4096 {
		t.Fatalf("pre-batch table must fit below the real write limit: %d bytes, %v", len(before), err)
	}
	bakBefore, err := os.ReadFile(filepath.Join(root, base.TableName+".bak"))
	if err != nil || bytes.Equal(bakBefore, before) {
		t.Fatalf("fixture must distinguish the old backup from the pre-batch table: %v", err)
	}
	_, sentBefore := base.OverviewMark(root, sentMark(testMCPSession))
	if sentBefore == "" {
		t.Fatal("the seeded cache must be saved under the session the MCP child names")
	}
	// POSIX shells count these blocks as 512 or 1024 bytes. The old table
	// fits either limit, but the submitted 20000-byte outline cannot fit.
	// This makes AtomicWrite's real Write fail after save has written .bak.
	t.Setenv("ORTG_UPDATE_MCP_LIMIT", "8")
	items := []string{"a.go " + updateLine("a.go", strings.Repeat("x", 20000)), "b.go " + updateLine("b.go", "新纲要")}
	msg, failed := nativeUpdate(t, root, map[string]any{"items": items})
	if !failed || !strings.Contains(msg, "全部未提交") || !strings.Contains(msg, ".bak") || !strings.Contains(msg, "file too large") {
		t.Fatalf("real save failure must report rollback and the backup limitation: %t %s", failed, msg)
	}
	after, err := os.ReadFile(filepath.Join(root, base.TableName))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed AtomicWrite must leave the current table and targets unchanged: %v", err)
	}
	bakAfter, err := os.ReadFile(filepath.Join(root, base.TableName+".bak"))
	if err != nil || !bytes.Equal(before, bakAfter) || bytes.Equal(bakBefore, bakAfter) {
		t.Fatalf("failed save may update .bak to the complete pre-batch table: %v", err)
	}
	_, sentAfter := base.OverviewMark(root, sentMark(testMCPSession))
	if sentAfter != sentBefore {
		t.Fatal("failed save must not change the persisted sent/solo cache")
	}
}
