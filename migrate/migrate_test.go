package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ortg/base"
)

const testHeader = "#====T====\n#A层级: E入口 L底层\n#B模块: ST存储 LG逻辑\n#D特征: T-TSV J-JSON\n#E规模: L大 M中 S小 T微"

// writeTable puts body at <root>/ortg.tsv and returns its path.
func writeTable(t *testing.T, root, body string) string {
	t.Helper()
	p := filepath.Join(root, base.TableName)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A table from before the module column is refused by the core, upgraded
// here with an empty module column spliced in, and then loads; the old bytes
// stay in .old.
func TestTableTenColumns(t *testing.T) {
	root := t.TempDir()
	old := "#ORTG-TSV: 1\n" + testHeader + "\n{}\n" + tenColumns + "\n" +
		"logic.go\t00000001\t0\t0\t\tlogic.go[LLG9L]: F:f | R:- | A:- | S:-\t\t\t\t0\n"
	p := writeTable(t, root, old)
	if _, err := base.Open(root); err == nil {
		t.Fatal("the core must not load an old table")
	}
	if up, err := Table(p); err != nil || !up {
		t.Fatalf("ten-column table not upgraded: %v %v", up, err)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatalf("upgraded table rejected: %v", err)
	}
	if r, ok := x.Row("logic.go"); !ok || r.Outline != "logic.go[9 L LG L]: Role:f | Uses:- | API:- | Constraints:-" {
		t.Fatalf("row parsed wrong: %+v", r)
	}
	if b, _ := os.ReadFile(p + ".old"); string(b) != old {
		t.Fatal(".old must hold the old bytes")
	}
}

// A table from before the target rename gets the new column names and the
// new header terms; unrelated header lines keep their words.
func TestTableFuture(t *testing.T) {
	root := t.TempDir()
	old := "#ORTG-TSV: 1\n" + testHeader + "\n#未来规则：条目下一行 #未来: 是未来纲要；#未来:删除 计划删除；#计划新建 文件尚不存在\n#另写的一行:未来纲要用 ortg_future 提交\n{}\n" +
		futureColumns + "\n" +
		"a.go\t00000000\t0\t0\t\t\t\t\ta.go[LST7T]: F:新 | R:- | A:- | S:讲的是未来纲要与ortg_future\t2026-09-01T00:00:00Z\t0\n"
	p := writeTable(t, root, old)
	if up, err := Table(p); err != nil || !up {
		t.Fatalf("future table not upgraded: %v %v", up, err)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	h := x.Header()
	if strings.Contains(h, "未来") || strings.Contains(h, "ortg_future") ||
		!strings.Contains(h, "#目标规则：条目下一行 #目标: 是目标纲要(改完后应有的纲要,不写待核查事项)") ||
		!strings.Contains(h, "#另写的一行:目标纲要用 ortg_target 提交") {
		t.Fatalf("header terms not renamed:\n%s", h)
	}
	if r, ok := x.Row("a.go"); !ok || r.Target != "a.go[7 L ST T]: Role:新 | Uses:- | API:- | Constraints:讲的是未来纲要与ortg_future" {
		t.Fatalf("future_outline not read as the target outline, or its text touched by the header terms: %+v", r)
	}
	if b, _ := os.ReadFile(p + ".old"); string(b) != old {
		t.Fatal(".old must hold the old bytes")
	}
}

// A version-1 table with the current columns gets the current version line,
// its outline and target outline lines in the Role/Uses/API/Constraints
// fields, and the template's format and quota lines in the new names.
func TestTableFields(t *testing.T) {
	root := t.TempDir()
	old := "#ORTG-TSV: 1\n" + testHeader + "\n#【整体规范】\n#纲要格式：文件名[ABCDE]: F:功能 | R:关联 | A:接口 | S:简述 ；文件名不带路径\n" +
		"#骨架规则：只有裸文件名没有纲要的行是尚未建立认知的文件,读取时顺便生成纲要并调用 ortg_update 提交\n#骨架规则：本项目自己写的\n#S配额: C9-8≤1200字\n{}\n" + base.ColumnHeader() + "\n" +
		"a.go\t00000000\t0\t0\t\tST\ta.go[LST7T]: F:甲 | R:b.go | A:Save | S:注意\t\ta.go[LST7T]: F:乙 | R:- | A:- | S:-\t\t0\n"
	p := writeTable(t, root, old)
	if up, err := Table(p); err != nil || !up {
		t.Fatalf("version-1 table not upgraded: %v %v", up, err)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := x.Row("a.go")
	if r.Outline != "a.go[7 L ST T]: Role:甲 | Uses:b.go | API:Save | Constraints:注意" || r.Target != "a.go[7 L ST T]: Role:乙 | Uses:- | API:- | Constraints:-" {
		t.Fatalf("entries not converted: %+v", r)
	}
	h := x.Header()
	if !strings.Contains(h, "Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束") || !strings.Contains(h, "#Constraints配额: C9-8≤1200字") || strings.Contains(h, "S配额") {
		t.Fatalf("header terms not renamed:\n%s", h)
	}
	// no header line is dropped: the rules there are what a model reads
	// when the hooks that inject the rules block are not running
	if !strings.Contains(h, "#【整体规范】") || strings.Count(h, "#骨架规则") != 2 || !strings.Contains(h, "#骨架规则：本项目自己写的") {
		t.Fatalf("header lines must all stay:\n%s", h)
	}
	if got := Entry("x.go[7 L ST T]: Role:已是新格式 | Uses:- | API:- | Constraints:-"); got != "x.go[7 L ST T]: Role:已是新格式 | Uses:- | API:- | Constraints:-" {
		t.Fatalf("a current line must pass through: %q", got)
	}
}

// Tag spells a compact tag out with the digit first; a tag without a module
// has nowhere to put one and is left for the core to report.
func TestTag(t *testing.T) {
	for in, want := range map[string]string{
		"LST8TJAM": "8 L ST T J A M",
		"GLG9L":    "9 G LG L",
		"RD9T":     "9 R D T",
		"L7T":      "L7T",
		"LST8":     "LST8",
		"8 L ST M": "8 L ST M",
	} {
		if got := Tag(in); got != want {
			t.Errorf("Tag(%q) = %q, want %q", in, got, want)
		}
	}
}

// Anything that is not a known old format — a current table, a newer one, a
// broken file — is left untouched.
func TestTableLeavesOthersAlone(t *testing.T) {
	for _, body := range []string{
		base.VersionLine() + "\n{}\n" + base.ColumnHeader() + "\n",
		"#ORTG-TSV: 3\n{}\n" + base.ColumnHeader() + "\n",
		"#ORTG-TSV: 1\n{}\n" + base.ColumnHeader() + "\tnewer\n",
		"garbage",
	} {
		root := t.TempDir()
		p := writeTable(t, root, body)
		if up, err := Table(p); err != nil || up {
			t.Fatalf("Table touched %q: %v %v", body, up, err)
		}
		if b, _ := os.ReadFile(p); string(b) != body {
			t.Fatalf("file rewritten for %q", body)
		}
		if _, err := os.Stat(p + ".old"); err == nil {
			t.Fatalf("no .old expected for %q", body)
		}
	}
}

func TestDocument(t *testing.T) {
	got := Document("#h\n" + volumeMarker + " 1\n#A Layer: R路由\n===ORTG索引 /r/===\na.go[RD9T]: F:a | R:- | A:- | S:-\n  #未来: a.go[RD9T]: F:b | R:- | A:- | S:-\nb.go[RD9T]: F:c | R:- | A:- | S:-\n#未来:删除\n")
	for _, want := range []string{"#A层级: R路由", "===ORTG纲要 /r/===", "\n#目标: a.go[9 R D T]: Role:b | Uses:- | API:- | Constraints:-", "\na.go[9 R D T]: Role:a | Uses:", "\n#目标:删除"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, volumeMarker) || strings.Contains(got, "#未来:") {
		t.Fatalf("old markers left:\n%s", got)
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

const testTemplate = "#===代码纲要规范===\n#A层级: E入口\n#B模块: X\n#C重要度: 9核心\n#D特征: T-TSV\n#E规模: T微\n#S配额: C9-8≤1200字 C7-4≤500字 C3-1≤100字"

// v1Doc renames the meta code dictionaries onto the template skeleton, keeps
// the template #S配额 (the quota the core enforces), drops the database
// dictionaries and the volume marker, and returns false without a code volume.
func TestV1Doc(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "overview.meta.txt"), []byte(testV1Meta), 0o644)
	code := "#ORTG-CODE-VOLUME: 1\n===/x/===\na.go[RD9T]: F:a | R:- | A:- | S:-\n"
	os.WriteFile(filepath.Join(root, "overview.code.txt"), []byte(code), 0o644)
	doc, ok := v1Doc(root, testTemplate)
	if !ok {
		t.Fatal("v1Doc must succeed with both volumes present")
	}
	for _, want := range []string{
		"#A层级: R路由 O文档 X构建",
		"#B模块: D容器 P平台",
		"#C重要度: 9核心 8高频 7业务 5常规 3辅助 1边缘",
		"#D特征: A异步 B构建 AB异步+构建",
		"#E规模: L大>400 T微<100",
		"#S配额: C9-8≤1200字 C7-4≤500字 C3-1≤100字",
		"#===代码纲要规范===",
		"===/x/===",
		"a.go[9 R D T]: Role:a | Uses:- | API:- | Constraints:-",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("doc missing %q:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "E实体") || strings.Contains(doc, volumeMarker) || strings.Contains(doc, "C9-8≤600") {
		t.Fatalf("database dictionary, volume marker or v1 quota leaked:\n%s", doc)
	}
	if _, ok := v1Doc(t.TempDir(), testTemplate); ok {
		t.Fatal("without overview.code.txt v1Doc must return false")
	}
}

// Import moves overview.txt aside before importing it, never clobbering an
// earlier backup, and hands imp the document in the current format.
func TestImportBacksUpOverview(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, OldDoc+".bak"), []byte("earlier"), 0o644)
	os.WriteFile(filepath.Join(root, OldDoc), []byte("a.go[LST7T]: F:a | R:- | A:- | S:-\n#未来:删除\n"), 0o644)
	var seen string
	r := Import(root, testTemplate, func(doc string) (base.ImportResult, error) {
		seen = doc
		return base.ImportResult{Entries: 1}, nil
	})
	if !r.Found || r.FromV1 || r.Err != nil || r.Backup != OldDoc+".bak.2" {
		t.Fatalf("unexpected result: %+v", r)
	}
	if !strings.Contains(seen, "#目标:删除") {
		t.Fatalf("imp got the old markers: %q", seen)
	}
	if _, err := os.Stat(filepath.Join(root, OldDoc)); err == nil {
		t.Fatal("overview.txt must be moved aside")
	}
	if b, _ := os.ReadFile(filepath.Join(root, OldDoc+".bak")); string(b) != "earlier" {
		t.Fatal("earlier backup clobbered")
	}
	if r := Import(t.TempDir(), testTemplate, nil); r.Found {
		t.Fatal("no overview.txt means nothing found")
	}
}
