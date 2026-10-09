package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ortg/base"
	"ortg/migrate"
)

const testHeader = "#====T====\n#A层级: E入口 L底层\n#B模块: ST存储 CL命令行\n#D特征: T-TSV\n#E规模: L大 M中 S小 T微"

// scanWithHeader builds the table like a fresh repository's first scan and
// then writes a real header, as a model must before any outline or plan is
// accepted; tests about other behaviour start from there.
func scanWithHeader() (string, error) {
	out, err := Scan()
	if err != nil {
		return out, err
	}
	x, err := openIndex(repoRoot())
	if err != nil {
		return out, err
	}
	_, missing := docs(x) // the first build outlines the documents first
	for _, p := range missing {
		if _, err := Update(p, path.Base(p)+"[3 L ST T]: Role:文档 | Uses:- | API:- | Constraints:-"); err != nil {
			return out, err
		}
	}
	_, err = Header(testHeader, nil, nil, nil, nil, nil, nil)
	return out, err
}

// TestMain keeps the tests off the local CBM: they would otherwise index
// every sandbox repository they create into the user's graph store. The
// integration tests' binaries inherit the variable.
func TestMain(m *testing.M) {
	os.Setenv("ORTG_CBM", "off")
	os.Exit(m.Run())
}

func TestClassify(t *testing.T) {
	cases := []struct {
		f    base.Fact
		want state
	}{
		{base.Fact{Ignored: true, Exists: true}, stIgnored},
		{base.Fact{IsMissing: true, OutlineEmpty: true, HasTarget: true, NeverSeen: true}, stPlanned},
		{base.Fact{IsMissing: true, OutlineEmpty: true, HasTarget: true}, stTombstone},
		{base.Fact{IsMissing: true}, stTombstone},
		{base.Fact{Exists: true, IsNew: true, OutlineEmpty: true}, stSkeleton},
		{base.Fact{Exists: true, OutlineEmpty: true, HasTarget: true}, stSkeleton},
		{base.Fact{Exists: true, FingerprintChanged: true, OutlineOlderThanFile: true}, stStale},
		{base.Fact{Exists: true, FingerprintChanged: true, OutlineOlderThanFile: false, HasTarget: true}, stPending},
		{base.Fact{Exists: true, FingerprintChanged: true}, stAligned},
		{base.Fact{Exists: true, HasTarget: true}, stPending},
		{base.Fact{Exists: true}, stAligned},
	}
	for i, c := range cases {
		if got := classify(c.f); got != c.want {
			t.Errorf("case %d: got %s want %s", i, stateNames[got], stateNames[c.want])
		}
	}
}

func TestApplyAndOutlineFlow(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, "gone.go"), []byte("x"), 0o644)
	x := base.NewMemIndex(root, testHeader)
	x.Batch(func() error {
		_, e := x.ImportOutline(testHeader + "\n===r " + filepath.ToSlash(root) + "/===\na.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-\nplanned.go[5 L ST T]: Role:p | Uses:- | API:- | Constraints:-\ngone.go[3 L ST T]: Role:g | Uses:- | API:- | Constraints:-\n#目标:删除\n")
		return e
	})
	facts, err := x.Reconcile(reconcileOpts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := apply(x, facts)
	if err != nil {
		t.Fatal(err)
	}
	// With split-import: a.go and gone.go exist on disk → Outline (stAligned /
	// stPending because gone.go carries #目标:删除); planned.go absent → stPlanned.
	if len(s.byState[stAligned]) != 1 || len(s.byState[stPending]) != 1 || len(s.byState[stPlanned]) != 1 {
		t.Fatalf("first reconcile: %v", s.byState)
	}
	// model implements a.go and submits its outline: target is applied
	if c := x.CheckEntry("a.go", "a.go[7 L ST T]: Role:实现 | Uses:- | API:- | Constraints:-"); len(c.Errors) != 0 {
		t.Fatal(c.Errors)
	}
	x.SetOutline("a.go", "a.go[7 L ST T]: Role:实现 | Uses:- | API:- | Constraints:-")
	x.ClearTarget("a.go")
	// gone.go is deleted on disk: tombstone, then purged because target_delete
	os.Remove(filepath.Join(root, "gone.go"))
	facts, _ = x.Reconcile(reconcileOpts)
	s, _ = apply(x, facts)
	if len(s.byState[stAligned]) != 1 || len(s.byState[stTombstone]) != 0 || s.purged != 1 {
		t.Fatalf("second reconcile: %v purged=%d", s.byState, s.purged)
	}
	if _, ok := x.Row("gone.go"); ok {
		t.Fatal("purged row still present")
	}
	stateOf := func(p string) state {
		facts, _ := x.Reconcile(reconcileOpts)
		for _, f := range facts {
			if f.Path == p {
				return classify(f)
			}
		}
		t.Fatalf("no fact for %s", p)
		return 0
	}
	// outline written after the code changed (clock ahead): silent align
	realNow := base.Now
	base.Now = func() time.Time { return time.Now().UTC().Add(time.Hour).Truncate(time.Second) }
	x.SetOutline("a.go", "a.go[7 L ST T]: Role:实现 | Uses:- | API:- | Constraints:-")
	base.Now = realNow
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a // v2"), 0o644)
	if st := stateOf("a.go"); st != stAligned {
		t.Fatalf("outline newer than code must align silently, got %s", stateNames[st])
	}
	// code changed right after the outline, within the same second: stale
	x.SetOutline("a.go", "a.go[7 L ST T]: Role:实现 | Uses:- | API:- | Constraints:-")
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a // v3"), 0o644)
	if st := stateOf("a.go"); st != stStale {
		t.Fatalf("code changed after outline must be stale, got %s", stateNames[st])
	}
	// file removed then restored with the same bytes: tombstone must clear
	os.Remove(filepath.Join(root, "a.go"))
	facts, _ = x.Reconcile(reconcileOpts)
	apply(x, facts)
	if r, _ := x.Row("a.go"); !r.Deleted {
		t.Fatal("missing file must be a tombstone")
	}
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a // v3"), 0o644)
	facts, _ = x.Reconcile(reconcileOpts)
	s, _ = apply(x, facts)
	if r, _ := x.Row("a.go"); r.Deleted {
		t.Fatal("restored file must lose its tombstone")
	}
	out := x.Render(renderOpts(x, ""))
	if !strings.Contains(out, "#计划新建 planned.go") || !strings.Contains(out, "Role:实现") {
		t.Fatalf("render wrong:\n%s", out)
	}
	if !strings.Contains(s.report(), "计划新建 1") {
		t.Fatalf("report wrong: %s", s.report())
	}
}

func TestExcludedPathsAndHelpers(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, "overview.txt"), []byte("x"), 0o644)
	if _, err := base.Create(root, testHeader); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if _, err := Update(filepath.Join(root, "a.go"), "a.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatalf("absolute path must be accepted: %v", err)
	}
	if _, err := Update("overview.txt", "overview.txt[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-"); err == nil {
		t.Fatal("excluded file must be rejected")
	}
	for _, p := range []string{"overview.txt", "ortg.tsv", "build/x"} {
		in := base.HookInput{ToolName: "Edit", FilePath: filepath.Join(root, p), Cwd: root}
		if out := HookReply("post", in); out != "" {
			t.Fatalf("hook must stay silent for %s, got %q", p, out)
		}
	}
	in := base.HookInput{ToolName: "Edit", FilePath: filepath.Join(root, "a.go"), Cwd: root}
	if out := HookReply("post", in); !strings.Contains(out, "a.go") {
		t.Fatalf("hook must nag for a.go, got %q", out)
	}
	os.WriteFile(filepath.Join(root, "ortg.tsv"), []byte("garbage"), 0o644)
	if out := HookReply("session", base.HookInput{Cwd: root}); !strings.Contains(out, "无法加载") {
		t.Fatalf("session hook must report a corrupt table, got %q", out)
	}
	if out := HookReply("post", in); out != "" {
		t.Fatalf("other hooks stay silent on a corrupt table, got %q", out)
	}
	newer := "#ORTG-TSV: 2\n{}\n" + base.ColumnHeader() + "\tnewer\n"
	os.WriteFile(filepath.Join(root, "ortg.tsv"), []byte(newer), 0o644)
	if out := HookReply("session", base.HookInput{Cwd: root}); !strings.Contains(out, "格式本版 ortg 不认识") || strings.Contains(out, "从 ortg.tsv.bak 恢复") {
		t.Fatalf("session hook must name a table this ortg predates, got %q", out)
	}
	if got := stripBodyMarker("#h\n\n" + bodyMarker + "\n===r /r/===\n"); strings.Contains(got, bodyMarker) || !strings.Contains(got, "===r /r/===") {
		t.Fatalf("body marker not stripped: %q", got)
	}
}

func TestPreInjectsOncePerSession(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	if _, err := base.Create(root, testHeader); err != nil {
		t.Fatal(err)
	}
	in := base.HookInput{ToolName: "Edit", FilePath: filepath.Join(root, "a.go"), Cwd: root, SessionID: "s1"}
	if HookReply("pre", in) == "" {
		t.Fatal("first pre must inject")
	}
	if out := HookReply("pre", in); out != "" {
		t.Fatalf("second pre in the same session must stay silent, got %q", out)
	}
	if HookReply("session", base.HookInput{Cwd: root, SessionID: "s1", Source: "resume"}) == "" {
		t.Fatal("session hook must answer")
	}
	if out := HookReply("pre", in); out != "" {
		t.Fatalf("resume keeps the context, pre must stay silent, got %q", out)
	}
	if HookReply("session", base.HookInput{Cwd: root, SessionID: "s1", Source: "compact"}) == "" {
		t.Fatal("session hook must answer")
	}
	if HookReply("pre", in) == "" {
		t.Fatal("pre must inject again after compact")
	}
	in.SessionID = ""
	if HookReply("pre", in) == "" || HookReply("pre", in) == "" {
		t.Fatal("without session_id every pre must inject")
	}
}

func TestApplyPatchHooksCoverEverySafePath(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	for _, p := range []string{"a.go", "old.go"} {
		if err := os.WriteFile(filepath.Join(root, p), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("a.go", "a.go[5 L CL T]: Role:甲 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("old.go", "old.go[5 L CL T]: Role:乙 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}

	in := base.HookInput{
		Cwd:       root,
		SessionID: "patch-1",
		ToolName:  "apply_patch",
		FilePath:  "a.go",
		FilePaths: []string{"a.go", "new.go", "old.go", "../escape.go", outside},
	}
	pre := HookReply("pre", in)
	for _, want := range []string{"a.go[5 L CL T]", "new.go 尚无纲要", "old.go[5 L CL T]"} {
		if !strings.Contains(pre, want) {
			t.Fatalf("pre hook missed %q:\n%s", want, pre)
		}
	}
	if strings.Contains(pre, "escape.go") || strings.Contains(pre, "outside.go") {
		t.Fatalf("pre hook accepted a path outside the repository:\n%s", pre)
	}

	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "old.go")); err != nil {
		t.Fatal(err)
	}
	post := HookReply("post", in)
	for _, want := range []string{"已改动 a.go", "已改动 new.go", "已删除 old.go", "delete=true"} {
		if !strings.Contains(post, want) {
			t.Fatalf("post hook missed %q:\n%s", want, post)
		}
	}
	if strings.Contains(post, "escape.go") || strings.Contains(post, "outside.go") {
		t.Fatalf("post hook accepted a path outside the repository:\n%s", post)
	}
}

// newly ignored files must lose their row, not linger as ignored=1 noise
func TestIgnoredRowsPurged(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "skip"), 0o755)
	os.WriteFile(filepath.Join(root, "skip", "x.md"), []byte("x"), 0o644)
	x := base.NewMemIndex(root, testHeader)
	facts, err := x.Reconcile(reconcileOpts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apply(x, facts); err != nil {
		t.Fatal(err)
	}
	if _, ok := x.Row("skip/x.md"); !ok {
		t.Fatal("skeleton row missing before ignore")
	}
	x.SetConfig(base.Config{IgnoreDirs: []string{"skip"}})
	facts, err = x.Reconcile(reconcileOpts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := apply(x, facts)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := x.Row("skip/x.md"); ok {
		t.Fatal("ignored row still in table")
	}
	if len(s.byState[stIgnored]) != 1 || s.purged != 1 {
		t.Fatalf("ignored=%v purged=%d", s.byState[stIgnored], s.purged)
	}
}

// ImportOutline must remap section paths when the document was authored in a
// different repo location (e.g. after copy/move of the repository).
func TestImportOutlineRemapsPaths(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "base"), 0o755)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, "base", "b.go"), []byte("package base"), 0o644)

	x := base.NewMemIndex(root, testHeader)
	// Document authored in /old/root but repo is now at root.
	doc := testHeader + "\n" +
		"===根 /old/root/===\na.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-\n" +
		"===底层 /old/root/base/===\nb.go[7 L ST T]: Role:b | Uses:- | API:- | Constraints:-\n"
	res, err := x.ImportOutline(doc)
	if err != nil {
		t.Fatalf("ImportOutline failed: %v", err)
	}
	if res.Entries != 2 {
		t.Fatalf("expected 2 entries, got %d", res.Entries)
	}
	if _, ok := x.Row("a.go"); !ok {
		t.Fatal("a.go not imported")
	}
	if _, ok := x.Row("base/b.go"); !ok {
		t.Fatal("base/b.go not imported")
	}
}

