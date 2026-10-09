package base

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// A row the scan stopped enumerating but whose file is still on disk survives
// only when it carries knowledge. A bare skeleton row is leftover noise and
// must be dropped; an outlined row must be kept (a gitignored file with an
// outline is not a tombstone).
func TestUnenumeratedRowsKeepKnowledgeDropNoise(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	// Both files are on disk but hidden from git, so listFiles never returns
	// them and only the table's rows can produce a fact.
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("kept.go\nnoise.go\n"), 0o644)
	for _, name := range []string{"kept.go", "noise.go"} {
		os.WriteFile(filepath.Join(root, name), []byte("package p"), 0o644)
	}
	x, err := Create(root, testHeader)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kept.go", "noise.go"} {
		if err := x.AddSkeleton(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.SetOutline("kept.go", "kept.go[9 E ST T]: Role:有认知 | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	facts, err := x.Reconcile(ReconcileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Fact{}
	for _, f := range facts {
		got[f.Path] = f
	}
	if f, ok := got["kept.go"]; !ok || !f.Exists || f.Ignored || f.OutlineEmpty {
		t.Fatalf("outlined row must survive as an existing file: %+v", f)
	}
	if f, ok := got["noise.go"]; !ok || !f.Ignored {
		t.Fatalf("bare skeleton row must be reported ignored so Purge drops it: %+v", f)
	}
}

// keep_files re-includes the few files that matter inside an ignored
// directory — including one that is a repository of its own, which the
// discovery walk must therefore still enter. The caller's structural
// exclusions stay absolute.
func TestKeepFilesOverrideIgnoreRules(t *testing.T) {
	root := t.TempDir()
	git := func(dir string, args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	initRepo := func(dir string) {
		os.MkdirAll(dir, 0o755)
		git(dir, "init", "-q")
		git(dir, "config", "user.email", "t@t")
		git(dir, "config", "user.name", "t")
	}
	initRepo(root)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/*/\n"), 0o644)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	initRepo(filepath.Join(root, "composetp"))
	os.WriteFile(filepath.Join(root, "composetp", "docker-compose.yml"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "composetp", "junk.sh"), []byte("y"), 0o644)

	x, err := Create(root, testHeader)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.SetConfig(Config{
		IgnoreDirs: []string{"composetp"},
		KeepFiles:  []string{"composetp/docker-compose.yml"},
	}); err != nil {
		t.Fatal(err)
	}
	opts := ReconcileOptions{ExcludeFiles: []string{TableName}}
	facts, err := x.Reconcile(opts)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range facts {
		if !f.Ignored {
			seen[f.Path] = true
		}
	}
	if !seen["composetp/docker-compose.yml"] {
		t.Fatalf("kept file inside an ignored nested repository must survive: %v", seen)
	}
	if seen["composetp/junk.sh"] {
		t.Fatalf("only the kept file may come back: %v", seen)
	}
	if x.Excluded("composetp/docker-compose.yml", opts) {
		t.Fatal("Excluded must honour keep_files too")
	}
	if !x.Excluded(TableName, opts) {
		t.Fatal("the caller's structural exclusions stay absolute")
	}
}

// Dependents finds the rows whose R names a file: by path, or by a file name
// unique in the table; an ambiguous name, a module name or the file itself
// does not count.
func TestDependents(t *testing.T) {
	root := t.TempDir()
	x := NewMemIndex(root, testHeader)
	for p, line := range map[string]string{
		"a/util.go": "util.go[5 L ST T]: Role:u | Uses:- | API:- | Constraints:-",
		"b/util.go": "util.go[5 L ST T]: Role:u2 | Uses:- | API:- | Constraints:-",
		"core.go":   "core.go[5 L ST T]: Role:c | Uses:- | API:- | Constraints:-",
		"x.go":      "x.go[5 L ST T]: Role:x | Uses:core.go,a/util.go | API:- | Constraints:-",
		"y.go":      "y.go[5 L ST T]: Role:y | Uses:util.go、ST | API:- | Constraints:-",
		"z.go":      "z.go[5 L ST T]: Role:z | Uses:core.go/ | API:- | Constraints:-",
		"w.go":      "w.go[5 L ST T]: Role:w | Uses:- | API:- | Constraints:core.go 里的东西",
	} {
		if err := x.SetOutline(p, line); err != nil {
			t.Fatal(err)
		}
	}
	if got := x.Dependents("core.go"); !reflect.DeepEqual(got, []string{"x.go", "z.go"}) {
		t.Fatalf("core.go dependents by unique name: %v", got)
	}
	if got := x.Dependents("a/util.go"); !reflect.DeepEqual(got, []string{"x.go"}) {
		t.Fatalf("a/util.go by path only; the ambiguous bare name must not match: %v", got)
	}
	if got := x.Dependents("x.go"); got != nil {
		t.Fatalf("nobody depends on x.go: %v", got)
	}
}

// UsesItems splits on commas and enumeration commas and cuts parenthetical
// notes; "-" gives nothing.
func TestUsesItems(t *testing.T) {
	line := "a.go[7 L ST T]: Role:a | Uses:x/y.go(Sink, Sunk)，z/、w.go（注） | API:- | Constraints:-"
	if got := fmt.Sprint(UsesItems(line)); got != "[x/y.go z/ w.go]" {
		t.Fatalf("items: %s", got)
	}
	if n := UsesItems("a.go[7 L ST T]: Role:a | Uses:- | API:- | Constraints:-"); len(n) != 0 {
		t.Fatalf("- names nothing: %v", n)
	}
}
