package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"ortg/base"
)

func TestPolicyCandidatesDiscovery(t *testing.T) {
	for _, p := range []string{
		"CONTRIBUTING.md", "HACKING.rst", "DEVELOPMENT.txt", "development-guide.md",
		"CODING_STANDARDS.md", "coding-guidelines.txt", "developer_guide.rst",
		".github/CONTRIBUTING.md", "doc/contributing.rst", "docs/DEVELOPMENT_GUIDE.md",
		"docs/contribute/guide.md", "docs/contributing/README.txt", "docs/development/index.rst",
		"docs/development/contributing.md",
	} {
		if !policyDocumentPath(p) {
			t.Errorf("standard path not recognized: %s", p)
		}
	}
	for _, p := range []string{
		"AGENTS.md", "CLAUDE.md", "README.md", "CONTRIBUTORS.md", "CONTRIBUTING.go", "CONTRIBUTING.adoc",
		"vendor/CONTRIBUTING.md", "third_party/DEVELOPMENT.md", "fixtures/CONTRIBUTING.md",
		"tests/docs/contributing.md", "docs/fixtures/CONTRIBUTING.md", "docs/testdata/DEVELOPMENT.md",
		"bundles/docs/development/index.rst", "docs/development/examples/sample.md",
		".github/development.md", "docs/development/AGENTS.md",
	} {
		if policyDocumentPath(p) {
			t.Errorf("non-policy source accepted: %s", p)
		}
	}
	root := updateRepo(t, "CONTRIBUTING.md", "HACKING.rst", "DEVELOPMENT.txt", "docs/contribute/index.md",
		"fixtures/CONTRIBUTING.md", "vendor/CONTRIBUTING.md", "AGENTS.md", "CLAUDE.md")
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header(testHeader, nil, []string{"HACKING.rst"}, nil, []string{"docs/contribute"}, []string{"DEVELOPMENT.txt"}, nil, "task"); err != nil {
		t.Fatal(err)
	}
	x := mustOpen(t, root)
	if _, err := reconcileAndApply(x); err != nil {
		t.Fatal(err)
	}
	// An existing file outside the active inventory must not be discovered.
	writeFile(t, filepath.Join(root, "CODING_STANDARDS.md"), "unindexed policy body")
	before, config := x.Rows(), x.Config()
	got := policyCandidates(x)
	want := fmt.Sprintf(policyCandidateLine, strconv.Quote("CONTRIBUTING.md"), policyCandidateUnknown)
	if !strings.Contains(got, want) || !strings.Contains(got, fmt.Sprintf(policyCandidatesCount, 1, 0)) {
		t.Fatalf("missing actual unknown path: %s", got)
	}
	for _, excluded := range []string{"HACKING", "DEVELOPMENT", "contribute/", "fixtures/", "vendor/", "AGENTS", "CLAUDE", "CODING_STANDARDS", "unindexed policy body"} {
		if strings.Contains(got, excluded) {
			t.Errorf("included excluded/unindexed source %q: %s", excluded, got)
		}
	}
	if !reflect.DeepEqual(before, x.Rows()) || !reflect.DeepEqual(config, x.Config()) {
		t.Fatal("discovery mutated inventory or roles")
	}
	cfg := x.Config()
	cfg.Mode = "build"
	if err := x.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if policyCandidates(x) != "" {
		t.Fatal("build must retain its staged document workflow")
	}
	empty := base.NewMemIndex(t.TempDir(), testHeader)
	cfg = empty.Config()
	cfg.Mode = "task"
	if err := empty.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if policyCandidates(empty) != "" {
		t.Fatal("empty inventory must not invent candidates")
	}
}