// A file that exists on disk but is gitignored must NOT be classified as
// tombstone when it already has an outline row in the tsv.
func TestReconcileGitignoredFileNotTombstone(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")

	// tracked.go is git-tracked; secret.md is gitignored.
	os.WriteFile(filepath.Join(root, "tracked.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("secret.md\n"), 0o644)
	os.WriteFile(filepath.Join(root, "secret.md"), []byte("private notes"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "init")

	x := base.NewMemIndex(root, testHeader)
	// Give both files an outline row.
	x.Batch(func() error {
		x.SetOutline("tracked.go", "tracked.go[7 L ST T]: Role:t | Uses:- | API:- | Constraints:-")
		x.SetOutline("secret.md", "secret.md[5 L ST T]: Role:s | Uses:- | API:- | Constraints:-")
		return nil
	})

	facts, err := x.Reconcile(reconcileOpts)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range facts {
		if f.Path == "secret.md" {
			if f.IsMissing {
				t.Fatal("gitignored file with outline row must not be IsMissing")
			}
			if !f.Exists {
				t.Fatal("gitignored file must have Exists=true")
			}
			st := classify(f)
			if st == stTombstone {
				t.Fatal("gitignored file must not be tombstone")
			}
			return
		}
	}
	t.Fatal("secret.md fact not found")
}

// missingNote returns "" for empty list and a warning for non-empty.
func TestMissingNote(t *testing.T) {
	if got := missingNote(nil); got != "" {
		t.Fatalf("nil missing should be empty, got %q", got)
	}
	got := missingNote([]string{"a.go", "b.go"})
	if !strings.Contains(got, "2 个索引条目") || !strings.Contains(got, "a.go") || !strings.Contains(got, "b.go") {
		t.Fatalf("missingNote wrong: %q", got)
	}
	if !strings.Contains(got, "ortg_target delete=true") {
		t.Fatal("missingNote must mention remediation")
	}
}

// openOrBootstrap with overview.txt: existing file → Outline, missing → Target + warning.
func TestOpenOrBootstrapSplitImport(t *testing.T) {
	root := t.TempDir()
	// a.go exists on disk; b.go is only in the overview (missing).
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	overview := testHeader + "\n===根 " + filepath.ToSlash(root) + "/===\na.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-\nb.go[7 L ST T]: Role:b | Uses:- | API:- | Constraints:-\n"
	os.WriteFile(filepath.Join(root, "overview.txt"), []byte(overview), 0o644)
	t.Chdir(root)
	x, note, err := openOrBootstrap(root)
	if err != nil {
		t.Fatal(err)
	}
	// a.go → Outline
	ra, ok := x.Row("a.go")
	if !ok || ra.Outline == "" {
		t.Fatalf("a.go should be in Outline: ok=%v row=%+v", ok, ra)
	}
	// b.go → Target + Missing warning in note
	rb, ok := x.Row("b.go")
	if !ok || rb.Target == "" || rb.Outline != "" {
		t.Fatalf("b.go should be in Target: ok=%v row=%+v", ok, rb)
	}
	if !strings.Contains(note, "1 个索引条目") || !strings.Contains(note, "b.go") {
		t.Fatalf("note must warn about missing files: %q", note)
	}
}

// openOrBootstrap on empty project (overview only, no code files) → all Target, no warning.
func TestOpenOrBootstrapEmptyProject(t *testing.T) {
	root := t.TempDir()
	overview := testHeader + "\n===根 " + filepath.ToSlash(root) + "/===\na.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-\n"
	os.WriteFile(filepath.Join(root, "overview.txt"), []byte(overview), 0o644)
	t.Chdir(root)
	x, note, err := openOrBootstrap(root)
	if err != nil {
		t.Fatal(err)
	}
	ra, ok := x.Row("a.go")
	if !ok || ra.Target == "" || ra.Outline != "" {
		t.Fatalf("a.go should be in Target only: ok=%v row=%+v", ok, ra)
	}
	if strings.Contains(note, "索引条目对应的文件在磁盘上不存在") {
		t.Fatalf("empty project must not trigger missing warning: %q", note)
	}
}

// TestBootstrapFromLegacyOverview verifies the end-to-end import-timing
// contract: when the repository has no ortg.tsv but an existing (legacy)
// overview.txt, the legacy content is fully read into memory, imported into
// the new table (split by file existence), and moved aside: no copy of the
// outline stays at overview.txt for a model to read instead of ortg_overview.
func TestBootstrapFromLegacyOverview(t *testing.T) {
	root := t.TempDir()
	// Source files the legacy document refers to.
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package b"), 0o644)

	legacy := testHeader + "\n\n===legacy /" + filepath.ToSlash(root) + "/===\na.go[LST7T]: F:legacy-a | R:- | A:- | S:-\nb.go[LST5S]: F:legacy-b | R:- | A:- | S:-\n"
	os.WriteFile(filepath.Join(root, "overview.txt"), []byte(legacy), 0o644)

	t.Chdir(root)
	out, err := Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if !strings.Contains(out, "已从 overview.txt 建表") {
		t.Fatalf("Scan output missing bootstrap note: %s", out)
	}

	// The legacy entries must have landed in the table. Both files exist on
	// disk, so split-import puts them into the Outline column (aligned).
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, wantOutline := range map[string]string{
		"a.go": "a.go[7 L ST T]: Role:legacy-a | Uses:- | API:- | Constraints:-",
		"b.go": "b.go[5 L ST S]: Role:legacy-b | Uses:- | API:- | Constraints:-",
	} {
		r, ok := x.Row(name)
		if !ok {
			t.Fatalf("row %s missing after bootstrap", name)
		}
		if !strings.Contains(r.Outline, wantOutline) {
			t.Fatalf("%s outline = %q, want substring %q", name, r.Outline, wantOutline)
		}
	}

	if _, err := os.Stat(filepath.Join(root, "overview.txt")); err == nil {
		t.Fatal("overview.txt must be moved aside after the import, not kept or re-exported")
	}
}

const testV1Meta = `#ORTG-META-VOLUME: 1
#S配额: C9-8≤600 C7-4≤500 C3-1≤50
#[Tag dictionary: code]
#A Layer: R路由 O文档 X构建
#B Module: D容器 P平台
#C Importance: 9核心 8高频 7业务 5常规 3辅助 1边缘
#D Trait: A异步 B构建 AB异步+构建
#E Scale: L大>400 T微<100
#[Tag dictionary: database]
#A Layer: E实体 T事务
`

// openOrBootstrap with a v1 root-manifest overview.txt and intact split
// volumes upgrades in place: existing files land in Outline, absent ones in
// Target, and the note names the upgrade plus the un-migrated module volume.
func TestOpenOrBootstrapV1Upgrade(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	manifest := "#ORTG-ROOT-MANIFEST: 1\n#Volume: id=meta kind=meta path=overview.meta.txt format=meta-v1\n#Volume: id=code kind=code path=overview.code.txt format=object-fras-v2\n"
	os.WriteFile(filepath.Join(root, "overview.txt"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(root, "overview.meta.txt"), []byte(testV1Meta), 0o644)
	slash := filepath.ToSlash(root)
	code := "#ORTG-CODE-VOLUME: 1\n===" + slash + "/===\na.go[9 R D T]: Role:a | Uses:- | API:- | Constraints:-\nb.go[8 R D A B T]: Role:b | Uses:- | API:- | Constraints:-\n===" + slash + "/sub/===\nc.go[5 O P T]: Role:c | Uses:- | API:- | Constraints:-\n"
	os.WriteFile(filepath.Join(root, "overview.code.txt"), []byte(code), 0o644)
	t.Chdir(root)
	x, note, err := openOrBootstrap(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "已从 ORTG v1 分卷索引升级建表：2 个目录段、3 条纲要") {
		t.Fatalf("upgrade note wrong: %q", note)
	}
	if !strings.Contains(note, "（模块卷本轮未迁移）") {
		t.Fatalf("note must mention the un-migrated module volume: %q", note)
	}
	if !strings.Contains(note, "b.go") || !strings.Contains(note, "sub/c.go") {
		t.Fatalf("note must list missing entries: %q", note)
	}
	if r, _ := x.Row("a.go"); r.Outline == "" || r.Target != "" {
		t.Fatalf("a.go should be in Outline: %+v", r)
	}
	for _, p := range []string{"b.go", "sub/c.go"} {
		if r, ok := x.Row(p); !ok || r.Target == "" || r.Outline != "" {
			t.Fatalf("%s should be in Target: ok=%v row=%+v", p, ok, r)
		}
	}
	if !strings.Contains(x.Header(), "#A层级: R路由 O文档 X构建") {
		t.Fatalf("header must carry the renamed v1 dictionary:\n%s", x.Header())
	}
}

// An overview.txt that imports cleanly but yields zero entries (e.g. a stray
// v2 rendering of a skeleton table) must still give way to the v1 upgrade
// when the split volumes are intact.
func TestOpenOrBootstrapV1UpgradeOnZeroEntryImport(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	slash := filepath.ToSlash(root)
	os.WriteFile(filepath.Join(root, "overview.txt"), []byte(headerTemplate+"\n===根 "+slash+"/===\na.go\n"), 0o644)
	os.WriteFile(filepath.Join(root, "overview.meta.txt"), []byte(testV1Meta), 0o644)
	os.WriteFile(filepath.Join(root, "overview.code.txt"), []byte("#ORTG-CODE-VOLUME: 1\n==="+slash+"/===\na.go[9 R D T]: Role:a | Uses:- | API:- | Constraints:-\n"), 0o644)
	t.Chdir(root)
	x, note, err := openOrBootstrap(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "已从 ORTG v1 分卷索引升级建表：1 个目录段、1 条纲要") || !strings.Contains(note, "（模块卷本轮未迁移）") {
		t.Fatalf("zero-entry import must fall back to the v1 upgrade: %q", note)
	}
	if r, _ := x.Row("a.go"); r.Outline == "" || !strings.Contains(x.Header(), "#A层级: R路由") {
		t.Fatalf("v1 outline and dictionary must win: row=%+v header=%.80s", r, x.Header())
	}
}

// A v1 root manifest without an intact code volume keeps the old manual
// migration hint path untouched.
func TestOpenOrBootstrapV1ManifestWithoutVolumes(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "overview.txt"), []byte("#ORTG-ROOT-MANIFEST: 1\n#Volume: id=code kind=code path=overview.code.txt format=object-fras-v2\n"), 0o644)
	t.Chdir(root)
	if _, note, err := openOrBootstrap(root); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(note, "overview.txt 不合本协议") {
		t.Fatalf("expected the manual migration hint, got %q", note)
	}
}

// ortg_target submits a plan: a document entry for a file that exists must land
// in the Target column and leave the current outline alone. Reusing the
// bootstrap split here would overwrite the very knowledge the plan sits next to.
func TestTargetDocumentNeverOverwritesOutline(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	cur := "a.go[9 E ST T]: Role:当前认知 | Uses:- | API:- | Constraints:-"
	if _, err := Update("a.go", cur); err != nil {
		t.Fatal(err)
	}
	doc := testHeader + "\n\n===计划 /" + filepath.ToSlash(root) + "/===\na.go[9 E ST T]: Role:计划中的样子 | Uses:- | API:- | Constraints:-\n"
	msg, err := Target("", "", false, doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "条目标纲要") {
		t.Fatalf("message must say the entries became target outlines: %q", msg)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := x.Row("a.go")
	if !ok {
		t.Fatal("row a.go missing")
	}
	if !strings.Contains(r.Outline, "当前认知") {
		t.Fatalf("current outline was overwritten by the plan: %q", r.Outline)
	}
	if !strings.Contains(r.Target, "计划中的样子") {
		t.Fatalf("plan did not land in the target column: %q", r.Target)
	}
}

// A legacy overview.txt must be renamed before the table exists, so the export
// hook cannot destroy it — on a failed import the migration steps are useless
// without the document they tell the model to read.
func TestBootstrapBacksUpLegacyOverview(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	legacy := "#旧索引，没有字典行，导入必然失败\n随便一行\n"
	os.WriteFile(filepath.Join(root, "overview.txt"), []byte(legacy), 0o644)
	t.Chdir(root)
	out, err := Scan()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "overview.txt.bak") {
		t.Fatalf("the migration note must name the backup: %s", out)
	}
	got, err := os.ReadFile(filepath.Join(root, "overview.txt.bak"))
	if err != nil {
		t.Fatalf("legacy document not backed up: %v", err)
	}
	if string(got) != legacy {
		t.Fatalf("backup is not the legacy bytes:\n%s", string(got))
	}
	// Nothing is left at overview.txt to pass for the outline.
	if _, err := os.Stat(filepath.Join(root, "overview.txt")); err == nil {
		t.Fatal("overview.txt must be moved aside, not kept or re-exported")
	}
	// A second migration attempt must not clobber the first backup.
	os.Remove(filepath.Join(root, "ortg.tsv"))
	os.WriteFile(filepath.Join(root, "overview.txt"), []byte("#另一份\n"), 0o644)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if first, _ := os.ReadFile(filepath.Join(root, "overview.txt.bak")); string(first) != legacy {
		t.Fatalf("second run clobbered the first backup: %q", string(first))
	}
	if second, err := os.ReadFile(filepath.Join(root, "overview.txt.bak.2")); err != nil ||
		string(second) != "#另一份\n" {
		t.Fatalf("second backup wrong: %v %q", err, string(second))
	}
}

// An edit made through the shell carries no file path, so the Edit|Write hook
// never sees it. The post hook must fall back to the table and name the files
// whose outline the edit invalidated — once per session, not on every command.
func TestPostHookReportsShellEdits(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("a.go", "a.go[9 E ST T]: Role:旧 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package a"), 0o644)
	Update("b.go", "b.go[9 E ST T]: Role:用 a | Uses:a.go | API:- | Constraints:-")
	in := base.HookInput{Cwd: root, SessionID: "s1", ToolName: "Bash"}
	if out := HookReply("post", in); out != "" {
		t.Fatalf("nothing changed yet, hook must stay silent: %q", out)
	}
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a // 用 Bash 改的"), 0o644)
	os.Chtimes(filepath.Join(root, "a.go"), time.Now().Add(2*time.Second), time.Now().Add(2*time.Second))
	out := HookReply("post", in)
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "ortg_update") {
		t.Fatalf("shell edit not reported: %q", out)
	}
	if !strings.Contains(out, fmt.Sprintf(impactNote, "a.go", "b.go(Uses)")) {
		t.Fatalf("a shell edit must ask for the impact check too: %q", out)
	}
	if again := HookReply("post", in); again != "" {
		t.Fatalf("same file must not be nagged twice in one session: %q", again)
	}
}

