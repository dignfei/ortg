package base

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIgnoredPatterns(t *testing.T) {
	cases := []struct {
		rel   string
		dirs  []string
		files []string
		want  bool
	}{
		{"build/x", []string{"build"}, nil, true},
		{"builder/x", []string{"build"}, nil, false},
		{"ortg.tsv.bak", nil, []string{"ortg.tsv.*"}, true},
		{"a/b/overview.txt", nil, []string{"overview*.txt"}, true},
		{"docs/a/b.md", nil, []string{"docs/**/*.md"}, true},
		{"docs/b.md", nil, []string{"docs/**/*.md"}, true},
		{"src/b.md", nil, []string{"docs/**/*.md"}, false},
		{"x/中文.go", nil, []string{"*.go"}, true},
		{"测试目录/中文.go", nil, []string{"测试目录/**"}, true},
		{"测试目录/中文.go", nil, []string{"中?.go"}, true},
	}
	for _, c := range cases {
		if got := ignored(c.rel, c.dirs, c.files); got != c.want {
			t.Errorf("ignored(%q)=%v want %v", c.rel, got, c.want)
		}
	}
}

func TestRel(t *testing.T) {
	root := t.TempDir()
	if r, ok := Rel(root, filepath.Join(root, "a", "b.go")); !ok || r != "a/b.go" {
		t.Fatalf("Rel inside: %q %v", r, ok)
	}
	if _, ok := Rel(root, "/etc/hosts"); ok {
		t.Fatal("outside path reported inside")
	}
	if r, ok := Rel(root, root); !ok || r != "." {
		t.Fatalf("Rel root: %q", r)
	}
}

