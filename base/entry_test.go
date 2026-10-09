package base

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEntryAndTag(t *testing.T) {
	e, err := parseEntry("tsv.go[8 L ST T J M]: Role:三段编解码 | Uses:base/fsio.go | API:open,save | Constraints:首行版本")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "tsv.go" || e.TA != "L" || e.TB != "ST" || e.TC != 8 || e.TD != "TJ" || e.TE != "M" || e.Uses != "base/fsio.go" {
		t.Fatalf("parsed wrong: %+v", e)
	}
	if _, err := parseEntry("tsv.go[8 L ST T J M]: Role:x | Uses:- | API:-"); err == nil {
		t.Fatal("missing S must fail")
	}
	d := dicts(testHeader)
	if issues := checkTag(e, d); len(issues) != 0 {
		t.Fatalf("valid tag flagged: %v", issues)
	}
	bad, _ := parseEntry("x.go[9 Q ZZ K]: Role:x | Uses:- | API:- | Constraints:-")
	if issues := checkTag(bad, d); len(issues) != 3 {
		t.Fatalf("expected A, B and E issues, got %v", issues)
	}
	if consLen(e) != 4 {
		t.Fatalf("Constraints rune count %d", consLen(e))
	}
	// the digit comes first, every other value is capital letters separated
	// by spaces: one each for layer, traits and scale, one or two for module
	for tag, ok := range map[string]bool{
		"8 L S M": true, "8 L ST M": true, "8 L S T J M": true,
		"LST8TJAM": false, "8 L STX M": false, "8 LS ST M": false, "8 L ST TJ M": false,
		"8 L ST": false, "10 L ST M": false, "0 L ST M": false, "8 L ST  M": false, "8 l st m": false, "8 L  ST M": false, "L ST 8 M": false,
	} {
		if _, _, _, _, _, got := parseTag(tag); got != ok {
			t.Errorf("parseTag(%q) ok=%v, want %v", tag, got, ok)
		}
	}
}

// A v1 code dictionary renamed to v2 keys (#A Layer: → #A层级: …) must parse
// under dicts(), and compact tags whose D segment is a compound symbol (AB)
// must pass checkTag's letter-by-letter judgement.
func TestDictsAndCheckTagV1CompoundD(t *testing.T) {
	hdr := "#====T====\n#A层级: X构建 O文档 R路由\n#B模块: D容器 P平台\n#C重要度: 9核心 8高频 7业务 5常规 3辅助 1边缘\n#D特征: A异步 B构建 AB异步+构建\n#E规模: L大>400 T微<100"
	d := dicts(hdr)
	e, err := parseEntry("docker-compose.k3s.yml[9 X D A B T]: Role:编排 | Uses:- | API:- | Constraints:-")
	if err != nil {
		t.Fatal(err)
	}
	if e.TA != "X" || e.TB != "D" || e.TC != 9 || e.TD != "AB" || e.TE != "T" {
		t.Fatalf("tag parsed wrong: %+v", e)
	}
	if issues := checkTag(e, d); len(issues) != 0 {
		t.Fatalf("compound D must pass letter-wise: %v", issues)
	}
	// the original v1 spelling parses under the same regex
	if len(dicts("#A Layer: R路由 O文档 X构建")["A"]) != 3 {
		t.Fatal("#A Layer: lines must parse under dicts()")
	}
}