// A file read through the shell carries no file path either, so the Read hook
// never fires on it. Under a bypass-permissions convention the model reads
// with cat/sed almost exclusively, which used to leave skeleton rows with no
// notice at all: the stat-only sweep only ever looked at rows that already had
// an outline. The post hook must name the skeleton file the command mentions.
func TestPostHookReportsShellReadOfSkeleton(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "b.go"), []byte("package sub"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	in := base.HookInput{Cwd: root, SessionID: "s1", ToolName: "Bash"}
	in.Command = "grep -n handler sub/b.go"
	out := HookReply("post", in)
	if !strings.Contains(out, "sub/b.go") || !strings.Contains(out, "ortg_update") {
		t.Fatalf("shell read of a skeleton file not reported: %q", out)
	}
	if strings.Contains(out, "a.go") {
		t.Fatalf("a.go is a skeleton too but the command never named it: %q", out)
	}
	if again := HookReply("post", in); again != "" {
		t.Fatalf("same file must not be nagged twice in one session: %q", again)
	}
	// An absolute path names the same row; a look-alike name does not.
	in2 := base.HookInput{Cwd: root, SessionID: "s2", ToolName: "Bash"}
	in2.Command = "cat " + filepath.Join(root, "a.go")
	if out := HookReply("post", in2); !strings.Contains(out, "a.go") {
		t.Fatalf("absolute path not matched: %q", out)
	}
	in3 := base.HookInput{Cwd: root, SessionID: "s3", ToolName: "Bash"}
	in3.Command = "cat vendor/a.golden mya.go"
	if out := HookReply("post", in3); out != "" {
		t.Fatalf("look-alike names must not match: %q", out)
	}
	// Once the outline exists, reading the file again says nothing.
	if _, err := Update("sub/b.go", "b.go[9 E ST T]: Role:新 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	in4 := base.HookInput{Cwd: root, SessionID: "s4", ToolName: "Bash"}
	in4.Command = "grep -n handler sub/b.go"
	if out := HookReply("post", in4); out != "" {
		t.Fatalf("aligned file must stay silent: %q", out)
	}
}

// ortg_header sets only the fields it was given: a call that changes the
// ignore rules must not wipe the whitelist, and one that sets the whitelist
// must not wipe the ignore rules.
func TestHeaderKeepsUntouchedConfigFields(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header("", []string{"vendor"}, []string{"*.bak"}, []string{"vendor/keep.go"}, nil, []string{"*_test.go"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Header("", []string{"vendor", "node_modules"}, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := x.Config()
	if len(cfg.KeepFiles) != 1 || cfg.KeepFiles[0] != "vendor/keep.go" {
		t.Fatalf("keep_files wiped by a later ignore-rules call: %v", cfg.KeepFiles)
	}
	if len(cfg.IgnoreFiles) != 1 || cfg.IgnoreFiles[0] != "*.bak" {
		t.Fatalf("ignore_files wiped: %v", cfg.IgnoreFiles)
	}
	if len(cfg.IgnoreDirs) != 2 {
		t.Fatalf("ignore_dirs not updated: %v", cfg.IgnoreDirs)
	}
	if len(cfg.ObserveFiles) != 1 || cfg.ObserveFiles[0] != "*_test.go" {
		t.Fatalf("observe_files wiped: %v", cfg.ObserveFiles)
	}
}

// The observe role is the middle tier: fingerprinted so drift still surfaces,
// never asked for an outline, never rendered into the document, and hooks stay
// silent for it. Acknowledging drift is ortg_align, not a gate.
func TestObserveRole(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, "a_test.go"), []byte("package a // v1"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header("", nil, nil, nil, nil, []string{"*_test.go"}, nil); err != nil {
		t.Fatal(err)
	}
	out, err := Scan()
	if err != nil {
		t.Fatal(err)
	}
	// Observed files are counted, never listed as missing an outline.
	if strings.Contains(out, "a_test.go") && strings.Contains(out, "无纲要") &&
		strings.Contains(strings.SplitN(out, "\n", 3)[1], "a_test.go") {
		t.Fatalf("observed file must not be demanded an outline:\n%s", out)
	}
	if !strings.Contains(out, "观察 1") {
		t.Fatalf("observed file must be counted:\n%s", out)
	}
	// A changed fingerprint is drift, and it is reported by path.
	os.WriteFile(filepath.Join(root, "a_test.go"), []byte("package a // v2"), 0o644)
	if out, err = Scan(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "观察变动 1: a_test.go") {
		t.Fatalf("drift on an observed file must be reported:\n%s", out)
	}
	// ortg_align acknowledges it; nothing is blocked meanwhile.
	if _, err := Align("paths", []string{"a_test.go"}); err != nil {
		t.Fatal(err)
	}
	if out, err = Scan(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "观察变动") {
		t.Fatalf("align must acknowledge the drift:\n%s", out)
	}
	// Writing an outline for it is refused, with the way out named.
	if _, err := Update("a_test.go", "a_test.go[3 T ST T]: Role:x | Uses:- | API:- | Constraints:-"); err == nil ||
		!strings.Contains(err.Error(), "keep_files") {
		t.Fatalf("Update on an observed file must be refused and name keep_files: %v", err)
	}
	// It must not reach the rendered document either.
	doc, err := Overview("", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, "a_test.go[") || strings.Contains(strings.SplitN(doc, "## 对账报告", 2)[0], "a_test.go") {
		t.Fatalf("observed file must not be rendered into the outline document:\n%s", doc)
	}
	// The hook stays silent for it.
	if got := HookReply("pre", base.HookInput{Cwd: root, SessionID: "s1", FilePath: filepath.Join(root, "a_test.go")}); got != "" {
		t.Fatalf("hook must stay silent on an observed file: %q", got)
	}
}

// The three choices must reach the user through the UserPromptSubmit hook: the
// host does not reliably render a resume-time SessionStart message.
func TestPromptHookCarriesChoices(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	if _, err := base.Create(root, testHeader); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	in := base.HookInput{Cwd: root, SessionID: "s1"}
	ctx, user := HookReplyFull("session", in)
	if strings.Contains(user, decisionPrompt) {
		t.Fatalf("session line stays a one-liner, got %q", user)
	}
	if !strings.Contains(ctx, decisionText) {
		t.Fatalf("model context must carry the mapping, got %q", ctx)
	}
	if strings.Contains(ctx, decisionPrompt) {
		t.Fatal("model must not be handed the option list to relay")
	}
	if c, u := HookReplyFull("prompt", in); u != decisionPrompt || c != "" {
		t.Fatalf("first prompt must show the choices only to the user, got %q / %q", c, u)
	}
	if _, u := HookReplyFull("prompt", in); u != "" {
		t.Fatalf("choices show once per session, got %q", u)
	}
	// A resume must show them again without clearing the file-level markers.
	if !base.InjectOnce("s1", "a.go") {
		t.Fatal("marker setup failed")
	}
	HookReplyFull("session", base.HookInput{Cwd: root, SessionID: "s1", Source: "resume"})
	if _, u := HookReplyFull("prompt", in); u != decisionPrompt {
		t.Fatalf("resume must show the choices again, got %q", u)
	}
	if base.InjectOnce("s1", "a.go") {
		t.Fatal("resume must keep the file-level markers")
	}
	if _, err := Update("a.go", "a.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	if _, u := HookReplyFull("prompt", base.HookInput{Cwd: root, SessionID: "s2"}); u != "" {
		t.Fatalf("nothing to decide, nothing shown, got %q", u)
	}
}

// The Codex host wraps MCP calls in functions.exec and silently truncates an
// oversized tool result, so the rules it receives must carry the directive that
// raises the budget. Claude does not understand that directive and must not be
// shown it.
func TestRulesCarryCodexBudgetOnlyUnderCodex(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	in := base.HookInput{Cwd: root, SessionID: "s1"}

	t.Setenv("ORTG_AGENT", "codex")
	for _, ev := range []string{"session", "subagent"} {
		out := HookReply(ev, in)
		if !strings.Contains(out, "@exec") || !strings.Contains(out, "max_output_tokens") {
			t.Fatalf("codex %s rules missing the budget directive: %q", ev, out)
		}
	}

	t.Setenv("ORTG_AGENT", "claude")
	for _, ev := range []string{"session", "subagent"} {
		if out := HookReply(ev, in); strings.Contains(out, "@exec") {
			t.Fatalf("claude %s rules must not carry the codex directive: %q", ev, out)
		}
	}
}

// A bare call is the header, the folder outlines and the module directory;
// modules come on request and only what the context lacks is sent; a repeat
// is the short reply; force resends; compact and clear start over.
func TestOverviewSendsOnce(t *testing.T) {
	defer func(l hostLimits) { claudeLimits = l }(claudeLimits)
	claudeLimits.whole = 10 // exercise the module list on a tiny repository
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"a.go", "b.go", "sub/c.go"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][2]string{{"a.go", "a.go[5 L CL T]: Role:甲 | Uses:- | API:- | Constraints:-"}, {"b.go", "b.go[5 L ST T]: Role:乙 | Uses:- | API:- | Constraints:-"}, {"sub/c.go", "c.go[5 L CL T]: Role:丙 | Uses:- | API:- | Constraints:-"}} {
		if _, err := Update(u[0], u[1]); err != nil {
			t.Fatal(err)
		}
	}
	get := func(module string, force bool) string {
		t.Helper()
		doc, err := Overview(module, force)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	doc := get("", false)
	if !strings.Contains(doc, "#A层级") || !strings.Contains(doc, "- CL  2 个文件") || strings.Contains(doc, "a.go[") || !strings.HasSuffix(doc, overviewModulesFooter) {
		t.Fatalf("a bare call is header plus module directory:\n%s", doc)
	}
	doc = get("CL", false)
	if !strings.Contains(doc, "a.go[5 L CL T]") || !strings.Contains(doc, "c.go[5 L CL T]") || strings.Contains(doc, "b.go[") || strings.Contains(doc, "#A层级") {
		t.Fatalf("module fetch carries that module and no header again:\n%s", doc)
	}
	if !strings.HasSuffix(doc, fmt.Sprintf(overviewPartFooter, "模块 CL ")) {
		t.Fatalf("module fetch closes with the part footer:\n%s", doc)
	}
	if doc := get("", false); !strings.Contains(doc, "已在上下文 2") || strings.Contains(doc, "#A层级") {
		t.Fatalf("the directory shows what the context holds, header not repeated:\n%s", doc)
	}
	doc = get("*", false)
	if !strings.Contains(doc, "b.go[5 L ST T]") || strings.Contains(doc, "a.go[") || !strings.Contains(doc, fmt.Sprintf(overviewIncrementNote, 1, 2)) ||
		!strings.HasSuffix(doc, fmt.Sprintf(overviewPartFooter, "整份")) {
		t.Fatalf("module=* sends the rest of the whole outline, past the budget:\n%s", doc)
	}
	if doc := get("", false); !strings.Contains(doc, fmt.Sprintf(overviewAlreadyRead, "整份纲要", 3)) || !strings.HasSuffix(doc, overviewShortFooter) {
		t.Fatalf("with everything sent a bare call is the short reply:\n%s", doc)
	}
	if doc := get("*", false); !strings.Contains(doc, fmt.Sprintf(overviewAlreadyRead, "整份纲要", 3)) {
		t.Fatalf("module=* with everything sent is short:\n%s", doc)
	}
	if _, err := Update("b.go", "b.go[5 L ST T]: Role:乙二 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	if doc := get("ST", false); !strings.Contains(doc, "Role:乙二") {
		t.Fatalf("a changed entry is sent again:\n%s", doc)
	}
	if doc := get("CL", true); !strings.Contains(doc, "a.go[5 L CL T]") || !strings.Contains(doc, "#A层级") {
		t.Fatalf("force resends, header included:\n%s", doc)
	}
	HookReply("session", base.HookInput{Cwd: root, SessionID: "s1", Source: "resume"})
	if doc := get("CL", false); !strings.HasSuffix(doc, overviewShortFooter) {
		t.Fatal("resume must not reset what was sent")
	}
	for _, src := range []string{"compact", "clear"} {
		time.Sleep(10 * time.Millisecond)
		HookReply("session", base.HookInput{Cwd: root, SessionID: "s1", Source: src})
		if doc := get("", false); !strings.Contains(doc, "#A层级") || !strings.Contains(doc, "已在上下文 0") {
			t.Fatalf("after %s everything starts over:\n%s", src, doc)
		}
	}
	time.Sleep(10 * time.Millisecond)
	get("CL", false)
	if got := HookReply("compact", base.HookInput{Cwd: root, SessionID: "s1"}); got != "" {
		t.Fatalf("PostCompact must stay silent (Codex rejects its context output): %q", got)
	}
	if doc := get("CL", false); !strings.Contains(doc, "a.go[5 L CL T]") {
		t.Fatal("PostCompact must reset what was sent")
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "d.go"), []byte("package d"), 0o644)
	t.Chdir(other)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if doc := get("", false); !strings.Contains(doc, "#A层级") {
		t.Fatal("each repository keeps its own record")
	}
}

// The session-start hook only points the model at ortg_overview; the outline
// itself goes through MCP, where the host limits are raised.
func TestSessionPointsToOverview(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	out := HookReply("session", base.HookInput{Cwd: root, SessionID: "p1", Source: "startup"})
	if !strings.Contains(out, sessionFetchNote) || strings.Contains(out, "#A层级") {
		t.Fatalf("session start must point to ortg_overview without carrying the outline:\n%s", out)
	}
}

// A fetch over the host limit is sent anyway, led by the split reminder, and
// not recorded as sent.
func TestOverviewTooLarge(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.go", "a.go[5 L CL T]: Role:甲 | Uses:- | API:- | Constraints:-")
	defer func(l hostLimits) { claudeLimits = l }(claudeLimits)
	claudeLimits.max, claudeLimits.margin = 30, 0
	doc, _ := Overview("CL", false)
	if !strings.HasPrefix(doc, "ortg: 模块 CL 的纲要约") || !strings.Contains(doc, "模块划分不够细") || !strings.Contains(doc, "a.go[5 L CL T]") {
		t.Fatalf("an oversized fetch leads with the split reminder and still sends the body:\n%s", doc)
	}
	claudeLimits.max, claudeLimits.margin = 500000, 20000
	if doc, _ := Overview("CL", false); !strings.Contains(doc, "a.go[5 L CL T]") || strings.Contains(doc, "模块划分不够细") {
		t.Fatal("an oversized send must not be recorded as sent")
	}
}

// Sizes follow the host's own meter: Claude Code's JS string length (UTF-16
// code units) and Codex's token estimate, UTF-8 bytes / 4 rounded up; the
// module list and the budgets speak the host's unit, and the Codex rule asks
// for exactly the budget ortg plans with and detects a cut middle.
func TestHostSize(t *testing.T) {
	t.Setenv("ORTG_AGENT", "claude")
	if n := outlineSize("汉a😀"); n != 4 {
		t.Fatalf("Claude counts UTF-16 code units: got %d, want 4", n)
	}
	t.Setenv("ORTG_AGENT", "codex")
	for s, want := range map[string]int{"汉a😀": 2, "汉": 1, "abcde": 2, "": 0} {
		if n := outlineSize(s); n != want {
			t.Fatalf("Codex counts ceil(bytes/4): %q got %d, want %d", s, n, want)
		}
	}
	for _, w := range []string{fmt.Sprintf(`"max_output_tokens": %d`, codexLimits.max), fmt.Sprintf("tool_output_token_limit = %d", codexLimits.max), "tokens truncated"} {
		if !strings.Contains(codexBudgetRule, w) {
			t.Fatalf("codexBudgetRule must carry %q", w)
		}
	}
	// the session that builds a table has no hook context: the rule rides
	// on the ortg_overview description, under Codex only
	if d := toolDescription("ortg_overview"); !strings.HasSuffix(d, codexBudgetRule) {
		t.Fatal("under Codex the ortg_overview description must carry codexBudgetRule")
	}
	if d := toolDescription("ortg_status"); d != toolDescriptions["ortg_status"] {
		t.Fatal("only ortg_overview carries codexBudgetRule")
	}
	t.Setenv("ORTG_AGENT", "claude")
	if strings.Contains(toolDescription("ortg_overview"), codexBudgetRule) {
		t.Fatal("Claude must not get codexBudgetRule")
	}
	t.Setenv("ORTG_AGENT", "codex")
	defer func(l hostLimits) { codexLimits = l }(codexLimits)
	codexLimits.whole = 10
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	// mostly CJK, so the estimate and bytes/4 differ
	line := "a.go[5 L CL T]: Role:" + strings.Repeat("甲乙丙", 20) + " | Uses:- | API:- | Constraints:-"
	if _, err := Update("a.go", line); err != nil {
		t.Fatal(err)
	}
	doc, _ := Overview("", false)
	if !strings.Contains(doc, "超过单次返回上限减余量（10 token）") || !strings.Contains(doc, fmt.Sprintf("约 %d token  已在上下文", estTokens(line))) || !strings.Contains(doc, "整份纲要估约 ") || strings.Contains(doc, "token、估约") {
		t.Fatalf("under Codex the module list speaks tokens:\n%s", doc)
	}
}

// A folder outline is an ordinary row for a directory: tagged with the module
// that uses it, sent with that module, counted in the module list, injected
// before an edit under it, and never stale or missing.
func TestFolderRow(t *testing.T) {
	defer func(l hostLimits) { claudeLimits = l }(claudeLimits)
	claudeLimits.whole = 10 // exercise the module list on a tiny repository
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"a.go", "vendored/lib/x.go", "node_modules/p/i.js"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header("", []string{"node_modules"}, nil, nil, []string{"vendored"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("a.go", "a.go[5 L CL T]: Role:甲 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("vendored", "vendored/[3 L CL T]: Role:第三方库foo | Uses:- | API:- | Constraints:只读不改"); err != nil {
		t.Fatalf("an observed directory takes a folder outline: %v", err)
	}
	if _, err := Update("node_modules/", "node_modules/[3 L ST T]: Role:npm依赖 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatalf("an excluded directory takes a folder outline: %v", err)
	}
	if _, err := Update("vendored", "vendored[3 L CL T]: Role:x | Uses:- | API:- | Constraints:-"); err == nil {
		t.Fatal("a directory outline without the trailing / must be refused")
	}
	if _, err := Update("a.go", "a.go/[3 L CL T]: Role:x | Uses:- | API:- | Constraints:-"); err == nil {
		t.Fatal("a file outline with a trailing / must be refused")
	}
	out, err := Scan()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "对齐 3") || strings.Contains(out, "vendored/lib/x.go") && strings.Contains(out, "无纲要") {
		t.Fatalf("folder rows stay aligned and their files ask for nothing:\n%s", out)
	}
	doc, _ := Overview("", false)
	if !strings.Contains(doc, "- CL  2 个文件") || !strings.Contains(doc, "- ST  1 个文件") || strings.Contains(doc, "vendored/[") {
		t.Fatalf("the module list counts folder rows, the bare call sends no entries:\n%s", doc)
	}
	doc, _ = Overview("CL", false)
	if !strings.Contains(doc, "vendored/[3 L CL T]: Role:第三方库foo") || !strings.Contains(doc, "a.go[5 L CL T]") || strings.Contains(doc, "node_modules/[") {
		t.Fatalf("a folder outline travels with its module:\n%s", doc)
	}
	pre := HookReply("pre", base.HookInput{Cwd: root, SessionID: "f1", FilePath: filepath.Join(root, "vendored/lib/x.go")})
	if !strings.Contains(pre, "属于目录纲要 vendored/") || !strings.Contains(pre, "只读不改") {
		t.Fatalf("an edit under a folder outline is shown that outline: %q", pre)
	}
	if got := HookReply("pre", base.HookInput{Cwd: root, SessionID: "f1", FilePath: filepath.Join(root, "vendored/lib/x.go")}); got != "" {
		t.Fatalf("once per session and file: %q", got)
	}
}

// Up to the whole budget a bare call sends the whole outline, still
// incrementally: after a module was fetched, only the rest goes.
func TestOverviewWholeUnderBudget(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"a.go", "b.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.go", "a.go[5 L CL T]: Role:甲 | Uses:- | API:- | Constraints:-")
	Update("b.go", "b.go[5 L ST T]: Role:乙 | Uses:- | API:- | Constraints:-")
	if doc, _ := Overview("CL", false); !strings.Contains(doc, "a.go[5 L CL T]") {
		t.Fatalf("module fetch:\n%s", doc)
	}
	doc, _ := Overview("", false)
	if !strings.Contains(doc, "b.go[5 L ST T]") || strings.Contains(doc, "a.go[") || strings.Contains(doc, "模块清单") || !strings.HasSuffix(doc, fmt.Sprintf(overviewPartFooter, "整份")) {
		t.Fatalf("under the budget a bare call sends the rest of the whole outline:\n%s", doc)
	}
}

// After an edit the reminder names the files whose R depends on the edited
// one, and always sends the model to the outline for semantic ties.
func TestPostEditNamesDependents(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"util.go", "main.go", "other.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("util.go", "util.go[5 L CL T]: Role:工具 | Uses:- | API:Greet | Constraints:-")
	Update("main.go", "main.go[9 E CL T]: Role:入口 | Uses:util.go | API:main | Constraints:调 Greet")
	Update("other.go", "other.go[5 L CL T]: Role:无关 | Uses:- | API:- | Constraints:-")
	got := HookReply("post", base.HookInput{Cwd: root, SessionID: "d1", ToolName: "Edit", FilePath: filepath.Join(root, "util.go")})
	if !strings.Contains(got, fmt.Sprintf(impactNote, "util.go", "main.go(Uses)")) || strings.Contains(got, "other.go") {
		t.Fatalf("post-edit must name the dependents from the outline:\n%s", got)
	}
	if got := HookReply("post", base.HookInput{Cwd: root, SessionID: "d1", ToolName: "Edit", FilePath: filepath.Join(root, "other.go")}); !strings.Contains(got, fmt.Sprintf(impactNote, "other.go", "无")) {
		t.Fatalf("no candidates still asks for the semantic check:\n%s", got)
	}
}

// Rebuilding a lost table must not clobber the backup the user may restore
// from: the import's save would back up the fresh template over it. With no
// backup before, none is left behind — a template copy only misleads.
func TestBootstrapKeepsTableBackup(t *testing.T) {
	legacy := testHeader + "\n\n===根 /x/===\na.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-\n"
	for _, prev := range []string{"用户的旧表备份\n", ""} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
		os.WriteFile(filepath.Join(root, "overview.txt"), []byte(legacy), 0o644)
		bak := filepath.Join(root, "ortg.tsv.bak")
		if prev != "" {
			os.WriteFile(bak, []byte(prev), 0o644)
		}
		if _, _, err := openOrBootstrap(root); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(bak)
		if prev != "" && string(got) != prev {
			t.Fatalf("existing ortg.tsv.bak clobbered: %q", got)
		}
		if prev == "" && err == nil {
			t.Fatalf("template backup left behind: %q", got)
		}
	}
}

// The gate refuses the first code read of a context that has not loaded the
// outline, then reminds once per file; once a module is loaded it asks, once
// per module, for the modules the context still lacks; a reset re-arms it.
func TestGate(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Chdir(t.TempDir())
	if got, _ := Gate("Read", "a.go", "", "", ""); got != "" {
		t.Fatalf("no table, no gate: %q", got)
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	for _, p := range []string{"a.go", "b.go", "sub/c.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.go", "a.go[7 L ST T]: Role:存储 | Uses:- | API:- | Constraints:-")
	Update("b.go", "b.go[7 L CL T]: Role:命令 | Uses:- | API:- | Constraints:-")
	Update("sub/c.go", "c.go[7 L CL T]: Role:命令二 | Uses:- | API:- | Constraints:-")
	abs := func(p string) string { return filepath.Join(root, p) }
	deny := `"permissionDecision":"deny"`
	remind := `"additionalContext"`
	steps := []struct {
		tool, path, dir, cmd, agent, want string
	}{
		{"Bash", "", "", "ls -la && git status", "", ""},
		{"Read", "/etc/hosts", "", "", "", ""},
		{"Read", abs("ortg.tsv"), "", "", "", ""},
		{"Read", abs("b.go"), "", "", "sub-agent", ""},
		{"Read", abs("a.go"), "", "", "", deny},
		{"Read", abs("a.go"), "", "", "", remind},
		{"Read", abs("a.go"), "", "", "", ""},
		{"Grep", "", "", "", "", "本仓库代码"},
		{"Bash", "", "", "grep -rn Save .", "", ""},
		{"Bash", "", "", "sed -n 1,5p b.go", "", remind},
	}
	for i, s := range steps {
		got, _ := Gate(s.tool, s.path, s.dir, s.cmd, s.agent)
		if (s.want == "" && got != "") || !strings.Contains(got, s.want) {
			t.Fatalf("step %d %s %s%s: want %q in %q", i, s.tool, s.path, s.cmd, s.want, got)
		}
	}
	if _, err := Overview("ST", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := Gate("Read", abs("a.go"), "", "", ""); got != "" {
		t.Fatalf("a.go's module is loaded, gate must be silent: %q", got)
	}
	if got, _ := Gate("Read", abs("b.go"), "", "", ""); !strings.Contains(got, "module=CL") {
		t.Fatalf("b.go's module CL is not loaded, gate must ask for it: %q", got)
	}
	if got, _ := Gate("Bash", "", "", "cat sub/c.go", ""); got != "" {
		t.Fatalf("module CL was asked for once already: %q", got)
	}
	Overview("", false)
	if got, _ := Gate("Read", abs("sub/c.go"), "", "", ""); got != "" {
		t.Fatalf("whole outline loaded, gate must be silent on reads: %q", got)
	}
	// An entry whose tag yields no module cannot be fetched by module: the
	// gate says the entry is broken and asks for it to be rewritten, once.
	os.WriteFile(abs("d.go"), []byte("package a"), 0o644)
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.SetOutline("d.go", "d.go[9]: Role:坏标签 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Gate("Read", abs("d.go"), "", "", ""); got != base.PreToolDecision(fmt.Sprintf(gateBadOutline, "d.go"), false) {
		t.Fatalf("an entry without a module must be reported as broken: %q", got)
	}
	if got, _ := Gate("Read", abs("d.go"), "", "", ""); got != "" {
		t.Fatalf("the broken entry is reported once: %q", got)
	}
	if doc, _ := Overview("-", false); !strings.Contains(doc, "d.go[9]") || strings.Contains(doc, "a.go[") {
		t.Fatalf("module - fetches the rows without a B tag: %q", doc)
	}
	time.Sleep(10 * time.Millisecond)
	base.MarkOverview(root, "reset", "")
	if got, _ := Gate("Read", abs("a.go"), "", "", ""); !strings.Contains(got, deny) {
		t.Fatalf("after a reset the context lost the outline, gate must refuse again: %q", got)
	}
}

// Only a command that searches files counts as reading code: a grep-family
// command naming a path, or rg/ag/ack and grep -r searching the working
// directory; a grep that filters a pipe does not.
func TestCodeSearch(t *testing.T) {
	cases := map[string]bool{
		"ortg mcp | grep -o version": false,
		// the command that tripped the first version of the gate
		`SP=/tmp/sp; ls ~/.claude/plugins/cache/ortg/ortg/; echo '{"jsonrpc":"2.0","id":0}' | ortg mcp | grep -o '"version":"[^"]*"'; $SP/mkgate.sh $SP/tG40`: false,
		`echo '{"id":0}' | ortg mcp | grep -o '"v":"x"'`: false,
		"git log --oneline | grep -c fix":                false,
		"ps aux | rg ortg":                               false,
		"grep 'a b'":                                     false,
		"echo grep foo .":                                false,
		"grep -rn Save .":                                true,
		"grep -rn Save":                                  true,
		"grep Save a.go":                                 true,
		`grep 'two words' base/`:                         true,
		"grep -e foo -e bar src/":                        true,
		"grep -rne foo":                                  true,
		"grep -e foo":                                    false,
		"rg Greet":                                       true,
		"cd sub && rg -n Greet":                          true,
		"git grep -n Greet":                              true,
		"find . -name '*.go' | xargs grep -l Greet":      true,
		"cat a.go | grep -n func":                        false,
		"grep foo < a.go":                                true,
		"grep -rn x . 2>/dev/null | head":                true,
		"grep -c x 2>/dev/null":                          false,
		"grep -c x 2>&1 a.go":                            true,
		"grep x > out.txt":                               false,
		"grep -c x <<< \"$s\"":                           false,
	}
	for cmd, want := range cases {
		if got := codeSearch(cmd); got != want {
			t.Errorf("codeSearch(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// Codex writes files through apply_patch: the gate reads the file headers,
// so a patch touching repository code counts, one outside it does not.
func TestGateApplyPatch(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	outside := "*** Begin Patch\n*** Update File: /etc/x.conf\n@@\n-a\n+b\n*** End Patch\n"
	if got, _ := Gate("apply_patch", "", "", outside, ""); got != "" {
		t.Fatalf("a patch outside the repository touches no code: %q", got)
	}
	inside := "*** Begin Patch\n*** Update File: a.go\n@@\n-package a\n+package b\n*** End Patch\n"
	if got, _ := Gate("apply_patch", "", "", inside, ""); !strings.Contains(got, `"permissionDecision":"deny"`) {
		t.Fatalf("a patch to a.go before the outline is loaded must be refused: %q", got)
	}
}

// A document written before the rename imports its "#未来:" lines once
// migrate.Document has renamed them, and the outline shows "#目标:".
func TestTargetMarkerRename(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	doc := testHeader + "\n\n===根 " + filepath.ToSlash(root) + "/===\na.go[7 L ST T]: Role:旧 | Uses:- | API:- | Constraints:-\n#未来: a.go[7 L ST T]: Role:新 | Uses:- | API:- | Constraints:-\n"
	if _, err := Target("", "", false, migrate.Document(doc)); err != nil {
		t.Fatal(err)
	}
	got, err := Overview("", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "#目标: a.go[7 L ST T]: Role:新") || strings.Contains(got, "#未来:") {
		t.Fatalf("legacy #未来: must import and render as #目标:\n%s", got)
	}
}

// migrate.Table rewrites the old template's rule, format and tag lines to the
// new ones; those copies must stay the lines the header template carries.
func TestLegacyRuleLineMatchesTemplate(t *testing.T) {
	root := t.TempDir()
	old := "#ORTG-TSV: 1\n" + testHeader + "\n#未来规则：条目下一行 #未来: 是未来纲要；#未来:删除 计划删除；#计划新建 文件尚不存在\n" +
		"#纲要格式：文件名[ABCDE]: F:功能 | R:关联 | A:接口 | S:简述 ；文件名不带路径；四字段必填,无内容写-\n" +
		"#【标签ABCDE】紧凑连写,首个数字是C重要度的切分锚点,B与D符号禁用数字,D可多选连写可为空\n{}\n" +
		"path\tcrc32\tignored\tdeleted\tmtime\tmodule\toutline\toutline_time\tfuture_outline\tfuture_time\tfuture_delete\n"
	os.WriteFile(filepath.Join(root, base.TableName), []byte(old), 0o644)
	x, err := openTable(root)
	if err != nil {
		t.Fatal(err)
	}
	line := func(h, prefix string) string {
		for _, l := range strings.Split(h, "\n") {
			if strings.HasPrefix(l, prefix) {
				return l
			}
		}
		return ""
	}
	for _, prefix := range []string{"#目标规则", "#纲要格式", "#【标签"} {
		if got, want := line(x.Header(), prefix), line(headerTemplate, prefix); got == "" || got != want {
			t.Fatalf("migrated line %q differs from the template's %q", got, want)
		}
	}
}

// A shell command is judged where it runs: cd and pushd move it, so a search
// or a file name in another repository touches nothing here, while one that
// comes back into this repository, or into a subdirectory, still does.
func TestShellTargetsFollowCd(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	other := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	for _, p := range []string{"a.go", "README.md", "sub/c.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"cd " + other + " && grep -m1 '^path' ortg.tsv":      "",
		"cd $R && git status --short | head -3; grep x a.go": "",
		"cd " + other + " && cat README.md":                  "",
		"grep -n x /etc/hosts":                               "",
		"cd .. && grep -rn x":                                "",
		"pushd " + other + "; rg foo; popd; rg bar":          ".",
		"pushd " + other + "; rg foo; popd":                  "",
		"cd sub && rg -n foo":                                ".",
		"cd " + other + "; cd " + root + " && grep -rn x .":  ".",
		"cat a.go": "a.go",
		"cd " + other + " && cat README.md; cd " + root + "; cat a.go": "a.go",
	}
	for cmd, want := range cases {
		touched, _ := gateTargets(x, "Bash", "", "", cmd)
		got := strings.Join(touched, ",")
		if got != want {
			t.Errorf("%q: targets %q, want %q", cmd, got, want)
		}
	}
}

// Writing is gated on a plan: a file may be written only once its target
// outline is written and ortg_review has seen the same plan twice in a row;
// every write outside such a plan is refused, and the unlock outlives the
// target (ortg_update clears it) until the context resets.
// changedFields cuts outline lines at their field labels: a field whose text
// holds " | " must not shift the fields after it, which once made a changed
// line read as unchanged.
func TestChangedFields(t *testing.T) {
	cur := "a.go[7 L S T]: Role:存储 | Uses:- | API:Save | Constraints:格式 Role:.. | Uses:.. 四段必齐"
	for target, want := range map[string]string{
		cur: "目标与现在的纲要一样",
		"a.go[7 L S T]: Role:存储 | Uses:- | API:Save | Constraints:格式 Role:.. | Uses:.. 五段必齐":    "改 Constraints",
		"a.go[7 L S T]: Role:存储 | Uses:b.go | API:Save | Constraints:格式 Role:.. | Uses:.. 四段必齐": "改 Uses",
		"a.go[8 L S T]: Role:存储 | Uses:- | API:Store | Constraints:格式 Role:.. | Uses:.. 四段必齐":   "改 标签/Role、API",
	} {
		if got := changedFields(cur, target); got != want {
			t.Errorf("changedFields(.., %q) = %q, want %q", target, got, want)
		}
	}
}

func TestWriteGatePlan(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"a.go", "b.go", "c.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.go", "a.go[7 L ST T]: Role:存储 | Uses:- | API:Save | Constraints:-")
	Update("b.go", "b.go[7 L CL T]: Role:命令 | Uses:a.go | API:Run | Constraints:-")
	Update("c.go", "c.go[7 L CL T]: Role:无关 | Uses:- | API:- | Constraints:-")
	a := filepath.Join(root, "a.go")
	deny := `"permissionDecision":"deny"`
	gate := func(tool, path, cmd string) string { got, _ := Gate(tool, path, "", cmd, ""); return got }
	if got := gate("Edit", a, ""); !strings.Contains(got, deny) || !strings.Contains(got, "ortg_overview") {
		t.Fatalf("a write before the outline is loaded must be refused: %q", got)
	}
	Overview("", false)
	c := filepath.Join(root, "c.go")
	// One file outside any plan is a single-file change: it passes, with a
	// reminder the first time.
	if got := gate("Edit", a, ""); strings.Contains(got, deny) || !strings.Contains(got, "单文件改动") {
		t.Fatalf("a single-file change passes with the reminder: %q", got)
	}
	if got := gate("Bash", "", "sed -i 's/Save/Store/' a.go"); got != "" {
		t.Fatalf("further writes to the same file pass silently: %q", got)
	}
	// A second unplanned file while the first awaits its outline is a
	// multi-file change: refused, by tool and by shell alike.
	for _, w := range []struct{ tool, path, cmd string }{{"Edit", c, ""}, {"Bash", "", "sed -i 's/x/y/' c.go"}} {
		if got := gate(w.tool, w.path, w.cmd); !strings.Contains(got, deny) || !strings.Contains(got, "多文件改动") || !strings.Contains(got, "a.go、c.go") {
			t.Fatalf("%s of a second unplanned file must be refused: %q", w.tool, got)
		}
	}
	if got := gate("Bash", "", "perl -pi -e 's/x/y/' b.go c.go"); !strings.Contains(got, deny) {
		t.Fatalf("one command writing two unplanned files is a multi-file change: %q", got)
	}
	for _, cmd := range []string{"cat a.go", "echo hi > /tmp/x", "python3 - <<'EOF'\nrm -rf a.go\nEOF"} {
		if got := gate("Bash", "", cmd); got != "" {
			t.Fatalf("%q writes nothing here: %q", cmd, got)
		}
	}
	// Submitting the outline closes the single-file change.
	Update("a.go", "a.go[7 L ST T]: Role:存储 | Uses:- | API:Save | Constraints:-")
	if got := gate("Edit", c, ""); strings.Contains(got, deny) || !strings.Contains(got, "单文件改动") {
		t.Fatalf("after ortg_update another single-file change passes: %q", got)
	}
	Update("c.go", "c.go[7 L CL T]: Role:无关 | Uses:- | API:- | Constraints:-")
	Target("a.go", "a.go[7 L ST T]: Role:存储 | Uses:- | API:Store | Constraints:-", false, "")
	if got := gate("Edit", a, ""); !strings.Contains(got, "还没核对到收敛") {
		t.Fatalf("a target not yet reviewed must be refused as such: %q", got)
	}
	first, _ := Review()
	if !strings.Contains(first, "改 API") || !strings.Contains(first, "b.go（Uses 依赖 a.go）") || !strings.Contains(first, "再调用一次 ortg_review") {
		t.Fatalf("first review must show the change, the dependent without a target and ask again:\n%s", first)
	}
	if got := gate("Edit", a, ""); !strings.Contains(got, deny) {
		t.Fatalf("one review is not a fixpoint: %q", got)
	}
	second, _ := Review()
	if !strings.Contains(second, "已收敛") {
		t.Fatalf("second review of the same plan must converge:\n%s", second)
	}
	if got := gate("Edit", a, ""); got != "" {
		t.Fatalf("a converged plan unlocks its file: %q", got)
	}
	if got := gate("Bash", "", "sed -i 's/Save/Store/' a.go"); got != "" {
		t.Fatalf("a shell write to a planned file must pass: %q", got)
	}
	if got := gate("Edit", c, ""); !strings.Contains(got, "单文件改动") {
		t.Fatalf("one file beside a converged plan is a single-file change: %q", got)
	}
	if got := gate("Edit", filepath.Join(root, "b.go"), ""); !strings.Contains(got, deny) || !strings.Contains(got, "b.go、c.go") {
		t.Fatalf("a second file outside the plan is refused: %q", got)
	}
	Update("a.go", "a.go[7 L ST T]: Role:存储 | Uses:- | API:Store | Constraints:-")
	if got := gate("Edit", a, ""); got != "" {
		t.Fatalf("the unlock outlives the target ortg_update cleared: %q", got)
	}
	time.Sleep(10 * time.Millisecond)
	base.MarkOverview(root, "reset", "")
	if got := gate("Edit", a, ""); !strings.Contains(got, deny) {
		t.Fatalf("after a reset the plan must be made and checked again: %q", got)
	}
}

// Shell writes: what a command writes, creates or removes in the repository.
func TestShellWrites(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	for _, p := range []string{"a.go", "b.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	inRepo := func(p string) (string, bool) { return base.Rel(root, p) }
	cases := map[string]string{
		"sed -i 's/a/b/' a.go":                          "a.go",
		"sed 's/a/b/' a.go":                             "",
		"perl -pi -e 's/x/y/' a.go b.go":                "a.go,b.go",
		"cat > new.go <<'EOF'\npackage x\nrm a.go\nEOF": "new.go",
		"python3 - <<'EOF'\nrm -rf a.go\nEOF\nrm b.go":  "b.go",
		"cp a.go c.go":                                  "c.go",
		"mv a.go sub":                                   "a.go,sub/a.go",
		"rm -f a.go missing.go":                         "a.go",
		"echo x 2>&1 >> a.go":                           "a.go",
		"go test ./... 2>/dev/null":                     "",
		"cd /tmp && sed -i 's/a/b/' " + root + "/b.go":  "b.go",
		"truncate -s 0 a.go":                            "a.go",
		"tee b.go < a.go":                               "b.go",
	}
	for cmd, want := range cases {
		_, w := shellTargets(cmd, root, inRepo, func(string) []string { return nil })
		if got := strings.Join(w, ","); got != want {
			t.Errorf("%q: writes %q, want %q", cmd, got, want)
		}
	}
}

// The session hook upgrades a table an older ORTG wrote before it reconciles;
// the read-only hooks leave it as it is.
func TestSessionUpgradesOldTable(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	old := "#ORTG-TSV: 1\n" + testHeader + "\n{}\n" +
		"path\tcrc32\tignored\tdeleted\tmtime\tmodule\toutline\toutline_time\tfuture_outline\tfuture_time\tfuture_delete\n" +
		"a.go\t00000000\t0\t0\t\t\ta.go[LST7T]: F:a | R:- | A:- | S:-\t\t\t\t0\n"
	p := filepath.Join(root, base.TableName)
	os.WriteFile(p, []byte(old), 0o644)
	HookReply("post", base.HookInput{ToolName: "Edit", FilePath: filepath.Join(root, "a.go"), Cwd: root})
	if b, _ := os.ReadFile(p); string(b) != old {
		t.Fatal("a read-only hook must not rewrite an old table")
	}
	if out := HookReply("session", base.HookInput{Cwd: root}); strings.Contains(out, "ortg.tsv") && strings.Contains(out, "停用") {
		t.Fatalf("session must upgrade, not stop: %q", out)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatalf("table not upgraded: %v", err)
	}
	if r, _ := x.Row("a.go"); r.Outline != "a.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-" {
		t.Fatalf("the outline must be in the current fields: %q", r.Outline)
	}
	if b, _ := os.ReadFile(p + ".old"); string(b) != old {
		t.Fatal("the old table must survive the first save in .old")
	}
}

// On a fresh table the header still holds the template's placeholders: no
// outline and no plan is accepted until a model has read the docs and written
// the header, and the overview says so first. A whole document may write the
// header; one that still carries the template's is refused and rolled back.
func TestHeaderFirst(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, "ARCH.md"), []byte("# a"), 0o644) // sorts before README.md
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# r"), 0o644)
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	for i := 0; i < 21; i++ {
		os.WriteFile(filepath.Join(root, "docs", fmt.Sprintf("d%02d.md", i)), []byte("d"), 0o644)
	}
	t.Chdir(root)
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	line := "a.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-"
	_, err := Update("a.go", line)
	if err == nil || !strings.Contains(err.Error(), "纲要头还没写") || !strings.Contains(err.Error(), "：README.md、") || !strings.Contains(err.Error(), " 等 23 个") {
		t.Fatalf("an outline before the header must be refused with the docs, README first, capped: %v", err)
	}
	if list := err.Error()[strings.Index(err.Error(), "：README.md")+len("：") : strings.Index(err.Error(), " 等 23 个")]; len(strings.Split(list, "、")) != docLimit {
		t.Fatalf("the list must name %d documents: %q", docLimit, list)
	}
	if _, err := Target("a.go", line, false, ""); err == nil || !strings.Contains(err.Error(), "纲要头还没写") {
		t.Fatalf("a plan before the header must be refused too: %v", err)
	}
	if _, err := Target("a.go", "", true, ""); err == nil {
		t.Fatal("a planned delete before the header must be refused too")
	}
	// writing 【部署】 alone is not enough: 【系统】 still holds its placeholder
	var deployed []string
	for _, l := range strings.Split(headerTemplate, "\n") {
		if strings.HasPrefix(l, "#【部署】(") {
			l = "#【部署】go build 出单个二进制"
		}
		deployed = append(deployed, l)
	}
	if _, err := Header(strings.Join(deployed, "\n"), nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("a.go", line); err == nil {
		t.Fatal("a header whose 【系统】 is still the placeholder is not written yet")
	}
	if doc, _ := Overview("", false); !strings.HasPrefix(doc, "ortg: 纲要头还没写") {
		t.Fatalf("the overview of a fresh table must lead with the docs:\n%.200s", doc)
	}
	section := "\n\n===根 " + filepath.ToSlash(root) + "/===\n" + line + "\n"
	if _, err := Target("", "", false, headerTemplate+section); err == nil || !strings.Contains(err.Error(), "纲要头还没写") {
		t.Fatalf("a document still carrying the template header must be refused: %v", err)
	}
	if r, _ := mustOpen(t, root).Row("a.go"); r.Target != "" {
		t.Fatalf("the refused document must be rolled back: %+v", r)
	}
	if _, err := Target("", "", false, testHeader+section); err != nil {
		t.Fatalf("a document with a real header writes the header: %v", err)
	}
	if _, err := Update("a.go", line); err != nil {
		t.Fatalf("with the header written outlines are accepted: %v", err)
	}
	if doc, _ := Overview("", true); strings.Contains(doc, "纲要头还没写") {
		t.Fatalf("a written header needs no guidance:\n%.200s", doc)
	}
}

func mustOpen(t *testing.T, root string) *base.Index {
	t.Helper()
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// An outline whose Uses is new or changed may outdate the header's account of
// dependencies, so its submission reminds the model to check it; one with the
// same Uses does not, however its other fields change.
func TestHeaderDepNote(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	// Uses must name repository paths; empty directories hold no file that
	// would need an outline of its own.
	os.MkdirAll(filepath.Join(root, "b"), 0o755)
	os.MkdirAll(filepath.Join(root, "c"), 0o755)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	note := fmt.Sprintf(headerDepNote, "a.go")
	for _, step := range []struct {
		line string
		want bool
	}{
		{"a.go[7 L ST T]: Role:a | Uses:b/ | API:- | Constraints:-", true},           // first outline
		{"a.go[7 L ST T]: Role:改了职责 | Uses:b/ | API:X | Constraints:写 | 也不乱", false}, // same Uses
		{"a.go[7 L ST T]: Role:改了职责 | Uses:b/,c/ | API:X | Constraints:-", true},     // new edge
	} {
		got, err := Update("a.go", step.line)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, note) != step.want {
			t.Fatalf("Update(%q) reminder=%v, want %v:\n%s", step.line, !step.want, step.want, got)
		}
	}
	if !strings.Contains(rulesBlock, "用 ortg_header 改") {
		t.Fatal("rule 3 must ask for the header's global descriptions to follow a change")
	}
	if got, _ := Update("a.go", "a.go[7 L ST T]: Role:x | Uses:- | API:X | Constraints:-"); !strings.Contains(got, note) {
		t.Fatalf("dropping every dependency changes the layers too:\n%s", got)
	}
	// no reminder while the table is still being built: a dependency-free
	// first outline, or a first outline while other files still lack one
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, "c.go"), []byte("package a"), 0o644)
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if got, _ := Update("b.go", "b.go[7 L ST T]: Role:b | Uses:c.go | API:- | Constraints:-"); strings.Contains(got, "Uses（依赖）") {
		t.Fatalf("a bulk build (c.go still without an outline) must not be reminded:\n%s", got)
	}
	if got, _ := Update("c.go", "c.go[7 L ST T]: Role:c | Uses:- | API:- | Constraints:-"); strings.Contains(got, "Uses（依赖）") {
		t.Fatalf("a first outline without dependencies must not be reminded:\n%s", got)
	}
	os.WriteFile(filepath.Join(root, "d.go"), []byte("package a"), 0o644)
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if got, _ := Update("d.go", "d.go[7 L ST T]: Role:d | Uses:b.go | API:- | Constraints:-"); !strings.Contains(got, fmt.Sprintf(headerDepNote, "d.go")) {
		t.Fatalf("a new file in a fully outlined table must be reminded:\n%s", got)
	}
}

// The first build runs documents first: a fresh table takes only documents'
// outlines, refuses a written header while a document lacks one (unless the
// same call moves it to observe), and only then takes source outlines; the
// guidance follows that progress, and documents get docQuota whatever their
// importance.
func TestDocsFirst(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# r"), 0o644)
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	os.WriteFile(filepath.Join(root, "docs", "a.md"), []byte("a"), 0o644)
	t.Chdir(root)
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	src := "main.go[7 E CL T]: Role:入口 | Uses:- | API:- | Constraints:-"
	if _, err := Update("main.go", src); err == nil || !strings.Contains(err.Error(), "还没纲要的文档：README.md、docs/a.md") {
		t.Fatalf("a source outline before the documents must be refused, naming them: %v", err)
	}
	if doc, _ := Overview("", false); !strings.HasPrefix(doc, fmt.Sprintf(headerFirstNote, "README.md、docs/a.md", docQuota)) {
		t.Fatalf("a fresh overview must lead with the documents to outline:\n%.300s", doc)
	}
	long := strings.Repeat("约", docQuota-10)
	got, err := Update("README.md", "README.md[3 D CL T]: Role:总览 | Uses:- | API:- | Constraints:"+long)
	if err != nil || strings.Contains(got, "配额") {
		t.Fatalf("a document's outline is taken first, under the document quota: %q %v", got, err)
	}
	if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "docs/a.md") {
		t.Fatalf("a written header waits for every document's outline: %v", err)
	}
	if x := mustOpen(t, root); !headerUnwritten(x.Header()) {
		t.Fatal("the refused header must not be written")
	}
	// moving a document to observe in the same call clears the way
	if _, err := Header(testHeader, nil, nil, nil, []string{"docs"}, nil, nil); err != nil {
		t.Fatalf("observed documents need no outline: %v", err)
	}
	// the gate belongs to the first build: once the header is written, a new
	// document without an outline does not hold back header edits
	os.WriteFile(filepath.Join(root, "NEW.md"), []byte("n"), 0o644)
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header(testHeader+"\n#【补充】x", nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("a written header is edited freely: %v", err)
	}
	if got, err := Update("main.go", src); err != nil {
		t.Fatalf("with the header written source outlines are taken: %v", err)
	} else if got2, _ := Update("main.go", "main.go[7 E CL T]: Role:入口 | Uses:- | API:- | Constraints:"+long); !strings.Contains(got2, "C7 配额") {
		t.Fatalf("a source file keeps its importance quota: %q (first reply %q)", got2, got)
	}
}

// Once every document has an outline the guidance asks for the header; a
// repository without documents is told to write it from the code.
func TestHeaderGuidanceStages(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644)
	t.Chdir(root)
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if doc, _ := Overview("", false); !strings.HasPrefix(doc, headerNoDocsNote) {
		t.Fatalf("no documents: the header comes from the code:\n%.200s", doc)
	}
	os.WriteFile(filepath.Join(root, "GUIDE.md"), []byte("g"), 0o644)
	if _, err := Update("GUIDE.md", "GUIDE.md[3 D CL T]: Role:指南 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	if doc, _ := Overview("", true); !strings.HasPrefix(doc, headerFromDocsNote) {
		t.Fatalf("documents outlined: the header comes next:\n%.200s", doc)
	}
	for p, want := range map[string]bool{"README.md": true, "docs/x.go": true, "a/doc/b.txt": true, "guide": true, "x.rst": true, "main.go": false, "docsify.go": false, "src/doc.go": false} {
		if isDocFile(p) != want {
			t.Errorf("isDocFile(%q) = %v", p, !want)
		}
	}
}

// While the header is still the template there are no layers to keep in step,
// so even a document that names a dependency and leaves nothing else without
// an outline gets no reminder.
func TestNoDepNoteBeforeHeader(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# r"), 0o644)
	os.MkdirAll(filepath.Join(root, "cmd"), 0o755)
	t.Chdir(root)
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	got, err := Update("README.md", "README.md[3 D CL T]: Role:总览 | Uses:cmd/ | API:- | Constraints:-")
	if err != nil || strings.Contains(got, "Uses（依赖）") || strings.Contains(got, "收尾") {
		t.Fatalf("no reminder before the header is written: %q %v", got, err)
	}
}

// The first outline of the last file without one ends the first build with
// the reminder to distill the documents still in the cognitive tier; not
// before, not on a rewrite, and not once the documents are observed.
func TestDistillNote(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, f := range []string{"a.go", "b.go", "README.md"} {
		os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644)
	}
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	os.WriteFile(filepath.Join(root, "docs", "d.md"), []byte("d"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	note := fmt.Sprintf(distillNote, "README.md、docs/d.md")
	if !strings.Contains(distillNote, "逐条核对") {
		t.Fatal("the distill note must ask for every convention's place before observing")
	}
	line := func(f string) string { return f + "[7 L ST T]: Role:x | Uses:- | API:- | Constraints:-" }
	for _, step := range []struct {
		file string
		want bool
	}{
		{"a.go", false}, // b.go still has no outline
		{"b.go", true},  // the last one
		{"b.go", false}, // a rewrite is not the end of a build
	} {
		got, err := Update(step.file, line(step.file))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, note) != step.want {
			t.Fatalf("Update(%s) distill=%v, want %v:\n%s", step.file, !step.want, step.want, got)
		}
	}
	// documents already observed leave nothing to distill
	os.WriteFile(filepath.Join(root, "c.go"), []byte("x"), 0o644)
	if _, err := Header("", nil, nil, nil, []string{"docs"}, []string{"README.md"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if got, _ := Update("c.go", line("c.go")); strings.Contains(got, "收尾") {
		t.Fatalf("no reminder once every document is observed:\n%s", got)
	}
}

// A bare call sends the whole outline only within both budgets: its size
// under the host's per-reply cap less the margin, and its estimated tokens
// under half the context window. Claude's window comes from the auto-compact
// setting, else a [1m] model named by ANTHROPIC_MODEL or recorded by the
// session hook, else 200K.
func TestWholeBudgetFollowsContextWindow(t *testing.T) {
	t.Setenv("ORTG_AGENT", "")
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	cases := []struct {
		window, model, recorded string
		want                    int
	}{
		{"", "", "", 200000},
		{"", "claude-opus-5-5[1m]", "", 1000000},
		{"", "", "claude-opus-5-5[1m]", 1000000},
		{"", "", "claude-sonnet-5", 200000},
		{"800000", "", "claude-sonnet-5", 800000},
		{"200000", "claude-opus-5-5[1m]", "", 200000},
		{"junk", "", "", 200000},
	}
	for _, c := range cases {
		t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", c.window)
		t.Setenv("ANTHROPIC_MODEL", c.model)
		base.MarkOverview(root, "model", c.recorded)
		if got := claudeWindow(root); got != c.want {
			t.Fatalf("window %q model %q recorded %q: %d, want %d", c.window, c.model, c.recorded, got, c.want)
		}
	}
	if got := limits().whole; got != claudeLimits.max-claudeLimits.margin {
		t.Fatalf("the per-reply budget is the cap less the margin, got %d", got)
	}
	// estimated tokens: CJK about one a character, the rest about half
	for text, want := range map[string]int{"": 0, strings.Repeat("纲", 1000): 1079, strings.Repeat("a", 1000): 530} {
		if got := estTokens(text); got != want {
			t.Fatalf("Claude estTokens(%d chars) = %d, want %d", len([]rune(text)), got, want)
		}
	}
	t.Setenv("ORTG_AGENT", "codex")
	if got := estTokens(strings.Repeat("a", 1000)); got != 253 {
		t.Fatalf("Codex estTokens = %d, want 253", got)
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("model = \"m\"\nmodel_context_window = 300000\n"), 0o644)
	if got := contextWindow(root); got != 300000 {
		t.Fatalf("under Codex the window comes from its config: got %d", got)
	}
	t.Setenv("ORTG_AGENT", "")

	// the session hook records the model; a small window turns the bare call
	// into the module list, naming the window
	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, "a.go"), []byte("package a"), 0o644)
	t.Chdir(proj)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	HookReplyFull("session", base.HookInput{Cwd: proj, SessionID: "s1", Source: "startup", Model: "claude-opus-5-5[1m]"})
	if got := claudeWindow(repoRoot()); got != 1000000 {
		t.Fatalf("the model the session hook saw sets the window: got %d", got)
	}
	if doc, _ := Overview("", false); strings.Contains(doc, "模块清单（") {
		t.Fatalf("a tiny outline goes whole:\n%.300s", doc)
	}
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "100")
	doc, _ := Overview("", true)
	if !strings.Contains(doc, "超过上下文窗口（100 token）的一半") || !strings.Contains(doc, " 字、估约 ") {
		t.Fatalf("over half the window a bare call sends the module list:\n%.400s", doc)
	}
}

// A plan that changes the code but not the outline: ortg_target takes "=",
// stores the current outline as the target, and the outline and the review
// show one short line instead of the whole entry twice.
func TestTargetSameShorthand(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"a.go", "b.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	cur := "a.go[7 L ST T]: Role:存储 | Uses:- | API:Save | Constraints:写入前必须保持文件锁(TestLock)"
	Update("a.go", cur)
	msg, err := Target("a.go", " = ", false, "")
	if err != nil || !strings.Contains(msg, "同现在") {
		t.Fatalf("= must write the current outline as the target: %q %v", msg, err)
	}
	if r, _ := mustOpen(t, root).Row("a.go"); r.Target != cur {
		t.Fatalf("the stored target is the current outline: %+v", r)
	}
	if _, err := Target("b.go", "=", false, ""); err == nil {
		t.Fatal("= needs a current outline to stand for")
	}
	doc, _ := Overview("*", true)
	if !strings.Contains(doc, prefixTargetSame) || strings.Contains(doc, prefixTarget+cur) {
		t.Fatalf("an unchanged target renders as one short line:\n%s", doc)
	}
	rev, _ := Review()
	if !strings.Contains(rev, fmt.Sprintf(reviewSame, "a.go")) || strings.Contains(rev, "目标: "+cur) {
		t.Fatalf("the review lists an unchanged target in one line:\n%s", rev)
	}
}

// Writing a planned test file is allowed, once with a reminder not to bend a
// guard test's expectation to the new code.
func TestGateTestFileReminder(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "tests"), 0o755)
	for _, p := range []string{"a.go", "tests/a_test.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.go", "a.go[7 L ST T]: Role:存储 | Uses:- | API:Save | Constraints:-")
	Update("tests/a_test.go", "a_test.go[3 L ST T]: Role:测a | Uses:a.go | API:- | Constraints:-")
	Overview("", false)
	Target("a.go", "=", false, "")
	Target("tests/a_test.go", "=", false, "")
	Review()
	Review()
	gate := func(p string) string { got, _ := Gate("Edit", filepath.Join(root, p), "", "", ""); return got }
	if got := gate("a.go"); got != "" {
		t.Fatalf("a planned source file passes silently: %q", got)
	}
	got := gate("tests/a_test.go")
	if strings.Contains(got, `"permissionDecision":"deny"`) || !strings.Contains(got, "按证据裁决") || !strings.Contains(got, "不改期望") {
		t.Fatalf("a planned test file passes with the reminder: %q", got)
	}
	if got := gate("tests/a_test.go"); got != "" {
		t.Fatalf("the reminder comes once per file: %q", got)
	}
	for p, want := range map[string]bool{
		"tests/x.rs": true, "pkg/a_test.go": true, "test_a.py": true, "src/a.test.ts": true,
		"ui/x.spec.js": true, "crates/x/tests/y.rs": true, "src/main.rs": false, "latest.go": false, "contest/a.go": false,
	} {
		if isTestPath(p) != want {
			t.Fatalf("isTestPath(%q) = %v, want %v", p, !want, want)
		}
	}
}