func TestPolicyCandidatesBounds(t *testing.T) {
	check := func(rows []base.Row) []string {
		t.Helper()
		text := renderPolicyCandidates(rows)
		if len(text) > 2048 || !utf8.ValidString(text) {
			t.Fatalf("index not bounded UTF8: %d bytes", len(text))
		}
		var paths []string
		for _, line := range strings.Split(text, "\n") {
			if !strings.HasPrefix(line, "- ") {
				continue
			}
			quoted, err := strconv.QuotedPrefix(line[2:])
			if err != nil {
				t.Fatal(err)
			}
			p, err := strconv.Unquote(quoted)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				found = found || row.Path == p
			}
			if !found {
				t.Fatalf("path truncated or invented: %q", p)
			}
			paths = append(paths, p)
		}
		if len(paths) > 8 || !strings.Contains(text, fmt.Sprintf(policyCandidatesCount, len(paths), len(rows)-len(paths))) {
			t.Fatalf("incorrect displayed/omitted count: %s", text)
		}
		return paths
	}
	var rows []base.Row
	for i := 0; i < 14; i++ {
		rows = append(rows, base.Row{Path: fmt.Sprintf("docs/contributing_%02d.md", i)})
	}
	if got := check(rows); len(got) != 8 {
		t.Fatalf("cardinality bound: %v", got)
	}
	rows = []base.Row{
		{Path: "000/" + strings.Repeat("长", 900) + "/CONTRIBUTING.md"},
		{Path: "a/开发 \"指南\" (参考)\\line\nCONTRIBUTING.md"},
		{Path: "b/" + strings.Repeat("文", 230) + "/development.md", Outline: "summary"},
		{Path: "c/" + strings.Repeat("字", 230) + "/contributing.md"},
		{Path: "z/HACKING.md"},
	}
	paths := check(rows)
	if len(paths) < 2 || paths[0] != rows[1].Path || paths[len(paths)-1] != rows[len(rows)-1].Path {
		t.Fatalf("oversized entry must be skipped and later complete paths considered: %q", paths)
	}
	first := renderPolicyCandidates(rows)
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if first != renderPolicyCandidates(rows) {
		t.Fatal("candidate order depends on input enumeration")
	}
}

func TestPolicyCandidatesDelivery(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", host)
			useTestMCPSession(t)
			root := updateRepo(t, "CONTRIBUTING.md", "other.go")
			if _, err := Scan(); err != nil {
				t.Fatal(err)
			}
			if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil, "task"); err != nil {
				t.Fatal(err)
			}
			outline := updateLine("CONTRIBUTING.md", "contributor guide")
			if _, err := Update("CONTRIBUTING.md", outline); err != nil {
				t.Fatal(err)
			}
			if _, err := Overview("*", false); err != nil {
				t.Fatal(err)
			}
			// A genuine persisted same-SID cache from before this index:
			// keep the already delivered header, rows, rules and other nags.
			c := sentFor(root)
			for key := range c.nagged {
				if strings.HasPrefix(key, "policy-candidates:") {
					delete(c.nagged, key)
				}
			}
			c.nagged["module:ST"] = true
			saveSent(root, c)
			delete(overviewSent, root+"\x00"+testMCPSession)
			get := func(module string, force bool, sid string) string {
				t.Helper()
				r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{
					"name": "ortg_overview", "arguments": map[string]any{"module": module, "force": force}, "_meta": map[string]any{"threadId": sid},
				}})[0]
				if r.Result.IsError || len(r.Result.Content) != 1 {
					t.Fatalf("native overview: %+v", r)
				}
				return r.Result.Content[0].Text
			}
			check := func(out string, present bool) {
				t.Helper()
				count := 0
				if present {
					count = 1
				}
				if strings.Count(out, policyCandidatesHead) != count {
					t.Fatalf("want candidates=%v: %s", present, out)
				}
				if present && strings.Index(out, policyCandidatesHead) > strings.LastIndex(out, "以上") {
					t.Fatal("candidate index displaced closing footer")
				}
			}
			out := get("ST", false, testMCPSession)
			check(out, true)
			if !strings.HasSuffix(out, overviewShortFooter) || strings.Contains(out, outline) || !strings.Contains(out, policyCandidateKnown) {
				t.Fatal("081 upgrade must augment the restored short reply without repeating outlines")
			}
			check(get("ST", false, testMCPSession), false)
			if restored := loadSent(root, testMCPSession); !restored.nagged["module:ST"] {
				t.Fatal("candidate delivery erased another namespace")
			}
			x := mustOpen(t, root)
			if err := x.SetOutline("CONTRIBUTING.md", ""); err != nil {
				t.Fatal(err)
			}
			out = get("ST", false, testMCPSession)
			check(out, true)
			if !strings.Contains(out, policyCandidateUnknown) {
				t.Fatal("known-to-unknown state change was suppressed")
			}
			if _, err := Update("CONTRIBUTING.md", outline); err != nil {
				t.Fatal(err)
			}
			check(get("ST", false, testMCPSession), true) // even a previously seen payload returns
			check(get("ST", true, testMCPSession), true)
			check(get("ST", false, testMCPSession), false)
			markReset(root, testMCPSession, "", "compact")
			check(get("ST", false, testMCPSession), true)
			check(get("ST", false, testMCPSession), false)
			if host == "codex" {
				check(get("ST", false, "separate-neutral-session"), true)
			}
			if _, err := Header("", nil, nil, nil, nil, nil, nil, "build"); err != nil {
				t.Fatal(err)
			}
			check(get("ST", false, testMCPSession), false)
			if _, err := Header("", nil, nil, nil, nil, nil, nil, "task"); err != nil {
				t.Fatal(err)
			}
			check(get("ST", false, testMCPSession), true)
		})
	}
}