func TestListFilesGitAndDiff(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	os.WriteFile(filepath.Join(root, "中文.go"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(root, "gone.go"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0o644)
	os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("c"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	os.Remove(filepath.Join(root, "gone.go"))
	os.WriteFile(filepath.Join(root, "new.go"), []byte("d"), 0o644)
	files, err := listFiles(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, f := range files {
		has[f] = true
	}
	if !has["中文.go"] || !has["new.go"] || has["gone.go"] || has["ignored.txt"] {
		t.Fatalf("listFiles wrong: %v", files)
	}
	fp, _ := fingerprintFile(filepath.Join(root, "中文.go"))
	rows := map[string]*record{"中文.go": {Path: "中文.go", CRC: fp.CRC}, "gone.go": {Path: "gone.go"}, "new.go": {Path: "new.go", CRC: 1}}
	disk := map[string]fingerprint{"中文.go": fp, "new.go": {CRC: 2}, "extra.go": {CRC: 3}}
	d := diff(rows, disk)
	if len(d.New) != 1 || d.New[0] != "extra.go" || len(d.Missing) != 1 || d.Missing[0] != "gone.go" || len(d.Changed) != 1 || d.Changed[0] != "new.go" {
		t.Fatalf("diff wrong: %+v", d)
	}
	if err := ExcludeLocal(root, []string{"ortg.tsv.*"}); err != nil {
		t.Fatal(err)
	}
	ExcludeLocal(root, []string{"ortg.tsv.*"})
	b, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if n := len(regexpCount(string(b), "ortg.tsv.*")); n != 1 {
		t.Fatalf("exclude written %d times", n)
	}
}

func regexpCount(s, sub string) []int {
	var idx []int
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			idx = append(idx, i)
		}
	}
	return idx
}

// A repository nested under a directory the root .gitignore hides must still be
// enumerated: git never descends into a nested work tree, and the parent's
// ignore rules do not govern the child. Deeper nesting must surface too, and
// skipDir must prune the discovery walk.
func TestListFilesFindsNestedRepos(t *testing.T) {
	root := t.TempDir()
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
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
	os.WriteFile(filepath.Join(root, "top.go"), []byte("a"), 0o644)
	// sub/ is a repository of its own, hidden from the root git by /*/
	initRepo(filepath.Join(root, "sub"))
	os.WriteFile(filepath.Join(root, "sub", "in_sub.go"), []byte("b"), 0o644)
	// plain/deeper/ is a repository two levels down, under a plain directory
	initRepo(filepath.Join(root, "plain", "deeper"))
	os.WriteFile(filepath.Join(root, "plain", "deeper", "deep.go"), []byte("c"), 0o644)
	// skipped/ is a repository the caller prunes
	initRepo(filepath.Join(root, "skipped"))
	os.WriteFile(filepath.Join(root, "skipped", "no.go"), []byte("d"), 0o644)

	// sub/ declares vendor/ not its own code, and a repository is checked in
	// there: the enclosing repository's rules win, so it must stay invisible.
	os.WriteFile(filepath.Join(root, "sub", ".gitignore"), []byte("vendor/\n"), 0o644)
	initRepo(filepath.Join(root, "sub", "vendor", "dep"))
	os.WriteFile(filepath.Join(root, "sub", "vendor", "dep", "dep.go"), []byte("e"), 0o644)

	files, err := listFiles(root, func(rel string) bool { return rel == "skipped" })
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, f := range files {
		has[f] = true
	}
	for _, want := range []string{"top.go", "sub/in_sub.go", "plain/deeper/deep.go"} {
		if !has[want] {
			t.Fatalf("nested repository file %s not enumerated: %v", want, files)
		}
	}
	if has["skipped/no.go"] {
		t.Fatalf("skipDir did not prune the discovery walk: %v", files)
	}
	if has["sub/vendor/dep/dep.go"] {
		t.Fatalf("a repository the enclosing repository ignores must stay out: %v", files)
	}
}

// Within one rule set the last match wins and "!" negates — gitignore's
// semantics — so a hole can be carved in a broad rule, and a hole in the hole
// still works. Directory rules are scanned before file rules.
func TestRuleNegationLastMatchWins(t *testing.T) {
	cases := []struct {
		name  string
		dirs  []string
		files []string
		rel   string
		want  bool
	}{
		{"plain dir rule", []string{"vendor"}, nil, "vendor/x.go", true},
		{"file rule carves a hole in a dir rule", []string{"vendor"}, []string{"!vendor/keep.go"}, "vendor/keep.go", false},
		{"…and only that hole", []string{"vendor"}, []string{"!vendor/keep.go"}, "vendor/other.go", true},
		{"glob then negation", nil, []string{"composetp/**", "!composetp/vendor/**"}, "composetp/a.yml", true},
		{"negation wins for the subtree", nil, []string{"composetp/**", "!composetp/vendor/**"}, "composetp/vendor/x.yml", false},
		{"hole in the hole", nil, []string{"composetp/**", "!composetp/vendor/**", "composetp/vendor/critical.yml"}, "composetp/vendor/critical.yml", true},
		{"order matters: negation first, positive later", nil, []string{"!a.go", "a.go"}, "a.go", true},
		{"order matters: positive first, negation later", nil, []string{"a.go", "!a.go"}, "a.go", false},
		{"negating a dir rule from the dir list", []string{"**/vendor", "!composetp/vendor"}, nil, "composetp/vendor/x", false},
	}
	for _, c := range cases {
		if got := ignored(c.rel, c.dirs, c.files); got != c.want {
			t.Errorf("%s: ignored(%q, %v, %v) = %v, want %v", c.name, c.rel, c.dirs, c.files, got, c.want)
		}
	}
	// A negation is not a reason to walk into an otherwise excluded directory.
	if keepsUnder("vendor", []string{"!vendor/x.go"}) {
		t.Fatal("a negated keep must not keep the discovery walk inside a directory")
	}
	if !keepsUnder("composetp", []string{"composetp/docker-compose.yml"}) {
		t.Fatal("a real keep target must keep the walk inside the directory")
	}
}