// ortg_target's items: several files in one call, "=" and 删除 included; a
// bad item is reported without undoing the others.
func TestTargetItems(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"a.go", "b.go", "c.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.go", "a.go[7 L ST T]: Role:存储 | Uses:- | API:Save | Constraints:-")
	Update("b.go", "b.go[7 L CL T]: Role:命令 | Uses:a.go | API:Run | Constraints:-")
	out, err := TargetItems([]string{
		"a.go a.go[7 L ST T]: Role:存储 | Uses:- | API:Store | Constraints:-",
		"b.go =",
		"c.go 删除",
		"d.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已写入 a.go") || !strings.Contains(out, "同现在") || !strings.Contains(out, "c.go 计划删除") || !strings.Contains(out, "d.go：失败") {
		t.Fatalf("each item reported on its own line:\n%s", out)
	}
	x, _ := base.Open(root)
	if r, _ := x.Row("a.go"); !strings.Contains(r.Target, "API:Store") {
		t.Fatalf("a.go target: %q", r.Target)
	}
	if r, _ := x.Row("b.go"); r.Target != r.Outline {
		t.Fatalf("b.go = keeps the outline: %q", r.Target)
	}
	if r, _ := x.Row("c.go"); !r.TargetDelete {
		t.Fatal("c.go marked for deletion")
	}
	if _, err := TargetItems([]string{"x.go"}); err == nil {
		t.Fatal("every item failing fails the call")
	}
}

