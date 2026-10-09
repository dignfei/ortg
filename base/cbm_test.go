package base

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestParseRelated(t *testing.T) {
	out := []byte(`{"content":[{"type":"text","text":"rows: 6  (cols: a.file_path b.file_path)\n  base/index.go base/tsv.go\n  base/tsv.go base/entry.go\n  base/tsv.go base/tsv.go\n  base/tsv.go base/fsio.go\n  my dir/x.go base/tsv.go\n  base/index.go base/tsv.go\ntotal: 6\n"}],"isError":false}`)
	got := parseRelated(out, "base/tsv.go", 10)
	want := []Relation{{Path: "base/index.go"}, {Path: "base/entry.go", Calls: true}, {Path: "base/fsio.go", Calls: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := parseRelated(out, "base/tsv.go", 1); len(got) != 1 {
		t.Fatalf("limit ignored: %v", got)
	}
	if got := parseRelated([]byte(`{"content":[{"type":"text","text":"{\"error\":\"project not found\"}"}],"isError":true}`), "a.go", 10); got != nil {
		t.Fatalf("error envelope: %v", got)
	}
	if got := parseRelated([]byte("not json"), "a.go", 10); got != nil {
		t.Fatalf("garbage: %v", got)
	}
}

func TestCbmDisabled(t *testing.T) {
	t.Setenv("ORTG_CBM", "off")
	if cbmPath() != "" || cbmRelated("/nonexistent", "a.go", 10, 0) != nil {
		t.Fatal("ORTG_CBM=off must disable CBM")
	}
}

// A hanging CBM whose grandchild holds stdout must not outlive the timeout:
// the pre hook has to finish inside the host's own hook timeout.
func TestCbmRelatedTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	dir := t.TempDir()
	stub := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(filepath.Join(dir, cbmBin), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORTG_CBM", "")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := time.Now()
	if got := cbmRelated(dir, "a.go", 10, 300*time.Millisecond); got != nil {
		t.Fatalf("got %v", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("hung for %v", d)
	}
}

// The daemon must be up before the index run starts, or the index run's
// temporary daemon swallows the start and dies with it.
func TestCbmWarmOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	stub := "#!/bin/sh\necho \"$1 $2\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, cbmBin), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORTG_CBM", "")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := cbmWarm(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if string(b) != "daemon start\ncli index_repository\n" {
		t.Fatalf("got %q", b)
	}
}