func TestPolicyCandidatesBudget(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", host)
			root := updateRepo(t, "CONTRIBUTING.md", "owned.go")
			if _, err := Scan(); err != nil {
				t.Fatal(err)
			}
			if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil, "task"); err != nil {
				t.Fatal(err)
			}
			if _, err := Update("owned.go", updateLine("owned.go", "owned")); err != nil {
				t.Fatal(err)
			}
			originalC, originalX := claudeLimits, codexLimits
			t.Cleanup(func() { claudeLimits, codexLimits = originalC, originalX })
			x := mustOpen(t, root)
			body := x.Render(renderOpts(x, ""))
			policy := policyCandidates(x)
			// Only the new index takes this otherwise fitting whole reply
			// over the configured whole-outline budget.
			lim := originalC
			if host == "codex" {
				lim = originalX
			}
			lim.whole = outlineSize(body+policy) - 1
			claudeLimits, codexLimits = lim, lim
			out, err := Overview("", false)
			if err != nil || !strings.HasSuffix(out, overviewModulesFooter) || !strings.Contains(out, policyCandidatesHead) {
				t.Fatalf("candidate bytes must count toward whole budget: %v", err)
			}
			// Start the next request with the index still missing, as on an
			// upgrade from a persisted header/row cache without this payload.
			for key := range sentFor(root).nagged {
				if strings.HasPrefix(key, "policy-candidates:") {
					delete(sentFor(root).nagged, key)
				}
			}
			// The actual complete reply (including rules/report/footer) has
			// no room. It must defer the index without remembering delivery.
			tiny := hostLimits{max: 1, whole: 1, unit: lim.unit}
			claudeLimits, codexLimits = tiny, tiny
			out, err = Overview("*", true)
			if err != nil || strings.Contains(out, policyCandidatesHead) {
				t.Fatalf("candidate must defer when actual reply cannot fit: %v", err)
			}
			c := sentFor(root)
			for key := range c.nagged {
				if strings.HasPrefix(key, "policy-candidates:") {
					t.Fatal("deferred candidate marked delivered")
				}
			}
			claudeLimits, codexLimits = originalC, originalX
			out, err = Overview("ST", false)
			if err != nil || !strings.Contains(out, policyCandidatesHead) {
				t.Fatalf("deferred candidate was lost: %v", err)
			}
			out, err = Overview("ST", false)
			if err != nil || strings.Contains(out, policyCandidatesHead) || !strings.HasSuffix(out, overviewShortFooter) {
				t.Fatalf("unchanged candidate not quiet: %v", err)
			}
		})
	}
}