func TestDocumentRoundTrip(t *testing.T) {
	doc := testHeader + "\n\n#代码纲要\n\n===项目根 /r/===\nmain.go[9 E ST T]: Role:入口 | Uses:- | API:main | Constraints:-\n#目标: main.go[9 E ST T]: Role:新入口 | Uses:- | API:- | Constraints:-\n\n===底层 /r/base/===\ntsv.go[8 L ST T J M]: Role:a | Uses:- | API:- | Constraints:-\nold.go[3 L ST T]: Role:b | Uses:- | API:- | Constraints:-\n#目标:删除\n"
	d, err := parseDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Header != testHeader+"\n#代码纲要" || len(d.Sections) != 2 || d.Sections[0].Dir != "/r/" || d.Sections[1].Dir != "/r/base/" {
		t.Fatalf("document parsed wrong: %+v", d)
	}
	if d.Sections[0].Entries[0].Target == "" || !d.Sections[1].Entries[1].TargetDelete {
		t.Fatalf("target lines not attached: %+v", d.Sections)
	}
	if _, err := parseDocument("===x /r/===\nnot an entry\n"); err == nil {
		t.Fatal("bad entry line must fail")
	}
	rows := []Row{
		{Path: "main.go", Outline: "main.go[9 E ST T]: Role:入口 | Uses:- | API:main | Constraints:-", Target: "main.go[9 E ST T]: Role:新入口 | Uses:- | API:- | Constraints:-", Exists: true},
		{Path: "base/tsv.go", Outline: "tsv.go[8 L ST T J M]: Role:a | Uses:- | API:- | Constraints:-", Exists: true},
		{Path: "base/new.go", Target: "new.go[5 L ST T]: Role:c | Uses:- | API:- | Constraints:-", Exists: false},
		{Path: "base/skel.go", Exists: true},
		{Path: "hidden.go", Ignored: true, Exists: true},
	}
	out := render(testHeader, rows, RenderOptions{Root: "/r", DirTitle: map[string]string{".": "项目根"}, Include: func(r Row) bool { return !r.Ignored },
		Prefix: Prefixes{Planned: "#计划新建 ", Target: "#目标: ", TargetDelete: "#目标:删除", Body: "#代码纲要"}})
	for _, want := range []string{"===项目根 /r/===", "===/r/base/===", "#计划新建 new.go", "#目标: new.go[5 L ST T]", "\nskel.go\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hidden.go") {
		t.Fatal("ignored row rendered")
	}
	back, err := parseDocument(out)
	if err != nil {
		t.Fatalf("rendered text must parse back: %v\n%s", err, out)
	}
	real, planned := 0, 0
	for _, sec := range back.Sections {
		for _, e := range sec.Entries {
			if e.Line != "" {
				real++
			}
			if e.Name == "new.go" && e.Target != "" {
				planned++
			}
		}
	}
	if real != 2 || planned != 1 {
		t.Fatalf("round trip: real=%d planned=%d", real, planned)
	}
}

func TestSetHeaderDropsBlankLinesAndReloads(t *testing.T) {
	root := t.TempDir()
	x, err := Create(root, testHeader)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.SetHeader("#====X====\n#A层级: E入口\n\n#B模块: ST存储\n#E规模: T微\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err != nil {
		t.Fatalf("table with header containing a blank line must still load: %v", err)
	}
	if x.SetHeader("#A层级: E\nno hash\n#B模块: X\n#E规模: T") == nil {
		t.Fatal("non-# header line must be rejected")
	}
	if !x.Excluded("build/x", ReconcileOptions{ExcludeDirs: []string{"build"}}) || x.Excluded("a.go", ReconcileOptions{}) {
		t.Fatal("Excluded wrong")
	}
}

// ---- ImportOutlineWith split-by-existence tests ----