// The cache outlives the MCP process: a host resuming the conversation in a
// new process (claude -p --resume) starts a new server whose context still
// holds the outline, so nothing is sent twice and the gate stays open; a
// session that did not resume (the start mark) voids the saved cache.
func TestSentCacheSurvivesResume(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.go", "a.go[7 L ST T]: Role:存储 | Uses:- | API:Save | Constraints:-")
	newProcess := func() {
		overviewMu.Lock()
		overviewSent = map[string]*sentCache{}
		overviewMu.Unlock()
	}
	if out, _ := Overview("", false); !strings.Contains(out, "Role:存储") {
		t.Fatalf("first fetch sends the outline:\n%s", out)
	}
	newProcess()
	if out, _ := Overview("", false); strings.Contains(out, "Role:存储") {
		t.Fatalf("a resumed context already holds the outline:\n%s", out)
	}
	if got, _ := Gate("Read", filepath.Join(root, "a.go"), "", "", ""); got != "" {
		t.Fatalf("the gate knows the outline is loaded: %q", got)
	}
	time.Sleep(10 * time.Millisecond)
	HookReply("session", base.HookInput{Source: "startup", SessionID: "s2"})
	newProcess()
	if out, _ := Overview("", false); !strings.Contains(out, "Role:存储") {
		t.Fatalf("a new session voids the saved cache:\n%s", out)
	}
	time.Sleep(10 * time.Millisecond)
	HookReply("session", base.HookInput{Source: "resume", SessionID: "s2"})
	newProcess()
	if out, _ := Overview("", false); strings.Contains(out, "Role:存储") {
		t.Fatalf("resume keeps the saved cache:\n%s", out)
	}
}

// A plan that brings a concept into a module hears of the guards other
// modules hold on that concept, though nothing in the plan depends on them.
func TestReviewQuotesRelatedContracts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"printer.go", "searcher.go", "other.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("printer.go", "printer.go[7 L ST T]: Role:打印 | Uses:- | API:Print | Constraints:必须保持 max_matches 达限后仍输出 after_context(tests::limit)；必须保持列号从 1 起(tests::column)")
	Update("searcher.go", "searcher.go[7 L CL T]: Role:搜索 | Uses:- | API:Search | Constraints:-")
	Update("other.go", "other.go[7 L CL T]: Role:无关 | Uses:- | API:- | Constraints:必须保持 unrelated_thing 不变(tests::x)")
	Overview("", false)
	Target("searcher.go", "searcher.go[7 L CL T]: Role:搜索 | Uses:- | API:Search | Constraints:由 searcher 执行 max_matches 限额", false, "")
	out, _ := Review()
	if !strings.Contains(out, "printer.go：必须保持 max_matches 达限后仍输出 after_context") || !strings.Contains(out, "同名概念：max_matches") {
		t.Fatalf("the guard on the concept the plan brings in is quoted:\n%s", out)
	}
	if strings.Contains(out, "列号从 1 起") || strings.Contains(out, "unrelated_thing") {
		t.Fatalf("clauses sharing no new identifier stay out:\n%s", out)
	}
}

// Uses names repository paths — a file, or a directory with a trailing
// slash, notes in parentheses allowed; a package, a module or a bare file
// name is refused, and "=" (the outline stays) is not checked again.
func TestUsesMustBePaths(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "pkg", "searcher"), 0o755)
	for _, p := range []string{"a.go", "pkg/searcher/core.go", "pkg/util.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	ok := "a.go[7 L ST T]: Role:a | Uses:pkg/searcher/,pkg/util.go(Sink) | API:- | Constraints:-"
	if _, err := Update("a.go", ok); err != nil {
		t.Fatalf("paths and directories are accepted: %v", err)
	}
	for _, uses := range []string{"grep-searcher", "util.go", "searcher", "pkg/missing.go"} {
		_, err := Update("a.go", "a.go[7 L ST T]: Role:a | Uses:"+uses+" | API:- | Constraints:-")
		if err == nil || !strings.Contains(err.Error(), uses) {
			t.Fatalf("Uses %q must be refused naming it: %v", uses, err)
		}
	}
	x, _ := base.Open(root)
	if got := x.Dependents("pkg/searcher/core.go"); len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("a directory item makes its files' dependents: %v", got)
	}
	if got := x.Dependents("pkg/util.go"); len(got) != 1 {
		t.Fatalf("an item with a note still matches its path: %v", got)
	}
	Update("pkg/util.go", "util.go[7 L ST T]: Role:u | Uses:- | API:- | Constraints:-")
	if _, err := Target("pkg/util.go", "util.go[7 L ST T]: Role:u | Uses:grep | API:- | Constraints:-", false, ""); err == nil {
		t.Fatal("a target outline is checked too")
	}
	if _, err := Target("pkg/util.go", "=", false, ""); err != nil {
		t.Fatalf("= keeps the current outline unchecked: %v", err)
	}
}

