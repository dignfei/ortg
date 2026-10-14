package base

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCheckEntryRequiresExplicitFields(t *testing.T) {
	x := NewMemIndex(t.TempDir(), testHeader)
	valid := "a.go[5 L ST T]: Role:职责 | Uses:- | API:- | Constraints:-"
	for _, field := range []string{"Role", "Uses", "API", "Constraints"} {
		value := "-"
		if field == "Role" {
			value = "职责"
		}
		for _, blank := range []string{"", "  ", "\u3000"} {
			t.Run(fmt.Sprintf("%s_%q", field, blank), func(t *testing.T) {
				line := strings.Replace(valid, field+":"+value, field+":"+blank, 1)
				got := x.CheckEntry("a.go", line)
				want := field + " 字段不能为空，无内容写 -"
				if !reflect.DeepEqual(got.Errors, []string{want}) {
					t.Fatalf("errors: %v; want %q", got.Errors, want)
				}
			})
		}
	}
	for _, line := range []string{valid, strings.Replace(valid, "Role:职责", "Role:-", 1), valid + " | Keys:unit:money(消费方)"} {
		if got := x.CheckEntry("a.go", line); len(got.Errors) != 0 {
			t.Fatalf("explicit fields must be accepted: %+v", got)
		}
	}
	line := strings.Replace(valid, "Constraints:-", "Constraints: | Keys:unit:money(消费方)", 1)
	if got := x.CheckEntry("a.go", line); !reflect.DeepEqual(got.Errors, []string{"Constraints 字段不能为空，无内容写 -"}) {
		t.Fatalf("Keys must not conceal an empty Constraints field: %+v", got)
	}
}

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

func TestUsesPathsPreserveLiteralParentheses(t *testing.T) {
	root := t.TempDir()
	x := NewMemIndex(root, testHeader)
	literal := "folder（中文）/a (data).go"
	trailing := "a(note)"
	planned := "new folder（draft）/future (plan)"
	for _, p := range []string{literal, trailing, "a", planned} {
		if err := x.SetTarget(p, filepath.Base(p)+"[5 L ST T]: Role:planned fixture | Uses:- | API:- | Constraints:-"); err != nil {
			t.Fatal(err)
		}
	}
	dir := "folder (shared)（公共）"
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	line := "consumer.go[5 L ST T]: Role:fixture | Uses:" + literal + "（Sink(nested), Sunk）," + trailing + "(consumer)," + planned + "," + dir + "/（使用） | API:- | Constraints:- | Keys:unit:money(消费, 提供)"
	want := []string{literal, trailing, planned, dir + "/"}
	if got := x.UsesItems(line); !reflect.DeepEqual(got, want) {
		t.Fatalf("literal and planned paths: %v, want %v", got, want)
	}
	if got := KeysOf(line); !reflect.DeepEqual(got, []string{"unit:money"}) {
		t.Fatalf("Keys role parsing changed: %v", got)
	}
	if err := x.SetOutline("consumer.go", line); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{literal, trailing, planned, dir + "/nested.go"} {
		if got := x.Dependents(p); !reflect.DeepEqual(got, []string{"consumer.go"}) {
			t.Fatalf("dependency edge lost for %s: %v", p, got)
		}
	}
	if got := x.Dependents("a"); len(got) != 0 {
		t.Fatalf("literal filename must not become another dependency: %v", got)
	}
	pure := "consumer.go[5 L ST T]: Role:fixture | Uses:" + literal + "（Sink(nested), Sunk） | API:- | Constraints:-"
	if got := UsesItems(pure); !reflect.DeepEqual(got, []string{literal}) {
		t.Fatalf("path-internal parentheses were stripped: %v", got)
	}
	x.t.get(trailing).Ignored = true
	if got := x.UsesItems(line)[1]; got != trailing || x.ResolvesUses(got) {
		t.Fatalf("an ignored literal must be refused, not silently rebound: %q", got)
	}
}