// empty project (no code files on disk) → all entries go to Target.
func TestImportEmptyProjectAllTarget(t *testing.T) {
	root := t.TempDir()
	x := NewMemIndex(root, testHeader)
	doc := testHeader + "\n===根 " + filepath.ToSlash(root) + "/===\na.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-\nb.go[7 L ST T]: Role:b | Uses:- | API:- | Constraints:-\n"
	res, err := x.ImportOutline(doc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries != 2 || len(res.Missing) != 0 {
		t.Fatalf("entries=%d missing=%v", res.Entries, res.Missing)
	}
	for _, p := range []string{"a.go", "b.go"} {
		r, ok := x.Row(p)
		if !ok || r.Target == "" || r.Outline != "" {
			t.Fatalf("%s: expected Target only, got %+v", p, r)
		}
	}
}

// project has code, all entries exist → all to Outline, no Missing.
func TestImportAllExistToOutline(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package b"), 0o644)
	x := NewMemIndex(root, testHeader)
	doc := testHeader + "\n===根 " + filepath.ToSlash(root) + "/===\na.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-\nb.go[7 L ST T]: Role:b | Uses:- | API:- | Constraints:-\n"
	res, err := x.ImportOutline(doc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries != 2 || len(res.Missing) != 0 {
		t.Fatalf("entries=%d missing=%v", res.Entries, res.Missing)
	}
	for _, p := range []string{"a.go", "b.go"} {
		r, ok := x.Row(p)
		if !ok || r.Outline == "" || r.Target != "" {
			t.Fatalf("%s: expected Outline only, got %+v", p, r)
		}
	}
	// fingerprint set → not stale, not skeleton
	f, _ := x.Inspect("a.go")
	if f.OutlineEmpty || f.FingerprintChanged {
		t.Fatalf("a.go fingerprint wrong: empty=%v changed=%v", f.OutlineEmpty, f.FingerprintChanged)
	}
}

// split: some exist, some don't → Outline for existing, Target+Missing for absent.
func TestImportSplitByExistence(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	x := NewMemIndex(root, testHeader)
	doc := testHeader + "\n===根 " + filepath.ToSlash(root) + "/===\na.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-\nb.go[7 L ST T]: Role:b | Uses:- | API:- | Constraints:-\n"
	res, err := x.ImportOutline(doc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries != 2 || len(res.Missing) != 1 || res.Missing[0] != "b.go" {
		t.Fatalf("entries=%d missing=%v", res.Entries, res.Missing)
	}
	ra, _ := x.Row("a.go")
	if ra.Outline == "" || ra.Target != "" {
		t.Fatalf("a.go should be in Outline: %+v", ra)
	}
	rb, _ := x.Row("b.go")
	if rb.Target == "" || rb.Outline != "" {
		t.Fatalf("b.go should be in Target: %+v", rb)
	}
}

// Chinese file names: existence check uses filepath.FromSlash correctly.
func TestImportChinesePaths(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "模块"), 0o755)
	os.WriteFile(filepath.Join(root, "模块", "入口.go"), []byte("package m"), 0o644)
	x := NewMemIndex(root, testHeader)
	doc := testHeader + "\n===模块 " + filepath.ToSlash(root) + "/模块/===\n入口.go[7 L ST T]: Role:入口 | Uses:- | API:- | Constraints:-\n缺失.go[7 L ST T]: Role:缺 | Uses:- | API:- | Constraints:-\n"
	res, err := x.ImportOutline(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "模块/缺失.go" {
		t.Fatalf("missing=%v", res.Missing)
	}
	r, _ := x.Row("模块/入口.go")
	if r.Outline == "" {
		t.Fatal("existing Chinese-named file must go to Outline")
	}
}

// path remap (5487fe4) combined with split logic.
func TestImportRemapWithSplit(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "base"), 0o755)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	// base/b.go does NOT exist on disk → should go to Target + Missing.
	x := NewMemIndex(root, testHeader)
	doc := testHeader + "\n===根 /old/root/===\na.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-\n" +
		"===底层 /old/root/base/===\nb.go[7 L ST T]: Role:b | Uses:- | API:- | Constraints:-\n"
	res, err := x.ImportOutline(doc)
	if err != nil {
		t.Fatalf("ImportOutline: %v", err)
	}
	if res.Entries != 2 {
		t.Fatalf("entries=%d", res.Entries)
	}
	ra, _ := x.Row("a.go")
	if ra.Outline == "" {
		t.Fatal("a.go should be in Outline (remapped + exists)")
	}
	rb, _ := x.Row("base/b.go")
	if rb.Target == "" || rb.Outline != "" {
		t.Fatalf("base/b.go should be in Target (remapped + missing): %+v", rb)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "base/b.go" {
		t.Fatalf("missing=%v", res.Missing)
	}
}

// #目标: annotations
// are preserved correctly in split mode.
func TestImportTargetAnnotationsPreserved(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package b"), 0o644)
	x := NewMemIndex(root, testHeader)
	doc := testHeader + "\n===根 " + filepath.ToSlash(root) + "/===\n" +
		"a.go[7 L ST T]: Role:current | Uses:- | API:- | Constraints:-\n" +
		"#目标: a.go[7 L ST T]: Role:target | Uses:- | API:- | Constraints:-\n" +
		"b.go[7 L ST T]: Role:doomed | Uses:- | API:- | Constraints:-\n" +
		"#目标:删除\n"
	res, err := x.ImportOutline(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 0 {
		t.Fatalf("both files exist, missing should be empty: %v", res.Missing)
	}
	ra, _ := x.Row("a.go")
	if ra.Outline != "a.go[7 L ST T]: Role:current | Uses:- | API:- | Constraints:-" {
		t.Fatalf("a.go outline wrong: %q", ra.Outline)
	}
	if ra.Target != "a.go[7 L ST T]: Role:target | Uses:- | API:- | Constraints:-" {
		t.Fatalf("a.go target wrong: %q", ra.Target)
	}
	rb, _ := x.Row("b.go")
	if rb.Outline != "b.go[7 L ST T]: Role:doomed | Uses:- | API:- | Constraints:-" {
		t.Fatalf("b.go outline wrong: %q", rb.Outline)
	}
	if !rb.TargetDelete {
		t.Fatal("b.go should have TargetDelete=true")
	}
}

// The optional Keys field follows Constraints and is not part of it; the
// header's contract table splits key from text at the first full-width colon.
func TestKeysFieldAndContracts(t *testing.T) {
	e, err := parseEntry("a.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:必须保持 x | y | Keys:max_matches(执行), route:POST /v1/o(提供)")
	if err != nil || e.Constraints != "必须保持 x | y" || fmt.Sprint(splitItems(e.Keys)) != "[max_matches route:POST /v1/o]" {
		t.Fatalf("constraints %q keys %q %v", e.Constraints, e.Keys, err)
	}
	c := contracts("#A层级: E入口\n#【契约】route:POST /v1/o：qty 以件计\n#【契约】坏行没有冒号")
	if len(c) != 1 || c["route:POST /v1/o"] != "qty 以件计" {
		t.Fatalf("contracts: %v", c)
	}
}

// Test names come only from the parentheses of "必须保持" clauses; those
// clauses are counted apart from the other constraints.
func TestGuardTestsAndContractLen(t *testing.T) {
	line := "a.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:必须保持 限 1 条带 2 行上下文得 4:d(tests::context_after_match, regression::issue42)；必须保持 空输入无输出(TestEmpty、另见 b.go)；缓冲至少 8K(见 buffer.rs)"
	if got := fmt.Sprint(testNames(line)); got != "[tests::context_after_match regression::issue42 TestEmpty]" {
		t.Fatalf("tests: %s", got)
	}
	e, _ := parseEntry(line)
	want := 0
	for _, cl := range clauses(e.Constraints)[:2] {
		want += len([]rune(cl))
	}
	if c := contractLen(e); c != want {
		t.Fatalf("contract clauses count %d runes, want %d", c, want)
	}
}

// Only words that name a test count: a helper type with "Test" in its name
// and a bare "tests" do not.
func TestIsTestName(t *testing.T) {
	for w, want := range map[string]bool{
		"tests::basic": true, "regression::issue42": true, "core::tests::max_items*": true,
		"TestEmpty": true, "Test_x": true, "test_parse": true, "parse_test": true,
		"ParserTester": false, "tests": false, "test": false, "Testament": false, "latest": false,
	} {
		if isTestName(w) != want {
			t.Errorf("isTestName(%q) = %v, want %v", w, !want, want)
		}
	}
}