// Cross-module contracts live once in the header's 【契约】 table; files take
// part through their Keys. A key must be registered; a review of a plan
// touching a party quotes the contract and names every party, and for a
// literal key the files using the literal undeclared; the impact note after
// an edit lists the other parties; a registered key nobody declares is
// reported when the header is written.
func TestContractKeys(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	files := map[string]string{
		"searcher.go": "package a", "printer.go": "package a", "cli.go": "package a",
		"server.go": `h.Handle("/api/v1/order", order)`, "client.go": `post("/api/v1/order")`,
	}
	for p, body := range files {
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("cli.go", "cli.go[7 L CL T]: Role:c | Uses:- | API:- | Constraints:- | Keys:max_matches(解析)"); err == nil || !strings.Contains(err.Error(), "max_matches") {
		t.Fatalf("an unregistered key is refused: %v", err)
	}
	reg := "\n#【契约】max_matches：达限后仍输出 after_context(tests::limit)\n#【契约】route:POST /api/v1/order：qty 以件为单位(tests::order)\n#【契约】unit:timeout：毫秒"
	out, err := Header(testHeader+reg, nil, nil, nil, nil, nil, nil)
	if err != nil || !strings.Contains(out, "max_matches") || !strings.Contains(out, "unit:timeout") {
		t.Fatalf("keys nobody declares are reported: %q %v", out, err)
	}
	for p, line := range map[string]string{
		"cli.go":      "cli.go[7 L CL T]: Role:c | Uses:- | API:- | Constraints:- | Keys:max_matches(解析)",
		"printer.go":  "printer.go[7 L CL T]: Role:p | Uses:- | API:- | Constraints:- | Keys:max_matches(输出)",
		"searcher.go": "searcher.go[7 L CL T]: Role:s | Uses:- | API:- | Constraints:-",
		"server.go":   "server.go[7 L CL T]: Role:sv | Uses:- | API:- | Constraints:- | Keys:route:POST /api/v1/order(提供)",
		"client.go":   "client.go[7 L CL T]: Role:cl | Uses:- | API:- | Constraints:-",
	} {
		if _, err := Update(p, line); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	Overview("", false)
	// The searcher takes over the limit and declares the key in its target.
	Target("searcher.go", "searcher.go[7 L CL T]: Role:s | Uses:- | API:- | Constraints:- | Keys:max_matches(执行)", false, "")
	Target("server.go", "=", false, "")
	rv, _ := Review()
	for _, want := range []string{"【max_matches】达限后仍输出 after_context(tests::limit)", "参与者：cli.go、printer.go、searcher.go", "【route:POST /api/v1/order】", "/api/v1/order 却没在 Keys 里声明", "client.go"} {
		if !strings.Contains(rv, want) {
			t.Fatalf("review lacks %q:\n%s", want, rv)
		}
	}
	if strings.Count(rv, "达限后仍输出 after_context") != 1 {
		t.Fatalf("a contract quoted by key is not repeated by the name fallback:\n%s", rv)
	}
	x, _ := base.Open(root)
	if got := impactCandidates(x, "s", []string{"printer.go"}); !strings.Contains(got, "cli.go(契约:max_matches)") {
		t.Fatalf("the impact note names the other parties: %s", got)
	}
}

// A hub file takes part in many contracts: the review ranks them by the
// concepts the plan brings in, quotes only the first keyFullMax in full and
// names the rest.
func TestContractKeysRanked(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"hub.go", "core.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	reg := ""
	var keys []string
	for i := range 9 {
		k := fmt.Sprintf("k%d", i)
		keys = append(keys, k)
		reg += fmt.Sprintf("\n#【契约】%s：约定%d", k, i)
	}
	reg += "\n#【契约】limit：达限后仍输出 after_context，max_matches 按匹配计"
	if _, err := Header(testHeader+reg, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	Update("hub.go", "hub.go[7 L CL T]: Role:h | Uses:- | API:- | Constraints:- | Keys:"+strings.Join(keys, ",")+",limit(传递)")
	Update("core.go", "core.go[7 L CL T]: Role:c | Uses:- | API:- | Constraints:- | Keys:limit(窗口)")
	Overview("", false)
	Target("hub.go", "=", false, "")
	Target("core.go", "core.go[7 L CL T]: Role:c | Uses:- | API:- | Constraints:由 searcher 执行 max_matches | Keys:limit(窗口)", false, "")
	rv, _ := Review()
	first := strings.Index(rv, "- 【")
	if first < 0 || !strings.HasPrefix(rv[first:], "- 【limit】") || !strings.Contains(rv, "计划新引入的同名概念：max_matches") {
		t.Fatalf("the contract naming what the plan brings in comes first:\n%s", rv)
	}
	if n := strings.Count(rv, "\n- 【"); n != keyFullMax {
		t.Fatalf("%d contracts quoted in full, want %d:\n%s", n, keyFullMax, rv)
	}
	if !strings.Contains(rv, "其余也涉及的契约") || !strings.Contains(rv, "【k8】(1)") {
		t.Fatalf("the rest are named with their party count:\n%s", rv)
	}
}

// The tests the outlines cite reach the review and the note after an edit;
// test contract clauses have a budget of their own.
func TestGuardTestsNamed(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"a.go", "b.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Header(testHeader+"\n#【契约】limit：必须保持达限仍出上下文(regression::issue42)", nil, nil, nil, nil, nil, nil)
	long := strings.Repeat("约", 480)
	out, err := Update("a.go", "a.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:"+long+"；必须保持 空输入无输出(tests::empty) | Keys:limit(执行)")
	if err != nil || strings.Contains(out, "超过") {
		t.Fatalf("500 runes of other constraints plus a contract clause fit C7: %q %v", out, err)
	}
	out, _ = Update("b.go", "b.go[7 L ST T]: Role:b | Uses:- | API:- | Constraints:必须保持 "+strings.Repeat("长", 260)+"(tests::b)")
	if !strings.Contains(out, "契约配额 250") {
		t.Fatalf("contract clauses over half the quota are flagged: %q", out)
	}
	Overview("", false)
	Target("a.go", "=", false, "")
	rv, _ := Review()
	if !strings.Contains(rv, "守护测试") || !strings.Contains(rv, "tests::empty") || !strings.Contains(rv, "regression::issue42") {
		t.Fatalf("the review names the guard tests of the plan and its contracts:\n%s", rv)
	}
	x, _ := base.Open(root)
	if got := impactTests(x, []string{"a.go"}); !strings.Contains(got, "tests::empty、regression::issue42") {
		t.Fatalf("the note after an edit names them too: %q", got)
	}
}

// Entries the tests call stay contracts: "单测入口" draws a warning, a Rust
// function the outline's API lists is flagged once it sits under
// #[cfg(test)] or is gone, and "=" on a file in a contract reminds that a
// moved execution point needs a full target and an updated contract.
func TestTestEntriesGuarded(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "dir.rs"), []byte("impl ConfigBuilder {\n    pub(crate) fn build(&self) {}\n    pub fn add(&self) {}\n}\n"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Header(testHeader+"\n#【契约】limit：达限仍出上下文", nil, nil, nil, nil, nil, nil)
	out, _ := Update("dir.rs", "dir.rs[7 L ST T]: Role:d | Uses:- | API:单测入口 ConfigBuilder{build,add,remove_all} | Constraints:- | Keys:limit(执行)")
	if !strings.Contains(out, "测试也调用") {
		t.Fatalf("\"单测入口\" is warned against: %q", out)
	}
	x, _ := base.Open(root)
	if got := apiGuardNote(x, "dir.rs"); !strings.Contains(got, "remove_all") || strings.Contains(got, "cfg(test) 下") {
		t.Fatalf("a missing API name is reported, nothing hidden yet: %q", got)
	}
	os.WriteFile(filepath.Join(root, "dir.rs"), []byte("impl ConfigBuilder {\n    #[cfg(test)]\n    pub(crate) fn build(&self) {}\n    pub fn add(&self) {}\n    fn remove_all() {}\n}\n"), 0o644)
	if got := apiGuardNote(x, "dir.rs"); !strings.Contains(got, "build 现在在 #[cfg(test)] 下") || strings.Contains(got, "已找不到") {
		t.Fatalf("an API function under cfg(test) is flagged: %q", got)
	}
	Update("dir.rs", "dir.rs[7 L ST T]: Role:d | Uses:- | API:测试也调用 ConfigBuilder{build,add} | Constraints:- | Keys:limit(执行)")
	if out, _ := Target("dir.rs", "=", false, ""); !strings.Contains(out, "参与契约 limit") {
		t.Fatalf("= on a contract party reminds of the execution point: %q", out)
	}
	if !strings.Contains(reviewChecklist, "9. 执行点") || !strings.Contains(updateDiscipline, "不写\"单测入口\"") {
		t.Fatal("the checklist and the discipline carry the rules")
	}
}

// A planned file taking on a contract it did not hold before is a duty
// moving in: the review says so, naming the old holders, and asks for the new
// holder to pass their guards on its own; any quoted contract comes with the
// conflict questions and the order of evidence.
func TestReviewMigrationAndConflict(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"printer.go", "searcher.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Header(testHeader+"\n#【契约】limit：必须保持达限仍出上下文(regression::issue42)", nil, nil, nil, nil, nil, nil)
	Update("printer.go", "printer.go[7 L ST T]: Role:p | Uses:- | API:- | Constraints:- | Keys:limit(执行)")
	Update("searcher.go", "searcher.go[7 L CL T]: Role:s | Uses:- | API:- | Constraints:-")
	Overview("", false)
	Target("searcher.go", "searcher.go[7 L CL T]: Role:s | Uses:- | API:- | Constraints:由此执行限额 | Keys:limit(执行)", false, "")
	rv, _ := Review()
	for _, want := range []string{"职责迁移：searcher.go 承担 limit(原参与者 printer.go)", "单独生效", "按证据裁决", "a 需求点名了该测试", "rulings"} {
		if !strings.Contains(rv, want) {
			t.Fatalf("review lacks %q:\n%s", want, rv)
		}
	}
	if !strings.Contains(headerTemplate, "需求与守护测试冲突时按证据裁决") {
		t.Fatal("the header template carries the order of evidence")
	}
}

// A plan binds the model to the contracts of its own files even when every
// target says "=": a file that is no hub has all its keys quoted in full, and
// the plan converges only once each of them has a ruling, which is then
// written at the end of the contract's text.
func TestRulingsGateConvergence(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"core.go", "hub.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	reg := ""
	var hubKeys []string
	for i := range 12 {
		k := fmt.Sprintf("k%02d", i)
		hubKeys = append(hubKeys, k)
		reg += fmt.Sprintf("\n#【契约】%s：约定%d", k, i)
	}
	reg += "\n#【契约】limit：达限仍出上下文"
	Header(testHeader+reg, nil, nil, nil, nil, nil, nil)
	Update("hub.go", "hub.go[7 L CL T]: Role:h | Uses:- | API:- | Constraints:- | Keys:"+strings.Join(hubKeys, ",")+",limit(传递)")
	Update("core.go", "core.go[7 L CL T]: Role:c | Uses:- | API:- | Constraints:- | Keys:limit(执行),k11(执行)")
	Overview("", false)
	Target("core.go", "=", false, "")
	Target("hub.go", "=", false, "")
	first, _ := Review()
	if !strings.Contains(first, "- 【limit】达限仍出上下文") || !strings.Contains(first, "- 【k11】") {
		t.Fatalf("every key of a file that is no hub is quoted in full, \"=\" or not:\n%s", first)
	}
	second, _ := Review("limit：需求没讲窗口，按 b 保持")
	if strings.Contains(second, "已收敛") || !strings.Contains(second, "还缺这些契约的裁决") || !strings.Contains(second, "k11") {
		t.Fatalf("a missing ruling keeps the plan open:\n%s", second)
	}
	third, _ := Review("limit：需求没讲窗口，按 b 保持", "k11: 不涉及")
	if !strings.Contains(third, "已收敛") {
		t.Fatalf("with every ruling the plan converges:\n%s", third)
	}
	x, _ := base.Open(root)
	if got := x.Contracts()["limit"]; got != "达限仍出上下文；【裁决】需求没讲窗口，按 b 保持" {
		t.Fatalf("the ruling is recorded in the contract: %q", got)
	}
}

// An outline whose API leaves out a name the file newly exports (against git
// HEAD) is refused; "=" warns about it; a name already exported at HEAD and
// a file new to the repository pass.
func TestNewAPIMustBeListed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(root, "s.rs"), []byte("pub struct Searcher;\nimpl Searcher {\n    pub fn new() {}\n}\n"), 0o644)
	git("add", "s.rs")
	git("commit", "-q", "-m", "base")
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	line := "s.rs[7 L ST T]: Role:搜索 | Uses:- | API:Searcher{new} | Constraints:-"
	if _, err := Update("s.rs", line); err != nil {
		t.Fatal(err)
	}
	if out, _ := Target("s.rs", "=", false, ""); !strings.Contains(out, "新增对外接口") {
		t.Fatalf("= warns that a new public interface needs a full target: %q", out)
	}
	os.WriteFile(filepath.Join(root, "s.rs"), []byte("pub struct Searcher;\nimpl Searcher {\n    pub fn new() {}\n    pub fn max_matches(&mut self) {}\n    pub(crate) fn internal() {}\n}\n"), 0o644)
	if _, err := Update("s.rs", line); err == nil || !strings.Contains(err.Error(), "max_matches") || strings.Contains(err.Error(), "internal") {
		t.Fatalf("a new pub fn missing from the API is refused, pub(crate) is not API: %v", err)
	}
	if _, err := Update("s.rs", "s.rs[7 L ST T]: Role:搜索 | Uses:- | API:Searcher{new,max_matches} | Constraints:限额由此执行"); err != nil {
		t.Fatalf("listing it lets the outline in: %v", err)
	}
	os.WriteFile(filepath.Join(root, "n.rs"), []byte("pub fn fresh() {}\n"), 0o644)
	Scan()
	if _, err := Update("n.rs", "n.rs[7 L ST T]: Role:新 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatalf("a file new to the repository is not checked: %v", err)
	}
}

// Model-visible text must stay generic: examples are made up, never taken
// from a benchmark repository (its names, paths, contract keys or answers).
// leakDigests are the SHA-256 digests (hex) and byte lengths of the
// lower-cased words model-visible text must never contain — names and paths
// specific to outside projects. Only digests are kept, so this file names none
// of them.
var leakDigests = []struct {
	n   int
	sum string
}{
	{7, "33165223a0a3c347557831c78e3780cafe5f22ef61a0dbf2a53ef9a3756eb1c9"},
	{7, "24cc503066d4871ecb5ee37119d3dad0180a56252ba6594167b16bf1b21ad7b2"},
	{6, "a80b15a7ec184a9b6fa1c6370e36005594350e9f6a1fe97e8d91dabafe3e0f70"},
	{9, "d579097b3c57f0d8757d3fada25f0b2135218b96f1dd2bc8d47cdb222cac38a0"},
	{6, "d8397f6010ef28003e91d1e967f4362a078c779aef93d208b5b1195fcf54b06f"},
	{7, "a3f04aab073f780da9779bddb1057456cae030b79a6fc3f494f0d5bb99daa42b"},
	{7, "33343eacfe0fc41b3328be6ba8c8ae1a2179c6db8dc9a706526c90c798abebc9"},
	{11, "9fcb6f0f387660d682894cef488f74ac8e2b28e0b245d11c97c56b2be2392c4c"},
	{5, "c3af43271ca5c989397c27dc110105646b22cd2f7c7a26b470d478b69ef2b495"},
	{7, "90130fa991d9c9ab0c765dae8b2578371dff5fc0261f70c0ad791b701b84b89d"},
	{8, "c067f98b8e2155d2a6d0c2ce542c20be2a0d31b7707a7b87a038af51b4177df3"},
	{7, "ba70e1dacc17e1a77072ce9705bcbe3bc0063b5792d590f20eefdcd3d876daee"},
	{18, "6106fe262b4d0ba3c59718410bca69ad970d918ac873db2cff344510724a9ecd"},
	{5, "b2965721c3250642b48a8b837effacba2ac0b148a766e9390731d679ebe5425d"},
	{5, "dcc08822fd52f2b286e6fccea1a12d54b27e4065fdcab20bcb42f083748bd009"},
	{8, "59645aa4c8d69fdc2e443af759f8d4bb15e83e3fd08ef1ba9e7c2892bbb8c8e3"},
}

func TestPromptNoBenchmarkLeak(t *testing.T) {
	src, err := os.ReadFile("prompt.go")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(src))
	for i, d := range leakDigests {
		for j := 0; j+d.n <= len(text); j++ {
			if s := sha256.Sum256([]byte(text[j : j+d.n])); hex.EncodeToString(s[:]) == d.sum {
				t.Errorf("prompt.go contains forbidden word #%d at byte %d", i, j)
				break
			}
		}
	}
}

// A broad run is the repository's own suite with nothing narrowed or
// overridden; anything selecting part of it, or run elsewhere, is not.
func TestTestRunKind(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "pkg", "test_a.py"), []byte(""), 0o644)
	cases := []struct {
		cmd           string
		broad, commit bool
	}{
		{"pytest", true, false},
		{"timeout 1800 python -m pytest -q -n 8 pkg", true, false},
		{"pytest --tb short -x -p no:cacheprovider", true, false},
		{`python -m pytest -o addopts="" -q pkg`, false, false},
		{"pytest -p no:doctest", false, false},
		{"python3 -m pytest pkg/sub", false, false},
		{"pytest pkg/test_a.py::test_x", false, false},
		{"pytest -k slow", false, false},
		{"pytest --deselect pkg/test_a.py::t", false, false},
		{"pytest --pyargs pkg.sub", false, false},
		{"cd pkg && pytest", false, false},
		{"cd /elsewhere && pytest", false, false},
		{"FOO=1 uv run pytest", true, false},
		{"go test ./...", true, false},
		{"go test -count 1 -race ./...", true, false},
		{"go test -run TestX ./...", false, false},
		{"go test ./pkg/...", false, false},
		{"go test", false, false},
		{"cargo test", true, false},
		{"cargo test --workspace --features full", true, false},
		{"cargo test -p core", false, false},
		{"cargo test parse_", false, false},
		{"cargo test -- --skip slow", false, false},
		{"cargo nextest run", true, false},
		{"npm test", true, false},
		{"npm run test -- src/a.test.js", false, false},
		{"npx vitest run", true, false},
		{"npx jest -t name", false, false},
		{"mvn -q test", true, false},
		{"mvn test -Dtest=FooTest", false, false},
		{"./gradlew test", true, false},
		{"./gradlew test --tests Foo", false, false},
		{"make test", true, false},
		{"git add a.py && git commit -m x", false, true},
		{"git -c user.name=x commit -qm y", false, true},
		{"git -C /elsewhere commit -m y", false, false},
		{"git log --grep commit", false, false},
		{"echo git commit", false, false},
		{"pytest && git commit -am x", true, true},
	}
	for _, c := range cases {
		if b, m := testRunKind(c.cmd, root); b != c.broad || m != c.commit {
			t.Errorf("%q: broad=%v commit=%v, want %v %v", c.cmd, b, m, c.broad, c.commit)
		}
	}
}

// gitRepo makes a repository at a fresh root with files committed, returns
// the root and a git runner; the test is skipped without git.
func gitRepo(t *testing.T, files map[string]string) (string, func(args ...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	for p, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	git("add", ".")
	git("commit", "-q", "-m", "base")
	return root, git
}

// A commit of code changed since the last broad test run is refused once
// per HEAD; a broad run in between, a document-only change, or an outline
// not yet loaded let it through.
func TestGateVerifyBeforeCommit(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root, git := gitRepo(t, map[string]string{"a.py": "x = 1\n", "README.md": "r\n"})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.py", "a.py[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-")
	n := 0
	later := func(p string) {
		n++
		os.WriteFile(filepath.Join(root, p), []byte(fmt.Sprintf("x = %d # %s\n", n, p)), 0o644)
		future := time.Now().Add(2 * time.Second)
		os.Chtimes(filepath.Join(root, p), future, future)
	}
	later("a.py")
	if got, _ := Gate("Bash", "", "", "git commit -am x", ""); got != "" {
		t.Fatalf("outline not loaded: no verify gate yet: %q", got)
	}
	Overview("", false)
	got, _ := Gate("Bash", "", "", "git commit -am x", "")
	if !strings.Contains(got, `"permissionDecision":"deny"`) || !strings.Contains(got, "整包测试") {
		t.Fatalf("a commit without a broad run since the change is refused: %q", got)
	}
	if got, _ := Gate("Bash", "", "", "git commit -am x", ""); got != "" {
		t.Fatalf("refused once per HEAD: %q", got)
	}
	git("commit", "-qam", "one")
	later("a.py")
	if got, _ := Gate("Bash", "", "", "pytest -k one", ""); got != "" {
		t.Fatalf("a test run passes the gate: %q", got)
	}
	if got, _ := Gate("Bash", "", "", "git commit -am two", ""); !strings.Contains(got, "整包测试") {
		t.Fatalf("a narrowed run does not count; a new HEAD is refused again: %q", got)
	}
	git("commit", "-qam", "two")
	later("a.py")
	time.Sleep(10 * time.Millisecond)
	os.Chtimes(filepath.Join(root, "a.py"), time.Now().Add(-time.Second), time.Now().Add(-time.Second))
	Gate("Bash", "", "", "timeout 600 python -m pytest -q", "")
	if got, _ := Gate("Bash", "", "", "git commit -am three", ""); got != "" {
		t.Fatalf("a broad run after the change lets the commit through: %q", got)
	}
	git("commit", "-qam", "three")
	later("README.md")
	if got, _ := Gate("Bash", "", "", "git commit -am four", ""); got != "" {
		t.Fatalf("a document-only change needs no test run: %q", got)
	}
}

// A refusal stops the whole command, its staging included: a refused command
// that stages says so, and the commit that follows at that commit point
// without staging is refused once more — unless it stages itself or staging
// ran on its own in between.
func TestGateVerifyRestage(t *testing.T) {
	for cmd, want := range map[string]bool{
		`for d in src/ lib/; do [ -e "$d" ] && git add -A "$d"; done; git commit -m x && git tag t`: true,
		"git commit -qam x":              true,
		"git commit --all -m x":          true,
		"bash -c 'git add a.py'":         true,
		"git rm -q old.py":               true,
		"git commit -m 'add all' -q":     false,
		"git -C /elsewhere add .":        false,
		"git status --short && git diff": false,
	} {
		if got := stagesChanges(cmd, t.TempDir()); got != want {
			t.Errorf("%q: stages=%v, want %v", cmd, got, want)
		}
	}

	t.Setenv("TMPDIR", t.TempDir())
	root, git := gitRepo(t, map[string]string{"a.py": "x = 1\n"})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.py", "a.py[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-")
	Overview("", false)
	n := 0
	later := func() {
		n++
		os.WriteFile(filepath.Join(root, "a.py"), []byte(fmt.Sprintf("x = %d\n", n+1)), 0o644)
		future := time.Now().Add(2 * time.Second)
		os.Chtimes(filepath.Join(root, "a.py"), future, future)
	}
	denied := func(s string) bool { return strings.Contains(s, deniedMark) }

	later()
	got, _ := Gate("Bash", "", "", "git add -A a.py; git commit -qm one && git tag t1", "")
	if !denied(got) || !strings.Contains(got, "暂存步骤也没有执行") {
		t.Fatalf("a refused command that stages says its staging did not run: %q", got)
	}
	if got, _ := Gate("Bash", "", "", "git commit -qm one && git tag t1", ""); !denied(got) || !strings.Contains(got, "git status") {
		t.Fatalf("the commit that follows without staging is refused once more: %q", got)
	}
	if got, _ := Gate("Bash", "", "", "git commit -qm one && git tag t1", ""); got != "" {
		t.Fatalf("and only once: %q", got)
	}
	git("commit", "-qam", "one")

	later()
	Gate("Bash", "", "", "git add a.py && git commit -qm two", "")
	if got, _ := Gate("Bash", "", "", "git add a.py", ""); denied(got) {
		t.Fatalf("staging on its own passes: %q", got)
	}
	if got, _ := Gate("Bash", "", "", "git commit -qm two", ""); got != "" {
		t.Fatalf("staging run in between: the commit passes: %q", got)
	}
	git("commit", "-qam", "two")

	later()
	Gate("Bash", "", "", "git add a.py && git commit -qm three", "")
	if got, _ := Gate("Bash", "", "", "git add a.py && git commit -qm three", ""); got != "" {
		t.Fatalf("the whole command rerun passes: %q", got)
	}
	git("commit", "-qam", "three")

	later()
	got, _ = Gate("Bash", "", "", "git commit -qm four", "")
	if !denied(got) || strings.Contains(got, "暂存步骤也没有执行") {
		t.Fatalf("a refused commit that stages nothing gets no staging note: %q", got)
	}
	if got, _ := Gate("Bash", "", "", "git commit -qm four", ""); got != "" {
		t.Fatalf("nothing staging was refused: the commit passes: %q", got)
	}
}

// Removing a top-level name that tests still import or call is reported, by
// language, with the tests; a name kept through a module __getattr__, one
// another file of the Go package still defines, and references outside
// tests are not. The post-edit hook says it once per set of names; the
// ortg_update reply says it each time.
func TestRemovedTestedNames(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root, _ := gitRepo(t, map[string]string{
		"pkg/__init__.py":         "",
		"pkg/mod.py":              "def keep():\n    pass\n\n\ndef old_helper():\n    pass\n\n\nCONST = 1\n",
		"pkg/user.py":             "from pkg.mod import keep, old_helper\n",
		"pkg/tests/__init__.py":   "",
		"pkg/tests/test_mod.py":   "from pkg.mod import (\n    keep,\n    old_helper,\n)\n\n\ndef test_k():\n    keep()\n",
		"pkg/tests/test_other.py": "from pkg import mod\n\n\ndef test_c():\n    assert mod.CONST == 1\n",
		"g/a.go":                  "package g\n\nfunc Gone() {}\n\nfunc Stay() {}\n",
		"g/a_test.go":             "package g\n\nimport \"testing\"\n\nfunc TestG(t *testing.T) { Gone() }\n",
		"web/util.ts":             "export function fmt() {}\nexport const KEEP = 1\n",
		"web/util.test.ts":        "import { fmt } from './util'\n",
	})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) { os.WriteFile(filepath.Join(root, p), []byte(body), 0o644) }
	x := mustOpen(t, root)
	write("pkg/mod.py", "def keep():\n    pass\n")
	note, _ := removedTestedNote(x, "pkg/mod.py")
	for _, want := range []string{"old_helper（pkg/tests/test_mod.py）", "CONST（pkg/tests/test_other.py）", "默认保留"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "user.py") || strings.Contains(note, "keep（") {
		t.Fatalf("only gone names and test references count:\n%s", note)
	}
	write("pkg/mod.py", "def keep():\n    pass\n\n\nCONST = 2\n\n\ndef __getattr__(name):\n    if name == \"old_helper\":\n        return keep\n    raise AttributeError(name)\n")
	if note, _ := removedTestedNote(x, "pkg/mod.py"); note != "" {
		t.Fatalf("a name served by the module __getattr__ is kept: %s", note)
	}
	write("g/a.go", "package g\n\nfunc Stay() {}\n")
	if note, _ := removedTestedNote(x, "g/a.go"); !strings.Contains(note, "Gone（g/a_test.go）") {
		t.Fatalf("a Go name the package's tests call: %s", note)
	}
	write("g/b.go", "package g\n\nfunc Gone() {}\n")
	if note, _ := removedTestedNote(x, "g/a.go"); note != "" {
		t.Fatalf("the package still defines it in another file: %s", note)
	}
	write("web/util.ts", "export const KEEP = 1\n")
	if note, _ := removedTestedNote(x, "web/util.ts"); !strings.Contains(note, "fmt（web/util.test.ts）") {
		t.Fatalf("a JS/TS export a test imports: %s", note)
	}
	write("pkg/mod.py", "def keep():\n    pass\n")
	in := base.HookInput{Cwd: root, SessionID: "rm1", ToolName: "Edit", FilePath: filepath.Join(root, "pkg/mod.py")}
	if got := HookReply("post", in); !strings.Contains(got, "old_helper（pkg/tests/test_mod.py）") {
		t.Fatalf("the post-edit hook reports removed names tests use:\n%s", got)
	}
	write("pkg/mod.py", "def keep():\n    return 1\n")
	if got := HookReply("post", in); strings.Contains(got, "old_helper（") {
		t.Fatalf("the same names are reported once per session:\n%s", got)
	}
	out, err := Update("pkg/mod.py", "mod.py[7 L ST T]: Role:m | Uses:- | API:keep | Constraints:-")
	if err != nil || !strings.Contains(out, "old_helper（pkg/tests/test_mod.py）") {
		t.Fatalf("ortg_update reports them each time: %v\n%s", err, out)
	}
}

// Later reviews of one plan send only what changed: unchanged targets are
// named, contracts already quoted get a short line, and the conflict text
// and the checklist go out once; a converged plan's successor starts over.
func TestReviewDelta(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	for _, p := range []string{"a.go", "b.go"} {
		os.WriteFile(filepath.Join(root, p), []byte("package a"), 0o644)
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Header(testHeader+"\n#【契约】cache：命中后不再回源", nil, nil, nil, nil, nil, nil)
	Update("a.go", "a.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:- | Keys:cache(执行)")
	Update("b.go", "b.go[7 L CL T]: Role:b | Uses:a.go | API:- | Constraints:-")
	Overview("", false)
	TargetItems([]string{"a.go =", "b.go b.go[7 L CL T]: Role:b2 | Uses:a.go | API:- | Constraints:-"})
	first, _ := Review()
	for _, want := range []string{"- 【cache】命中后不再回源", "逐项核对", "按证据裁决", "目标: b.go[7 L CL T]: Role:b2"} {
		if !strings.Contains(first, want) {
			t.Fatalf("the first review says everything; lacks %q:\n%s", want, first)
		}
	}
	second, _ := Review("cache：不涉及")
	for _, want := range []string{fmt.Sprintf(reviewUnchanged, "a.go"), fmt.Sprintf(reviewUnchanged, "b.go"), fmt.Sprintf(reviewKeySeen, "cache"), "已收敛"} {
		if !strings.Contains(second, want) {
			t.Fatalf("the second review lacks %q:\n%s", want, second)
		}
	}
	for _, gone := range []string{"命中后不再回源", "逐项核对", "按证据裁决", "目标: b.go"} {
		if strings.Contains(second, gone) {
			t.Fatalf("the second review repeats %q:\n%s", gone, second)
		}
	}
	Target("b.go", "b.go[7 L CL T]: Role:b3 | Uses:a.go | API:- | Constraints:-", false, "")
	third, _ := Review("cache：不涉及")
	if !strings.Contains(third, "逐项核对") || !strings.Contains(third, "命中后不再回源") {
		t.Fatalf("after convergence the next plan is reviewed from scratch:\n%s", third)
	}
	Target("b.go", "b.go[7 L CL T]: Role:b4 | Uses:a.go | API:- | Constraints:-", false, "")
	fourth, _ := Review("cache：不涉及")
	if !strings.Contains(fourth, "Role:b4") || !strings.Contains(fourth, fmt.Sprintf(reviewUnchanged, "a.go")) || strings.Contains(fourth, "逐项核对") {
		t.Fatalf("a moved target is shown again, the rest named:\n%s", fourth)
	}
}

// Review findings on 0.5.22: redirections, unresolvable selections and
// no-run options classify right; a broad run survives compaction and counts
// when a subagent ran it; a root below the git toplevel still sees changes.
func TestVerifyGateEdges(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "pkg"), 0o755)
	for cmd, broad := range map[string]bool{
		"go test ./... 2>&1 | tail -50":                             true,
		"cargo test --workspace > /tmp/c.log 2>&1":                  true,
		"npm test 2>&1 | tail":                                      true,
		"python -m pytest pkg -n 8 -q > full.log 2>&1":              true,
		"nohup python -m pytest pkg -n 8 -q > /tmp/full.log 2>&1 &": true,
		"python -m pytest pkg -q 2>/dev/null | tail":                true,
		"for d in pkg/a pkg/b; do python -m pytest $d -q; done":     false,
		"python -m pytest $(git diff --name-only | grep test_) -q":  false,
		"python -m pytest --collect-only -q":                        false,
		"pytest -qk test_foo":                                       false,
		"pytest -vk 'a or b'":                                       false,
		"pytest -ra -n8":                                            true,
		"go test -list . ./...":                                     false,
	} {
		if b, _ := testRunKind(cmd, root); b != broad {
			t.Errorf("%q: broad=%v, want %v", cmd, b, broad)
		}
	}

	t.Setenv("TMPDIR", t.TempDir())
	root, git := gitRepo(t, map[string]string{"a.py": "x = 1\n"})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.py", "a.py[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-")
	Overview("", false)
	os.WriteFile(filepath.Join(root, "a.py"), []byte("x = 2\n"), 0o644)
	past := time.Now().Add(-time.Second)
	os.Chtimes(filepath.Join(root, "a.py"), past, past)
	Gate("Bash", "", "", "python -m pytest -q -n 8 2>&1 | tail", "agent-1") // a subagent's run
	time.Sleep(10 * time.Millisecond)
	base.MarkOverview(root, "reset", "") // compaction
	Overview("", false)
	if got, _ := Gate("Bash", "", "", "git commit -qam x && git tag t1", ""); got != "" {
		t.Fatalf("a subagent's broad run before compaction still counts: %q", got)
	}
	git("commit", "-qam", "x")

	sub := filepath.Join(root, "svc")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "b.py"), []byte("y = 1\n"), 0o644)
	if _, err := base.Create(sub, testHeader); err != nil {
		t.Fatal(err)
	}
	x := mustOpen(t, sub)
	if changed, ok := x.ChangedFiles(); !ok || changed["b.py"].IsZero() || len(changed) != 2 {
		t.Fatalf("a root below the toplevel sees its own changes, root-relative: %v %v", changed, ok)
	}
}

