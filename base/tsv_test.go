package base

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testHeader = "#====T====\n#A层级: E入口 L底层\n#B模块: ST存储 CL命令行\n#D特征: T-TSV J-JSON\n#E规模: L大 M中 S小 T微"

func TestTableRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), TableName)
	tb := newTable(p, testHeader)
	tb.cfg.set("ignore_dirs", []string{"build"})
	tb.cfg.set("custom", map[string]any{"keep": true})
	if err := tb.upsert(&record{Path: "b/x.go", CRC: 0xdeadbeef, Outline: "x.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-", OutlineTime: Now()}); err != nil {
		t.Fatal(err)
	}
	if err := tb.upsert(&record{Path: "a.go", Target: "a.go[9 E ST L]: Role:b | Uses:- | API:- | Constraints:-", TargetTime: Now(), TargetDelete: false}); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(p)
	got, err := openTable(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.header != testHeader || len(got.rows) != 2 || got.rows["b/x.go"].CRC != 0xdeadbeef {
		t.Fatalf("round trip lost data: %+v", got.rows["b/x.go"])
	}
	if !strings.Contains(string(first), `"custom"`) || got.cfg.strings("ignore_dirs")[0] != "build" {
		t.Fatal("config not preserved")
	}
	if err := got.save(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(p)
	if string(first) != string(second) {
		t.Fatal("save is not byte-stable")
	}
	if !strings.HasPrefix(string(first), tsvVersionLine+"\n") || strings.Index(string(first), "a.go\t") > strings.Index(string(first), "b/x.go\t") {
		t.Fatal("rows must be sorted by path after the version line")
	}
}

func TestTableRejectsCorrupt(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"no version": "#x\n\n{}\n\n" + strings.Join(columns, "\t") + "\n",
		"no columns": tsvVersionLine + "\n#x\n\n{}\n\nfoo\n",
		"bad cols":   tsvVersionLine + "\n\n{}\n\n" + strings.Join(columns, "\t") + "\na\tzz\t0\t0\t\t\t\t\t\t0\n",
		"dup path":   tsvVersionLine + "\n\n{}\n\n" + strings.Join(columns, "\t") + "\na\t00000000\t0\t0\t\t\t\t\t\t0\na\t00000000\t0\t0\t\t\t\t\t\t0\n",
	}
	for name, body := range cases {
		p := filepath.Join(dir, name+".tsv")
		os.WriteFile(p, []byte(body), 0o644)
		if _, err := openTable(p); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	tb := newTable("", "")
	if err := tb.upsert(&record{Path: "a", Outline: "has\ttab"}); err == nil {
		t.Error("tab in cell must be rejected")
	}
}

func TestTableToleratesBOMAndCRLF(t *testing.T) {
	p := filepath.Join(t.TempDir(), TableName)
	body := "\uFEFF" + tsvVersionLine + "\r\n#A层级: E\r\n\r\n{}\r\n\r\n" + strings.Join(columns, "\t") + "\r\na\t00000001\t0\t0\t\t\t\t\t\t\t0\r\n"
	os.WriteFile(p, []byte(body), 0o644)
	tb, err := openTable(p)
	if err != nil || tb.rows["a"] == nil || tb.rows["a"].CRC != 1 {
		t.Fatalf("BOM/CRLF input rejected: %v", err)
	}
}

func TestBatchSavesOnceAndLocks(t *testing.T) {
	p := filepath.Join(t.TempDir(), TableName)
	tb := newTable(p, testHeader)
	if err := tb.save(); err != nil {
		t.Fatal(err)
	}
	err := tb.batch(func() error {
		if _, err := os.Stat(p + ".lock"); err != nil {
			t.Error("lock file missing inside batch")
		}
		tb.upsert(&record{Path: "a"})
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("batch error must propagate")
	}
	got, _ := openTable(p)
	if len(got.rows) != 0 {
		t.Fatal("failed batch must not persist rows")
	}
	if _, err := os.Stat(p + ".lock"); err == nil {
		t.Fatal("lock not released")
	}
}

func TestSaveSkipsUnchangedAndKeepsBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), TableName)
	tb := newTable(p, testHeader)
	if err := tb.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Fatal("first save must not create a backup")
	}
	st1, _ := os.Stat(p)
	if err := tb.save(); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(p)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("unchanged content must not be rewritten")
	}
	tb.upsert(&record{Path: "a"})
	bak, err := os.ReadFile(p + ".bak")
	if err != nil || strings.Contains(string(bak), "\na\t") {
		t.Fatalf("backup must hold the previous content: %v", err)
	}
}
