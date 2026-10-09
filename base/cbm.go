package base

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// The local codebase-memory-mcp (CBM) binary is an optional helper: it keeps a
// call graph of the repository that says which files call into which. ORTG
// only ever shells out to its one-shot `cli` mode; a missing binary, a failed
// or slow query, or an unindexed project all degrade to "no relations" and
// never change what ORTG itself decides.
const cbmBin = "codebase-memory-mcp"

// Relation is one cross-file call edge seen from the queried file: Calls means
// the queried file calls into Path, otherwise Path calls into it.
type Relation struct {
	Path  string
	Calls bool
}

// cbmPath returns the binary, or "" when CBM is disabled (ORTG_CBM=off, which
// the tests set so they never index their sandboxes) or not installed.
func cbmPath() string {
	if os.Getenv("ORTG_CBM") == "off" {
		return ""
	}
	p, err := exec.LookPath(cbmBin)
	if err != nil {
		return ""
	}
	return p
}

// cbmProject is ORTG's own project name for root, so the graph it refreshes
// and the graph it queries are always the same one.
func cbmProject(root string) string {
	return fmt.Sprintf("ortg-%08x", crc32.ChecksumIEEE([]byte(root)))
}

func cbmIndexCmd(ctx context.Context, bin, root string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, "cli", "index_repository",
		"--repo-path", root, "--name", cbmProject(root), "--mode", "fast")
}

// cbmWarm brings CBM's permanent daemon up, then re-indexes root, in that
// order and synchronously; it is meant to run in a detached process (see
// Index.Spawn), never in a hook. The order matters: a one-shot `cli` command
// starts a temporary daemon that exits with its last client, and a
// `daemon start` arriving while it runs only answers "already active" — the
// daemon then dies with the index run and every later query cold-starts
// (2.7s on a fast machine, past the pre hook's query timeout on a small
// server). `daemon start` is idempotent, so running this at every session
// start also brings the daemon back after a reboot or container restart.
func cbmWarm(root string) error {
	bin := cbmPath()
	if bin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	err := exec.CommandContext(ctx, bin, "daemon", "start").Run()
	cancel()
	if err != nil {
		return fmt.Errorf("cbm daemon start: %w", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := cbmIndexCmd(ctx, bin, root).Run(); err != nil {
		return fmt.Errorf("cbm index %s: %w", root, err)
	}
	return nil
}

var cbmMu sync.Mutex
var cbmBusy, cbmDirty = map[string]bool{}, map[string]bool{}

// cbmRefresh re-indexes root in the background of a long-lived process (the
// MCP server): refreshes run one at a time per root, and requests that
// arrive meanwhile collapse into one more run.
func cbmRefresh(root string) {
	bin := cbmPath()
	if bin == "" {
		return
	}
	cbmMu.Lock()
	defer cbmMu.Unlock()
	if cbmBusy[root] {
		cbmDirty[root] = true
		return
	}
	cbmBusy[root] = true
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			if err := cbmIndexCmd(ctx, bin, root).Run(); err != nil {
				Debugf("cbm refresh %s: %v", root, err)
			}
			cancel()
			cbmMu.Lock()
			if !cbmDirty[root] {
				cbmBusy[root] = false
				cbmMu.Unlock()
				return
			}
			cbmDirty[root] = false
			cbmMu.Unlock()
		}
	}()
}

// cbmRelated asks the graph for the files rel calls into and the files that
// call into rel. Self edges are dropped; the result is in CBM's order,
// deduplicated per direction, nil on any failure and non-nil (maybe empty)
// on success, so a caller can tell "no callers" from "not known".
func cbmRelated(root, rel string, limit int, timeout time.Duration) []Relation {
	bin := cbmPath()
	if bin == "" {
		return nil
	}
	lit := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(rel) + `"`
	q := "MATCH (a)-[:CALLS]->(b) WHERE a.file_path = " + lit + " OR b.file_path = " + lit +
		" RETURN DISTINCT a.file_path, b.file_path LIMIT 200"
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "cli", "--json", "query_graph",
		"--project", cbmProject(root), "--query", q)
	// Killing the child on timeout does not close a stdout pipe a grandchild
	// still holds; without this bound Output waits for it and the hook overruns
	// the host's own timeout, losing the outline injection with it.
	cmd.WaitDelay = 200 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		Debugf("cbm query %s: %v", rel, err)
		return nil
	}
	return parseRelated(out, rel, limit)
}

// parseRelated reads query_graph's JSON envelope, whose text is a table:
// "rows: N  (cols: ...)", one indented row per line, then "total: N".
// Rows that do not split into exactly two paths (a path with a space) are
// skipped rather than guessed at.
func parseRelated(out []byte, rel string, limit int) []Relation {
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(out, &env) != nil || env.IsError || len(env.Content) == 0 {
		return nil
	}
	rels := []Relation{}
	seen := map[Relation]bool{}
	for _, line := range strings.Split(env.Content[0].Text, "\n") {
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || f[0] == f[1] {
			continue
		}
		var r Relation
		switch rel {
		case f[0]:
			r = Relation{Path: f[1], Calls: true}
		case f[1]:
			r = Relation{Path: f[0]}
		default:
			continue
		}
		if seen[r] {
			continue
		}
		seen[r] = true
		rels = append(rels, r)
		if len(rels) == limit {
			break
		}
	}
	return rels
}