// Review findings on 0.5.22 for the deletion guard: moves under if/try, a
// comment in an import list, Go and Rust methods, a JS default export, a
// same-named file elsewhere, another module's last name, and names quoted
// outside __getattr__ do not count as removed names tests use.
func TestRemovedTestedNoFalseAlarms(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root, _ := gitRepo(t, map[string]string{
		"pkg/__init__.py":        "from .impl import (\n    alpha,\n    beta,\n)\n",
		"pkg/impl.py":            "def alpha():\n    pass\n\n\ndef beta():\n    pass\n",
		"pkg/fixes.py":           "def compat(x):\n    return x\n\n\nOLD = 1\n",
		"pkg/tests/test_api.py":  "from pkg import alpha, beta\nfrom pkg.fixes import compat, OLD\n",
		"bench/datasets.py":      "from pkg.impl import alpha as make_data\n",
		"pkg/tests/test_data.py": "from pkg import datasets\n\n\ndef test_d():\n    datasets.make_data()\n",
		"g/job.go":               "package g\n\ntype job struct{}\n\nfunc (j *job) Run() {}\n\nfunc New() *job { return nil }\n",
		"g/job_test.go":          "package g\n\nimport \"testing\"\n\nfunc TestJ(t *testing.T) { t.Run(\"x\", nil); New() }\n",
		"c/Cargo.toml":           "[package]\nname = \"c\"\n",
		"c/src/foo.rs":           "pub struct Foo;\nimpl Foo {\n    pub fn new() -> Foo { Foo }\n}\n",
		"c/tests/it.rs":          "#[test]\nfn t() { let _ = c::foo::Foo::new(); let _: Vec<u8> = Vec::new(); }\n",
		"web/Button.tsx":         "export default function Button() { return null }\n",
		"web/Button.test.tsx":    "import Button from './Button'\n",
		"web/a/util.ts":          "export function fmt() {}\n",
		"web/b/util.ts":          "export function fmt() {}\n",
		"web/b/util.test.ts":     "import { fmt } from './util'\n",
		"m/mod.py":               "__all__ = [\"keep\", \"gone\"]\n\n\ndef keep():\n    pass\n\n\ndef gone():\n    pass\n\n\ndef __getattr__(name):\n    raise AttributeError(name)\n",
		"m/tests/test_m.py":      "from m.mod import gone\n",
	})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	x := mustOpen(t, root)
	write := func(p, body string) { os.WriteFile(filepath.Join(root, p), []byte(body), 0o644) }
	quiet := func(rel, why string) {
		t.Helper()
		if note, _ := removedTestedNote(x, rel); note != "" {
			t.Fatalf("%s: %s", why, note)
		}
	}
	write("pkg/__init__.py", "from .impl import (\n    alpha,  # re-export\n    beta,\n)\n")
	quiet("pkg/__init__.py", "a comment in the import list")
	write("pkg/fixes.py", "import sys\n\nif sys.version_info >= (3, 12):\n    def compat(x):\n        return x\nelse:\n    def compat(x):\n        return x\n\ntry:\n    from math import OLD\nexcept ImportError:\n    OLD = 1\n")
	quiet("pkg/fixes.py", "names moved under if/try")
	write("bench/datasets.py", "\n")
	quiet("bench/datasets.py", "another module named datasets")
	write("g/job.go", "package g\n\ntype job struct{}\n\nfunc New() *job { return nil }\n")
	quiet("g/job.go", "a removed Go method")
	write("c/src/foo.rs", "pub struct Foo;\n")
	write("c/src/ctor.rs", "impl crate::foo::Foo {\n    pub fn new() -> Self { Self }\n}\n")
	quiet("c/src/foo.rs", "a Rust impl moved to another file")
	write("web/Button.tsx", "function Button() { return null }\nexport default memo(Button)\n")
	quiet("web/Button.tsx", "a default export rewritten")
	write("web/a/util.ts", "export const other = 1\n")
	quiet("web/a/util.ts", "a same-named file in another directory")
	write("m/mod.py", "__all__ = [\"keep\", \"gone\"]\n\n\ndef keep():\n    pass\n\n\ndef __getattr__(name):\n    raise AttributeError(name)\n")
	if note, _ := removedTestedNote(x, "m/mod.py"); !strings.Contains(note, "gone（m/tests/test_m.py）") {
		t.Fatalf("a name quoted only in __all__ is not kept by __getattr__: %q", note)
	}
}

// A version gate is an if/elif comparing a dependency's version inside a
// pytest hook (or feeding collect_ignore at module level) whose block skips:
// the interpreter's version, the platform, a raise, a plain assignment and a
// per-test helper are not.
func TestScanPyGates(t *testing.T) {
	src := `import sys
import pytest
from mylib.fixes import dep_version, parse_version

if parse_version(pytest.__version__) < parse_version("7"):
    raise ImportError("pytest too old")

needs_net = dep_version >= parse_version("1.10")

if dep_version < parse_version("1.0"):
    collect_ignore = ["old_api.py"]


def setup_one():
    if dep_version < parse_version("1.1"):
        raise SkipTest("Skipping one.rst, dep version < 1.1")


def pytest_collection_modifyitems(config, items):
    skip_doctests = False
    if sys.version_info < (3, 9):
        skip_doctests = True
    if dep_version >= parse_version("2"):
        reason = "output repr changed in dep 2"
        skip_doctests = True
    if skip_doctests:
        marker = pytest.mark.skip(reason=reason)
        for item in items:
            if isinstance(item, DoctestItem):
                item.add_marker(marker)
`
	gates := scanPyGates(src)
	if len(gates) != 2 {
		t.Fatalf("want 2 gates, got %d: %+v", len(gates), gates)
	}
	if g := gates[0]; g.Line != 10 || g.Category != gateCatIgnored {
		t.Errorf("module-level collect_ignore gate: %+v", g)
	}
	if g := gates[1]; g.Line != 23 || g.Cond != `if dep_version >= parse_version("2"):` || g.Category != gateCatDoctest || g.Reason != "output repr changed in dep 2" {
		t.Errorf("hook gate over doctests: %+v", g)
	}
}

// A commit is refused once per HEAD while a gate in force was never run
// lifted, even after a broad run; the outline header carries the gates; a
// broad run with the gate lifted, then restored, lets later commits through.
func TestVersionGateCommit(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	conftest := "from mylib.fixes import dep_version, parse_version\n\n\ndef pytest_collection_modifyitems(config, items):\n    if dep_version >= parse_version(\"2\"):\n        marker = pytest.mark.skip(reason=\"doctest output changed\")\n        for item in items:\n            if isinstance(item, DoctestItem):\n                item.add_marker(marker)\n"
	root, git := gitRepo(t, map[string]string{"a.py": "x = 1\n", "conftest.py": conftest})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	Update("a.py", "a.py[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-")
	Update("conftest.py", "conftest.py[3 T ST T]: Role:测试配置 | Uses:- | API:- | Constraints:-")
	doc, _ := Overview("", false)
	if !strings.Contains(doc, "本地验证盲区（自动检出）") || !strings.Contains(doc, "conftest.py:5 `if dep_version >= parse_version(\"2\"):`") {
		t.Fatalf("the header goes out with the gates:\n%s", doc)
	}
	past := time.Now().Add(-time.Minute)
	edit := func(p, body string) {
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
		os.Chtimes(filepath.Join(root, p), past, past)
	}
	edit("a.py", "x = 2\n")
	Gate("Bash", "", "", "python -m pytest -q", "")
	got, _ := Gate("Bash", "", "", "git commit -qam one", "")
	if !strings.Contains(got, `"permissionDecision":"deny"`) || !strings.Contains(got, "整包已经跑过") || !strings.Contains(got, "conftest.py:5") {
		t.Fatalf("a gate never run lifted refuses the commit: %q", got)
	}
	if got, _ := Gate("Bash", "", "", "git commit -qam one", ""); got != "" {
		t.Fatalf("once per HEAD: %q", got)
	}
	git("commit", "-qam", "one")
	edit("a.py", "x = 3\n")
	edit("conftest.py", strings.Replace(conftest, `if dep_version >= parse_version("2"):`, `if False and dep_version >= parse_version("2"):`, 1))
	Gate("Bash", "", "", "python -m pytest -q", "")
	git("checkout", "--", "conftest.py") // restored: no change left
	if got, _ := Gate("Bash", "", "", "git commit -qam two", ""); got != "" {
		t.Fatalf("a broad run with the gate lifted satisfies it: %q", got)
	}
	x := mustOpen(t, root)
	v := loadVerify(root)
	if gs := versionGates(x); len(gs) != 1 || v.GateRuns[gs[0].key()].IsZero() {
		t.Fatalf("the lifted run is recorded per gate: %+v %+v", gs, v.GateRuns)
	}
}

// A test run whose summary skipped everything it selected gets a notice
// after the shell command, with the repository's gates; a run that ran
// anything, or no pytest run, gets none.
func TestAllSkippedNotice(t *testing.T) {
	for _, c := range []struct {
		cmd, out string
		want     int
	}{
		{"python -m pytest pkg --doctest-modules -q", "sss\n17 skipped in 0.21s\n", 17},
		{"pytest -q x.py", "== 2 skipped, 1 warning in 0.05s ==", 2},
		{"pytest -q x.py", "8 passed, 2 skipped in 1.00s", 0},
		{"pytest -q x.py", "1 failed, 3 skipped in 1.00s", 0},
		{"ls", "17 skipped in 0.21s", 0},
	} {
		if got := allSkipped(c.cmd, c.out); got != c.want {
			t.Errorf("%q / %q: %d, want %d", c.cmd, c.out, got, c.want)
		}
	}
	t.Setenv("TMPDIR", t.TempDir())
	root, _ := gitRepo(t, map[string]string{"conftest.py": "def pytest_collection_modifyitems(config, items):\n    if dep_version >= parse_version(\"2\"):\n        skip = True\n"})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	in := base.HookInput{Cwd: root, SessionID: "sk1", ToolName: "Bash", Command: "python -m pytest pkg --doctest-modules", ToolOutput: "17 skipped in 0.21s"}
	got := HookReply("post", in)
	if !strings.Contains(got, "全部被跳过（17 skipped）") || !strings.Contains(got, "conftest.py:2") {
		t.Fatalf("an all-skipped run is called out with the gates:\n%s", got)
	}
	if got := HookReply("post", in); strings.Contains(got, "全部被跳过") {
		t.Fatalf("once per command and session:\n%s", got)
	}
}

// Review findings on 0.5.24: what is and is not a version gate.
func TestScanPyGatesEdges(t *testing.T) {
	cases := []struct {
		name, src string
		want      int
	}{
		{"warning filter", "def pytest_configure(config):\n    if dep_version >= parse_version(\"2\"):\n        warnings.filterwarnings(\"ignore\", category=FutureWarning)\n", 0},
		{"one test xfail", "def pytest_collection_modifyitems(config, items):\n    for item in items:\n        if item.name == \"test_x\" and dep_version < parse_version(\"1.1\"):\n            item.add_marker(pytest.mark.skip(reason=\"old\"))\n", 0},
		{"interpreter", "def pytest_collection_modifyitems(config, items):\n    if PY_VERSION < (3, 10):\n        skip_all = True\n", 0},
		{"skip only in a comment", "def pytest_collection_modifyitems(config, items):\n    if dep_version >= parse_version(\"2\"):\n        x = 1  # skip later\n", 0},
		{"already made false", "def pytest_collection_modifyitems(config, items):\n    if False and dep_version >= parse_version(\"2\"):\n        skip_doctests = True\n", 0},
		{"trailing comment", "def pytest_collection_modifyitems(config, items):\n    if dep_version >= parse_version(\"2\"):  # pragma: no cover\n        skip_doctests = True\n", 1},
		{"module level after a def", "def helper():\n    return 1\n\n\nif dep_version < parse_version(\"1.0\"):\n    collect_ignore = [\"old.py\"]\n", 1},
		{"ignore_collect hook", "def pytest_ignore_collect(collection_path, config):\n    if dep_version < parse_version(\"1.0\"):\n        return True\n", 1},
	}
	for _, c := range cases {
		if got := scanPyGates(c.src); len(got) != c.want {
			t.Errorf("%s: %d gates, want %d: %+v", c.name, len(got), c.want, got)
		}
	}
	if g := scanPyGates(cases[6].src); len(g) == 1 && (g[0].Fn != "" || g[0].Category != gateCatIgnored) {
		t.Errorf("a module-level gate after a def belongs to no def: %+v", g[0])
	}
}

// testpaths scopes the gates a commit requires: a conftest outside them is
// never loaded by a broad run. Toml arrays and ini continuation lines parse.
func TestTestpathsScope(t *testing.T) {
	if got := parseTestpaths("[tool.pytest.ini_options]\ntestpaths = [\n  \"pkg\",\n  \"tests/\",\n]\n", "[tool.pytest.ini_options]"); strings.Join(got, ",") != "pkg,tests" {
		t.Errorf("toml array: %v", got)
	}
	if got := parseTestpaths("[metadata]\nname = x\n[tool:pytest]\ntestpaths =\n    pkg\n    ./more\naddopts = -q\n", "[tool:pytest]"); strings.Join(got, ",") != "pkg,more" {
		t.Errorf("ini continuation: %v", got)
	}
	t.Setenv("TMPDIR", t.TempDir())
	gate := "def pytest_collection_modifyitems(config, items):\n    if dep_version >= parse_version(\"2\"):\n        skip_doctests = True\n"
	root, _ := gitRepo(t, map[string]string{"setup.cfg": "[tool:pytest]\ntestpaths = pkg\n", "pkg/conftest.py": gate, "doc/conftest.py": gate, "pkg/a.py": "x = 1\n"})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	x := mustOpen(t, root)
	if all, req := versionGates(x), requiredGates(x); len(all) != 2 || len(req) != 1 || req[0].File != "pkg/conftest.py" {
		t.Fatalf("only the gate under testpaths is required: all %+v, required %+v", all, req)
	}
}

