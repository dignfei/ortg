package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ortg/base"
)

func pathParserTaskMode(t *testing.T, root string) {
	t.Helper()
	r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_header", "arguments": map[string]any{"mode": "task"}}})[0]
	if r.Result.IsError {
		t.Fatalf("task fixture header: %+v", r)
	}
}

func TestMCPPathParsingSpacesAndParentheses(t *testing.T) {
	folder := "reports/model runner（high）(fast)"
	a, b, spaced := folder+"/README.md", folder+"/selection-index.json", folder+"/read me (draft).md"
	literal, plain := folder+"/literal(note)", folder+"/literal"
	root := updateRepo(t, a, b, spaced, literal, plain)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	pathParserTaskMode(t, root)
	first := strings.Replace(updateLine(a, "single path"), "Uses:-", "Uses:"+b+"（reader, writer）", 1)
	if msg, failed := nativeUpdate(t, root, map[string]any{"path": filepath.Join(root, a), "outline": first}); failed {
		t.Fatalf("single absolute path and literal Uses: %s", msg)
	}
	before, _ := os.ReadFile(filepath.Join(root, base.TableName))
	lines := map[string]string{a: updateLine(a, "batch path"), b: updateLine(b, "batch dependency"), spaced: updateLine(spaced, "space in basename")}
	items := []string{filepath.Join(root, a) + " " + lines[a], b + "\t" + lines[b], spaced + "  " + lines[spaced]}
	if msg, failed := nativeUpdate(t, root, map[string]any{"items": items}); failed {
		t.Fatalf("batch paths and basenames with spaces: %s", msg)
	}
	bak, _ := os.ReadFile(filepath.Join(root, base.TableName+".bak"))
	if !bytes.Equal(bak, before) {
		t.Fatal("batch backup must contain the full pre-batch table")
	}
	for p, want := range lines {
		if row, _ := mustOpen(t, root).Row(p); row.Outline != want {
			t.Fatalf("literal path %s was not stored: %+v", p, row)
		}
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"path": spaced, "outline": updateLine(spaced, "single spaced basename")}); failed {
		t.Fatalf("single path must accept a spaced basename too: %s", msg)
	}
	for _, bad := range [][]string{
		{a + " " + lines[a], filepath.Join(root, a) + " " + lines[a]},
		{a + " " + lines[a], b + " invalid outline"},
	} {
		before, _ := os.ReadFile(filepath.Join(root, base.TableName))
		oldBak, _ := os.ReadFile(filepath.Join(root, base.TableName+".bak"))
		if msg, failed := nativeUpdate(t, root, map[string]any{"items": bad}); !failed || msg == "" {
			t.Fatalf("true duplicate or malformed item must fail: %s", msg)
		}
		after, _ := os.ReadFile(filepath.Join(root, base.TableName))
		afterBak, _ := os.ReadFile(filepath.Join(root, base.TableName+".bak"))
		if !bytes.Equal(before, after) || !bytes.Equal(oldBak, afterBak) {
			t.Fatal("failed batch changed the table or its backup")
		}
	}
	literalUse := strings.Replace(updateLine(a, "literal trailing parentheses"), "Uses:-", "Uses:"+literal, 1)
	if msg, failed := nativeUpdate(t, root, map[string]any{"path": a, "outline": literalUse}); failed {
		t.Fatalf("literal filename ending in parentheses: %s", msg)
	}
	r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_header", "arguments": map[string]any{"ignore_files": []string{literal}}}})[0]
	if r.Result.IsError {
		t.Fatalf("ignore fixture: %+v", r)
	}
	// Header persists roles; reconcile them through the ordinary native scan.
	r = nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_status", "arguments": map[string]any{}}})[0]
	if r.Result.IsError {
		t.Fatalf("ignored fixture reconciliation: %+v", r)
	}
	if msg, failed := nativeUpdate(t, root, map[string]any{"path": a, "outline": literalUse}); !failed || !strings.Contains(msg, literal) {
		t.Fatalf("ignored literal must not be accepted as its unannotated alias: %s", msg)
	}
}

func TestTargetItemsSpacedPaths(t *testing.T) {
	folder := "reports/model runner（high）"
	a, b, c := folder+"/a note.md", folder+"/b.md", folder+"/c.md"
	root := updateRepo(t, a, b, c)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	pathParserTaskMode(t, root)
	for _, p := range []string{a, b, c} {
		if msg, failed := nativeUpdate(t, root, map[string]any{"path": p, "outline": updateLine(p, "original")}); failed {
			t.Fatal(msg)
		}
	}
	planned := "new folder（draft）/future (plan)"
	plannedLine := updateLine(planned, "planned literal")
	aLine := strings.Replace(updateLine(a, "new target"), "Uses:-", "Uses:"+planned, 1)
	items := []string{planned + " " + plannedLine, filepath.Join(root, a) + " " + aLine, b + "\t=", c + " 删除", "bad item"}
	r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_target", "arguments": map[string]any{"items": items}}})[0]
	if r.Result.IsError || len(r.Result.Content) != 1 || !strings.Contains(r.Result.Content[0].Text, "bad：失败") {
		t.Fatalf("partial target success must retain its per-item failure: %+v", r)
	}
	x := mustOpen(t, root)
	for p, want := range map[string]string{a: aLine, planned: plannedLine} {
		if row, _ := x.Row(p); row.Target != want {
			t.Fatalf("spaced/planned target %s: %+v", p, row)
		}
	}
	if row, _ := x.Row(b); row.Target != row.Outline {
		t.Fatalf("spaced path = must keep its outline: %+v", row)
	}
	if row, _ := x.Row(c); !row.TargetDelete {
		t.Fatalf("spaced path deletion must keep its target marker: %+v", row)
	}
}

func TestOutlineItemEmbeddedMarker(t *testing.T) {
	path := "reports/notes]: Role: (draft).md"
	line := updateLine(path, "mentions [7 L ST T]: Role: and ]: Role: markers")
	for _, rest := range []string{line, "=", "删除"} {
		item := path + " " + rest
		gotPath, gotRest := splitOutlineItem(item)
		if gotPath != path || gotRest != rest {
			t.Fatalf("splitOutlineItem(%q) = %q, %q; want literal path and body", item, gotPath, gotRest)
		}
	}
}