// A gate whose condition does not hold here is acknowledged by committing
// again after the refusal; a lift and its run in one command count; a
// broad run the write gate refuses never ran; lifting a gate line is no
// planned change; a stale-run refusal still names the gates in force.
func TestVersionGateFlow(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	gate := "def pytest_collection_modifyitems(config, items):\n    if dep_version >= parse_version(\"2\"):\n        marker = pytest.mark.skip(reason=\"doctest output changed\")\n        for item in items:\n            if isinstance(item, DoctestItem):\n                item.add_marker(marker)\n"
	root, git := gitRepo(t, map[string]string{"a.py": "x = 1\n", "b.py": "y = 1\n", "conftest.py": gate})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.py", "b.py"} {
		Update(p, p+"[7 L ST T]: Role:x | Uses:- | API:- | Constraints:-")
	}
	Update("conftest.py", "conftest.py[3 T ST T]: Role:测试配置 | Uses:- | API:- | Constraints:-")
	Overview("", false)
	past := time.Now().Add(-time.Minute)
	edit := func(p, body string) {
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
		os.Chtimes(filepath.Join(root, p), past, past)
	}
	edit("a.py", "x = 2\n")
	Gate("Bash", "", "", "python -m pytest -q 2>&1 | tail -3", "")
	if got, _ := Gate("Bash", "", "", "git commit -qam one", ""); !strings.Contains(got, "整包已经跑过") {
		t.Fatalf("gates-only refusal: %q", got)
	}
	Gate("Bash", "", "", "git commit -qam one", "") // committing again acknowledges it
	git("commit", "-qam", "one")
	x := mustOpen(t, root)
	if v := loadVerify(root); v.GateRuns[requiredGates(x)[0].key()].IsZero() {
		t.Fatalf("the second commit acknowledged the gate: %+v", v.GateRuns)
	}
	edit("a.py", "x = 3\n")
	future := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(root, "a.py"), future, future) // changed after the last broad run
	got, _ := Gate("Bash", "", "", "git commit -qam two", "")
	if !strings.Contains(got, "还没跑过整包测试") || !strings.Contains(got, "仍有按依赖版本整类跳过") || strings.Contains(got, "整包已经跑过") {
		t.Fatalf("a stale run is refused with the gates in force as a reminder, not as a demand: %q", got)
	}
	git("commit", "-qam", "two")

	// a fresh repository state for the in-command lift
	v := loadVerify(root)
	v.GateRuns = map[string]time.Time{}
	saveVerify(root, v)
	Gate("Bash", "", "", `sed -i 's/if dep_version/if False and dep_version/' conftest.py && python -m pytest -q; git checkout -- conftest.py`, "")
	if v := loadVerify(root); v.GateRuns[requiredGates(x)[0].key()].IsZero() {
		t.Fatalf("a lift and its run in one command count: %+v", v.GateRuns)
	}

	before := loadVerify(root).BroadRun
	time.Sleep(20 * time.Millisecond)
	if got, _ := Gate("Bash", "", "", "sed -i s/x/z/ a.py b.py && python -m pytest -q", ""); !strings.Contains(got, `"permissionDecision":"deny"`) {
		t.Fatalf("two unplanned files are refused: %q", got)
	}
	if after := loadVerify(root).BroadRun; !after.Equal(before) {
		t.Fatalf("a refused command never ran: %v -> %v", before, after)
	}
	if got, _ := Gate("Edit", filepath.Join(root, "conftest.py"), "", "", ""); strings.Contains(got, `"permissionDecision":"deny"`) {
		t.Fatalf("lifting a gate line is allowed: %q", got)
	}
	if got, _ := Gate("Edit", filepath.Join(root, "a.py"), "", "", ""); strings.Contains(got, `"permissionDecision":"deny"`) {
		t.Fatalf("the gate file does not count as an unplanned change: %q", got)
	}
}

// A colored pytest summary (--color=yes) is read once colors are removed.
func TestAllSkippedColored(t *testing.T) {
	in := base.ReadHookInput(strings.NewReader(`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"python -m pytest x.py -q"},"tool_response":{"stdout":"\u001b[33m\u001b[1m17 skipped\u001b[0m\u001b[33m in 0.05s\u001b[0m"}}`), time.Second)
	if got := allSkipped(in.Command, in.ToolOutput); got != 17 {
		t.Fatalf("colored summary: %d", got)
	}
}

// A test run with a version gate lifted — in the same command or in the work
// tree — whose summary has failures gets a notice that failures predating
// the change count too; a run with the gate in force, or without failures,
// gets none.
func TestGatedFailuresNotice(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	gate := "def pytest_collection_modifyitems(config, items):\n    if dep_version >= parse_version(\"2\"):\n        skip_doctests = True\n"
	root, _ := gitRepo(t, map[string]string{"conftest.py": gate})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	post := func(session, cmd, out string) string {
		return HookReply("post", base.HookInput{Cwd: root, SessionID: session, ToolName: "Bash", Command: cmd, ToolOutput: out})
	}
	const lift = `sed -i 's/if dep_version/if False and dep_version/' conftest.py && python -m pytest -q; git checkout -- conftest.py`
	if got := post("gf1", lift, "3 failed, 10 passed in 2.00s"); !strings.Contains(got, "汇总里有 3 个失败或错误") {
		t.Fatalf("a lift and its failing run in one command: %q", got)
	}
	if got := post("gf1", lift, "3 failed, 10 passed in 2.00s"); strings.Contains(got, "失败或错误") {
		t.Fatalf("once per command and session: %q", got)
	}
	if got := post("gf1", "python -m pytest -q", "3 failed, 10 passed in 2.00s"); strings.Contains(got, "失败或错误") {
		t.Fatalf("the gate in force: no notice: %q", got)
	}
	os.WriteFile(filepath.Join(root, "conftest.py"), []byte(strings.Replace(gate, "if dep_version", "if False and dep_version", 1)), 0o644)
	if got := post("gf1", "python -m pytest -q --doctest-modules", "== 1 failed, 4 passed, 1 error in 1.00s =="); !strings.Contains(got, "汇总里有 2 个失败或错误") {
		t.Fatalf("lifted in the work tree; errors count too: %q", got)
	}
	if got := post("gf1", "python -m pytest -q -x", "14 passed in 1.00s"); strings.Contains(got, "失败或错误") {
		t.Fatalf("no failures: no notice: %q", got)
	}
}

// depVersions reads each manifest kind; path dependencies and entries
// without a version are left out, comments are dropped, and a key met twice
// keeps both versions.
func TestDepVersions(t *testing.T) {
	cargo := "[package]\nname = \"app\"\nversion = \"1.0.0\"\n\n[workspace.dependencies]\nlinelib = \"0.41.0\"\nlocal = { path = \"crates/local\" }\nnet = { version = \"2.1\", features = [\n  \"tls\",\n] }\nedge = { git = \"https://example.com/edge\", tag = \"v1\", rev = \"abc\" } # see { version = \"9\" }\nquoted = '0.3'\n\n[target.'cfg(unix)'.dependencies]\nsys = \"0.3\"\n\n[dependencies.table]\nversion = \"1.2\"\nbranch = \"main\"\noptional = true\n\n[features]\ndefault = [\"net\"]\n"
	got := depVersions("Cargo.toml", cargo)
	want := map[string]string{"workspace.dependencies\x00linelib": "0.41.0", "workspace.dependencies\x00net": "version=2.1", "workspace.dependencies\x00edge": "rev=abc tag=v1", "workspace.dependencies\x00quoted": "0.3", "target.'cfg(unix)'.dependencies\x00sys": "0.3", "dependencies.table": "branch=main version=1.2"}
	if len(got) != len(want) {
		t.Fatalf("Cargo.toml: %q", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Cargo.toml %q = %q, want %q", k, got[k], v)
		}
	}
	lock := "[[package]]\nname = \"linelib\"\nversion = \"0.41.0\"\nsource = \"registry\"\n\n[[package]]\nname = \"dup\"\nversion = \"2.0.0\"\n\n[[package]]\nname = \"dup\"\nversion = \"1.0.0\"\ndependencies = [\n \"linelib 0.41.0\",\n]\n"
	if got := depVersions("Cargo.lock", lock); got["linelib"] != "0.41.0" || got["dup"] != "1.0.0, 2.0.0" || len(got) != 2 {
		t.Errorf("Cargo.lock: %q", got)
	}
	gomod := "module example.com/app\n\ngo 1.22\n\nrequire example.com/one v1.2.0\n\nrequire (\n\t// v1 pinned until the port\n\texample.com/two v0.3.1 // indirect\n\texample.com/three v2.0.0+incompatible\n)\n"
	if got := depVersions("go.mod", gomod); got["example.com/one"] != "v1.2.0" || got["example.com/three"] != "v2.0.0+incompatible" || len(got) != 2 {
		t.Errorf("go.mod: %q", got)
	}
	pkg := `{"name":"app","version":"1.0.0","dependencies":{"left":"^1.2.0"},"devDependencies":{"tester":"~3.0.0"},"peerDependencies":{"odd":{"x":1}}}`
	if got := depVersions("web/package.json", pkg); got["dependencies\x00left"] != "^1.2.0" || got["devDependencies\x00tester"] != "~3.0.0" || len(got) != 2 {
		t.Errorf("package.json: %q", got)
	}
	py := "[build-system]\nrequires = [\"numlib>=1.25\"]\n\n[project]\nname = \"app\"\nrequires-python = \">=3.9\"\ndependencies = [\n  \"numlib>=1.22\",  # TODO \"numlib>=3.0\"\n  \"Other_Lib[extra] >= 2.0, <3\",\n]\n\n[project.optional-dependencies]\nsecure = [\"numlib>=1.30\"]\n\n[tool.poetry.dependencies]\npython = \"^3.9\"\nframe = \"^4.1\"\nweb = { version = \"^2.28\", extras = [\"socks\"] }\n"
	got = depVersions("pyproject.toml", py)
	want = map[string]string{"build-system.requires\x00numlib": ">=1.25", "project.dependencies\x00numlib": ">=1.22", "project.dependencies\x00other_lib": ">=2.0,<3", "project.optional-dependencies.secure\x00numlib": ">=1.30", "tool.poetry.dependencies\x00frame": "^4.1", "tool.poetry.dependencies\x00web": "version=^2.28"}
	if len(got) != len(want) {
		t.Fatalf("pyproject.toml: %q", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("pyproject.toml %q = %q, want %q", k, got[k], v)
		}
	}
	req := "# pinned\n-r base.txt\nalpha==1.0 ; python_version < \"3.10\"\nalpha==1.1 ; python_version >= \"3.10\"\nbeta>=2\ngamma\n"
	if got := depVersions("requirements-dev.txt", req); got["alpha"] != "==1.0, ==1.1" || got["beta"] != ">=2" || len(got) != 2 {
		t.Errorf("requirements: %q", got)
	}
	for _, p := range []string{"Cargo.toml", "crates/a/Cargo.lock", "go.mod", "web/package.json", "pyproject.toml", "requirements.txt", "requirements-dev.txt", "dev-requirements.txt", "requirements/base.txt"} {
		if !isDepManifest(p) {
			t.Errorf("%s is a manifest", p)
		}
	}
	for _, p := range []string{"package-lock.json", "src/Cargo.rs", "requirements.md", "go.sum", "notes.txt"} {
		if isDepManifest(p) {
			t.Errorf("%s is no manifest", p)
		}
	}
}

// An edit, or a shell command, that moves an existing dependency's version
// against HEAD gets a notice once per set of changes; a new dependency, or a
// second locked version beside the old one, is no move.
func TestDepBumpNotice(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	cargo := "[package]\nname = \"app\"\n\n[workspace.dependencies]\nlinelib = \"0.41.0\"\nnet = \"2.1\"\n"
	lock := "[[package]]\nname = \"linelib\"\nversion = \"0.41.0\"\n\n[[package]]\nname = \"net\"\nversion = \"2.1.0\"\n"
	root, _ := gitRepo(t, map[string]string{"Cargo.toml": cargo, "Cargo.lock": lock, "src/main.rs": "fn main() {}\n"})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) { os.WriteFile(filepath.Join(root, p), []byte(body), 0o644) }
	edit := func(session, p string) string {
		return HookReply("post", base.HookInput{Cwd: root, SessionID: session, ToolName: "Edit", FilePath: filepath.Join(root, p)})
	}
	bash := func(session, cmd string) string {
		return HookReply("post", base.HookInput{Cwd: root, SessionID: session, ToolName: "Bash", Command: cmd})
	}
	write("Cargo.toml", cargo+"extra = \"1.0\"\n")
	if got := edit("d1", "Cargo.toml"); strings.Contains(got, "已有依赖的版本") {
		t.Fatalf("a new dependency is no version change: %q", got)
	}
	write("Cargo.toml", strings.Replace(cargo, `"0.41.0"`, `"0.42.0"`, 1))
	if got := edit("d1", "Cargo.toml"); !strings.Contains(got, "Cargo.toml：linelib 0.41.0 → 0.42.0") {
		t.Fatalf("an edit moving a version: %q", got)
	}
	if got := edit("d1", "Cargo.toml"); strings.Contains(got, "已有依赖的版本") {
		t.Fatalf("once per set of changes: %q", got)
	}
	write("Cargo.lock", strings.Replace(lock, `"0.41.0"`, `"0.42.0"`, 1))
	got := bash("d1", "cargo update -p linelib --precise 0.42.0 --offline")
	if !strings.Contains(got, "Cargo.lock：linelib 0.41.0 → 0.42.0") || strings.Contains(got, "Cargo.toml：linelib") {
		t.Fatalf("a lock update by a package-manager command; the manifest's change was named already: %q", got)
	}
	if got := bash("d1", "ls src"); strings.Contains(got, "已有依赖的版本") {
		t.Fatalf("a command naming no manifest runs no check: %q", got)
	}
	write("Cargo.toml", cargo)
	write("Cargo.lock", lock+"\n[[package]]\nname = \"net\"\nversion = \"3.0.0\"\n")
	if got := bash("d2", "cargo build && git diff --stat Cargo.lock"); strings.Contains(got, "已有依赖的版本") {
		t.Fatalf("a second locked version beside the old one is no move: %q", got)
	}
	// adding a dependency moves the lock's shared packages: no move
	write("Cargo.lock", strings.Replace(lock, `"2.1.0"`, `"2.2.0"`, 1)+"\n[[package]]\nname = \"json\"\nversion = \"1.0.0\"\n")
	if got := bash("d3", "cargo add json"); strings.Contains(got, "已有依赖的版本") {
		t.Fatalf("lock moves that come with an added package: %q", got)
	}
	// a change written before the session's start mark is not this session's
	write("Cargo.lock", lock)
	write("Cargo.toml", strings.Replace(cargo, `"2.1"`, `"2.5"`, 1))
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "Cargo.toml"), past, past)
	base.MarkOverview(root, "start", "")
	if got := bash("d4", "cat Cargo.toml"); strings.Contains(got, "已有依赖的版本") {
		t.Fatalf("a manifest untouched since the session started: %q", got)
	}
	os.Chtimes(filepath.Join(root, "Cargo.toml"), time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	if got := bash("d4", "cat Cargo.toml"); !strings.Contains(got, "net 2.1 → 2.5") {
		t.Fatalf("written after the start mark: %q", got)
	}
}

// Removing a dependency that needed an older locked version leaves the newer
// one: no move.
func TestDepChangesDropVersion(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	lock := "[[package]]\nname = \"hyper\"\nversion = \"0.14.28\"\n\n[[package]]\nname = \"hyper\"\nversion = \"1.3.1\"\n"
	root, _ := gitRepo(t, map[string]string{"Cargo.lock": lock, "Cargo.toml": "[package]\nname = \"a\"\n"})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "Cargo.lock"), []byte("[[package]]\nname = \"hyper\"\nversion = \"1.3.1\"\n"), 0o644)
	if got := depChanges(mustOpen(t, root), []string{"Cargo.lock"}); len(got) != 0 {
		t.Fatalf("a dropped version alone: %q", got)
	}
}

// A broad run wrapped for detaching still reads as one: setsid, nohup and
// sh -c scripts are seen through, line continuations join lines, a cd in a
// child shell stays there; a script file or a narrowed run is not broad.
func TestTestRunKindWrapped(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		cmd   string
		broad bool
	}{
		{`setsid nohup bash -c 'python -m pytest -q > /tmp/full.log 2>&1; echo EXIT $? >> /tmp/full.log' > /dev/null 2>&1 < /dev/null &`, true},
		{`bash -lc "pytest"`, true},
		{`bash -c -l 'pytest'`, true},
		{`bash -O extglob -e -c 'pytest'`, true},
		{`sh -o pipefail -c 'cd sub && cd .. && pytest -x'`, true},
		{`setsid pytest &`, true},
		{"nohup python -m pytest -q \\\n  > /tmp/full.log 2>&1 &", true},
		{`bash -c 'cd /tmp && true' && pytest`, true},
		{`bash run_tests.sh`, false},
		{`sh script.sh -c x`, false},
		{`sh -c 'pytest -k slow'`, false},
		{`bash -c 'cd /tmp && pytest'`, false},
	} {
		if b, _ := testRunKind(c.cmd, root); b != c.broad {
			t.Errorf("%q: broad=%v, want %v", c.cmd, b, c.broad)
		}
	}
	if w, _ := scanTestRun("nohup python -m pytest -q \\\n  > /tmp/full.log 2>&1 &", root); strings.Join(w, " ") != "pytest -q" {
		t.Errorf("a line continuation is no word: %q", w)
	}
	if _, commit := testRunKind(`bash -c 'cd /tmp' && git commit -qam x`, root); !commit {
		t.Error("a cd in a child shell does not move the commit")
	}
	for cmd, want := range map[string]bool{
		"pytest -q &":                      true,
		"setsid nohup bash -c 'make test'": true,
		"FOO=1 nohup pytest":               true,
		"(cd x && make test) &":            true,
		"pytest -q & wait":                 false,
		"pytest -q > log 2>&1":             false,
		"a && b":                           false,
		"cmd &> out.log":                   false,
		`echo "unit & race done"`:          false,
		"go vet ./... |& tail -5":          false,
	} {
		if got := detachedCommand(cmd); got != want {
			t.Errorf("detachedCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// A broad run left in the background is recorded for its session; the
// Stop hook objects once while that run still goes (in this repository,
// started after the record) and code changes are uncommitted — never for
// another session, never while a Stop hook already kept the turn going,
// never without a session, and not once the run is over.
func TestStopWhileBackgroundRun(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc")
	}
	t.Setenv("TMPDIR", t.TempDir())
	root, _ := gitRepo(t, map[string]string{"a.py": "x = 1\n"})
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	stop := func(session string, active bool) string {
		return HookReply("stop", base.HookInput{Cwd: root, SessionID: session, StopHookActive: active})
	}
	marker := fmt.Sprintf("pytest -q --basetemp=probe%d", os.Getpid())
	HookReply("post", base.HookInput{Cwd: root, SessionID: "bg1", ToolName: "Bash", Command: "python -m " + marker, Background: true})
	run := exec.Command("sh", "-c", "sleep 30; : "+marker)
	run.Dir = root
	if err := run.Start(); err != nil {
		t.Skip(err)
	}
	defer run.Process.Kill()
	time.Sleep(150 * time.Millisecond)
	if got := stop("bg1", false); got != "" {
		t.Fatalf("no uncommitted code change: no objection: %q", got)
	}
	os.WriteFile(filepath.Join(root, "a.py"), []byte("x = 2\n"), 0o644)
	for _, c := range []struct {
		session string
		active  bool
	}{{"bg1", true}, {"other", false}, {"", false}} {
		if got := stop(c.session, c.active); got != "" {
			t.Fatalf("session %q active=%v: no objection: %q", c.session, c.active, got)
		}
	}
	if got := stop("bg1", false); !strings.Contains(got, "还在运行") || !strings.Contains(got, marker) {
		t.Fatalf("a background run still going with uncommitted changes: %q", got)
	}
	if got := stop("bg1", false); got != "" {
		t.Fatalf("once per run and session: %q", got)
	}
	run.Process.Kill()
	run.Wait()
	HookReply("post", base.HookInput{Cwd: root, SessionID: "bg1", ToolName: "Bash", Command: "python -m " + marker + " &"})
	if got := stop("bg1", false); got != "" {
		t.Fatalf("the run is over: no objection: %q", got)
	}
	if _, data := base.OverviewMark(root, "bgrun"); data != "" {
		t.Fatalf("a finished run's record is cleared: %q", data)
	}
	HookReply("post", base.HookInput{Cwd: root, SessionID: "bg1", ToolName: "Bash", Command: "python -m pytest -q -k one", Background: true})
	if _, data := base.OverviewMark(root, "bgrun"); data != "" {
		t.Fatalf("a narrowed run is not recorded: %q", data)
	}
}
