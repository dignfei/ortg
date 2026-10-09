package base

import (
	"bytes"
	"context"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// TableName is the data file name at the repository root.
const TableName = "ortg.tsv"

// FindRepoRoot walks up from start looking for ortg.tsv, then falls back to
// the git root, then to start itself.
func FindRepoRoot(start string) string {
	abs, err := filepath.Abs(start)
	if err != nil {
		abs = start
	}
	for d := abs; ; d = filepath.Dir(d) {
		if st, err := os.Stat(filepath.Join(d, TableName)); err == nil && !st.IsDir() {
			return d
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	if g, ok := GitRoot(abs); ok {
		return g
	}
	return abs
}

// GitRoot returns the git work tree containing dir.
func GitRoot(dir string) (string, bool) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", false
	}
	return filepath.Clean(strings.TrimSpace(string(out))), true
}

func foldCase(s string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(s)
	}
	return s
}

// Rel converts an absolute path to a slash-separated path relative to root
// and reports whether it lies inside root. Case is folded only for comparison.
func Rel(root, abs string) (string, bool) {
	a, err := filepath.Abs(abs)
	if err != nil {
		return "", false
	}
	r, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	a, r = filepath.ToSlash(filepath.Clean(a)), filepath.ToSlash(filepath.Clean(r))
	if foldCase(a) == foldCase(r) {
		return ".", true
	}
	prefix := strings.TrimSuffix(r, "/") + "/"
	if !strings.HasPrefix(foldCase(a), foldCase(prefix)) {
		return "", false
	}
	return a[len(prefix):], true
}

// ExcludeLocal appends missing patterns to .git/info/exclude when root has a
// .git directory.
func ExcludeLocal(root string, patterns []string) error {
	gitDir := filepath.Join(root, ".git")
	if st, err := os.Stat(gitDir); err != nil || !st.IsDir() {
		return nil
	}
	p := filepath.Join(gitDir, "info", "exclude")
	existing, _ := os.ReadFile(p)
	have := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, pat := range patterns {
		if !have[pat] {
			add = append(add, pat)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	text := string(existing)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return os.WriteFile(p, []byte(text+strings.Join(add, "\n")+"\n"), 0o644)
}

// gitList runs git ls-files inside dir and returns the paths it reports,
// relative to dir. It reports false when dir is not usable as a repository.
func gitList(dir string) ([]string, bool) {
	cmd := exec.Command("git", "-c", "core.quotepath=false", "-c", "core.fsmonitor=false", "-C", dir,
		"ls-files", "-z", "-t", "--cached", "--others", "--exclude-standard")
	raw, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var names []string
	for _, item := range bytes.Split(raw, []byte{0}) {
		if len(item) < 3 || item[0] == 'R' {
			continue
		}
		names = append(names, strings.TrimPrefix(string(item[2:]), "./"))
	}
	return names, true
}

// hasGitDir reports whether dir is the top of a git work tree (.git is a
// directory for a normal clone, a file for a worktree or submodule).
func hasGitDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// gitIgnores reports whether the repository at dir ignores the path rel
// (relative to dir). A repository that cannot answer is treated as not
// ignoring, so a git failure can only widen the search, never narrow it.
func gitIgnores(dir, rel string) bool {
	cmd := exec.Command("git", "-c", "core.fsmonitor=false", "-C", dir, "check-ignore", "-q", "--", rel)
	return cmd.Run() == nil
}

// nestedRepos walks root and returns the relative paths of the git work trees
// inside it. The walk keeps descending into a repository it found so that
// repositories nested deeper still surface; skipDir prunes a subtree.
//
// A repository living in a directory that the ENCLOSING repository ignores is
// skipped along with its subtree: a project that declares vendor/ or
// node_modules/ not its own code means that for the repositories checked in
// there too. The root is exempt from that test — ORTG's scope is the tree it
// was pointed at, and a root .gitignore routinely hides the first-level
// subprojects (a bare /*/ is common), which is the very blindness this
// discovery walk exists to cure.
func nestedRepos(root string, skipDir func(rel string) bool) ([]string, error) {
	var repos []string
	// enclosing returns the deepest already-found repository containing rel,
	// or "" when only the root encloses it.
	enclosing := func(rel string) string {
		best := ""
		for _, r := range repos {
			if strings.HasPrefix(rel, r+"/") && len(r) > len(best) {
				best = r
			}
		}
		return best
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, ok := Rel(root, p)
		if !ok || rel == "." {
			return nil
		}
		if skipDir != nil && skipDir(rel) {
			return filepath.SkipDir
		}
		if !hasGitDir(p) {
			return nil
		}
		if owner := enclosing(rel); owner != "" &&
			gitIgnores(filepath.Join(root, filepath.FromSlash(owner)), strings.TrimPrefix(rel, owner+"/")) {
			return filepath.SkipDir
		}
		repos = append(repos, rel)
		return nil
	})
	sort.Strings(repos)
	return repos, err
}

// listFiles enumerates regular files under root as slash-separated relative
// paths. Every git repository under root — the root itself plus any nested one
// — is enumerated by its own git (tracked plus untracked, honoring that
// repository's ignores). Nested repositories are found by walking the file
// system rather than by asking the parent's git: git never descends into a
// nested work tree, and the parent's ignore rules do not govern a child
// repository, so a root .gitignore that hides the subdirectory must not hide
// the repository living in it. skipDir prunes the discovery walk and may be
// nil. A root that is no repository at all falls back to a plain walk.
func listFiles(root string, skipDir func(rel string) bool) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(rel string) {
		if rel == "" || seen[rel] {
			return
		}
		st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !st.Mode().IsRegular() {
			return
		}
		seen[rel] = true
		out = append(out, rel)
	}
	rootIsRepo := false
	if _, ok := GitRoot(root); ok {
		if names, ok := gitList(root); ok {
			rootIsRepo = true
			for _, n := range names {
				add(n)
			}
		}
	}
	nested, walkErr := nestedRepos(root, skipDir)
	enumerated := map[string]bool{}
	for _, rel := range nested {
		names, ok := gitList(filepath.Join(root, filepath.FromSlash(rel)))
		if !ok {
			continue
		}
		enumerated[rel] = true
		for _, n := range names {
			add(rel + "/" + n)
		}
	}
	if rootIsRepo {
		sort.Strings(out)
		return out, walkErr
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, ok := Rel(root, p)
		if !ok {
			return nil
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			// A nested repository was already enumerated by its own git; its
			// ignored files must not come back in through the plain walk.
			// Nothing else is pruned here: a file the caller ignores must still
			// be listed, so its stale row can be recognised and purged.
			if d.Name() == ".git" || enumerated[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			add(rel)
		}
		return nil
	})
	sort.Strings(out)
	if err == nil {
		err = walkErr
	}
	return out, err
}

var (
	globMu    sync.Mutex
	globCache = map[string]*regexp.Regexp{}
)

// globRegexp compiles a pattern where ** spans slashes, * stays within one
// segment and ? matches one character.
func globRegexp(pattern string) *regexp.Regexp {
	globMu.Lock()
	defer globMu.Unlock()
	if re, ok := globCache[pattern]; ok {
		return re
	}
	var b strings.Builder
	b.WriteString("^")
	rs := []rune(pattern) // rune-wise: byte-wise would mangle non-ASCII path segments
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '*' && i+1 < len(rs) && rs[i+1] == '*':
			i++
			if i+1 < len(rs) && rs[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	globCache[pattern] = re
	return re
}

// ignored reports whether rel matches a directory prefix or a file pattern.
// keepsUnder reports whether any keep pattern could match something inside rel,
// so a directory the rules exclude is still worth walking. A pattern without a
// slash matches by basename and one with ** spans directories, so either could
// hit anywhere; anything else must name rel as its prefix.
func keepsUnder(rel string, keeps []string) bool {
	for _, k := range keeps {
		if strings.HasPrefix(k, "!") {
			continue // a negation is not a reason to walk into a directory
		}
		k = strings.TrimPrefix(strings.TrimPrefix(k, "./"), "/")
		if k == "" {
			continue
		}
		if !strings.Contains(k, "/") || strings.Contains(k, "**") || strings.HasPrefix(k, rel+"/") {
			return true
		}
	}
	return false
}

func ignored(rel string, dirs, files []string) bool {
	hit := false
	scan := func(pats []string, match func(pat, rel string) bool) {
		for _, p := range pats {
			neg := strings.HasPrefix(p, "!")
			q := strings.TrimSpace(strings.TrimPrefix(p, "!"))
			if q == "" {
				continue
			}
			if match(q, rel) {
				hit = !neg
			}
		}
	}
	scan(dirs, matchDir)
	scan(files, matchFile)
	return hit
}

// matchDir matches a directory rule: the path is the directory itself or lives
// under it.
func matchDir(pat, rel string) bool {
	pat = strings.Trim(pat, "/")
	return pat != "" && (rel == pat || strings.HasPrefix(rel, pat+"/"))
}

// matchFile matches a file rule: an exact path, a bare name matched against the
// basename, or a glob (** spans directories, * does not; a pattern without a
// slash is also tried against the basename).
func matchFile(pat, rel string) bool {
	if strings.ContainsAny(pat, "*?") {
		re := globRegexp(pat)
		return re.MatchString(rel) || (!strings.Contains(pat, "/") && re.MatchString(path.Base(rel)))
	}
	return rel == pat || path.Base(rel) == pat
}

type fingerprint struct {
	CRC   uint32
	Mtime time.Time
	Size  int64
}

// fingerprintFile streams the file through CRC32 (IEEE) and records mtime.
func fingerprintFile(p string) (fingerprint, error) {
	st, err := os.Stat(p)
	if err != nil {
		return fingerprint{}, err
	}
	if st.IsDir() {
		// A folder outline's row: it exists while the directory does and never
		// goes stale — the files under it carry their own fingerprints.
		return fingerprint{}, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return fingerprint{}, err
	}
	defer f.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return fingerprint{}, err
	}
	return fingerprint{CRC: h.Sum32(), Mtime: st.ModTime().UTC().Truncate(time.Second), Size: st.Size()}, nil
}

type diffResult struct{ New, Missing, Changed []string }

// diff compares table rows with on-disk fingerprints: New is on disk only,
// Missing is in the table only, Changed has a different CRC.
func diff(rows map[string]*record, disk map[string]fingerprint) diffResult {
	var d diffResult
	for p, fp := range disk {
		r, ok := rows[p]
		switch {
		case !ok:
			d.New = append(d.New, p)
		case r.CRC != fp.CRC:
			d.Changed = append(d.Changed, p)
		}
	}
	for p := range rows {
		if _, ok := disk[p]; !ok {
			d.Missing = append(d.Missing, p)
		}
	}
	sort.Strings(d.New)
	sort.Strings(d.Missing)
	sort.Strings(d.Changed)
	return d
}

// headContent returns rel's content as committed at HEAD of the repository
// holding root; false when root is in no repository or HEAD lacks the file
// (a new file).
func headContent(root, rel string) (string, bool) {
	out, err := exec.Command("git", "-c", "core.fsmonitor=false", "-C", root, "show", "HEAD:./"+rel).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// gitTimeout bounds each git helper below: they run inside hooks and the
// write gate, which must never hang the host.
const gitTimeout = 5 * time.Second

// gitOutput runs git in root under gitTimeout; ok is false on any failure.
// It takes no optional lock (git status would otherwise hold index.lock
// while it refreshes the index, and a killed status leaves the lock behind
// for the commit that follows), and a timeout interrupts git so it can clean
// up before it is killed.
func gitOutput(root string, args ...string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", root}, args...)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	return out, err == nil
}

// gitChanged maps the files under root that differ from HEAD in the work
// tree and are still on disk — modified, added, renamed and untracked (not
// ignored), never deleted ones or directories — to their modification
// times; ok is false when root is no repository or git fails. Porcelain
// paths are relative to the toplevel, so a root below it strips its prefix.
func gitChanged(root string) (map[string]time.Time, bool) {
	pre, ok := gitOutput(root, "rev-parse", "--show-prefix")
	if !ok {
		return nil, false
	}
	prefix := strings.TrimSpace(string(pre))
	out, ok := gitOutput(root, "status", "--porcelain", "-z", "--untracked-files=all", "--", ".")
	if !ok {
		return nil, false
	}
	files := map[string]time.Time{}
	recs := strings.Split(string(out), "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if len(r) < 4 {
			continue
		}
		st, p := r[:2], r[3:]
		if st[0] == 'R' || st[0] == 'C' {
			i++ // the next record is the source path
		}
		if strings.Contains(st, "D") || !strings.HasPrefix(p, prefix) {
			continue
		}
		p = p[len(prefix):]
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil || fi.IsDir() {
			continue
		}
		files[p] = fi.ModTime()
	}
	return files, true
}

// gitHead returns HEAD's commit id and commit time; ok is false without one.
func gitHead(root string) (string, time.Time, bool) {
	out, ok := gitOutput(root, "log", "-1", "--format=%H %ct", "HEAD")
	if !ok {
		return "", time.Time{}, false
	}
	sha, ts, found := strings.Cut(strings.TrimSpace(string(out)), " ")
	if !found {
		return "", time.Time{}, false
	}
	var sec int64
	for _, c := range ts {
		if c < '0' || c > '9' {
			return "", time.Time{}, false
		}
		sec = sec*10 + int64(c-'0')
	}
	return sha, time.Unix(sec, 0), true
}

// gitGrepWords finds, in one git grep, the tracked text files holding each
// word as a whole word: word → paths in git's order, at most limit each; a
// word nothing holds (or every word, when git fails) maps to nothing.
func gitGrepWords(root string, words []string, limit int) map[string][]string {
	out := map[string][]string{}
	if len(words) == 0 {
		return out
	}
	args := []string{"grep", "-n", "-w", "-F", "-I", "-z"}
	for _, w := range words {
		args = append(args, "-e", w)
	}
	b, ok := gitOutput(root, args...)
	if !ok {
		return out // exit 1 = no match
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		p, rest, found := strings.Cut(line, "\x00")
		if !found {
			continue
		}
		_, text, _ := strings.Cut(rest, "\x00")
		for _, w := range words {
			if len(out[w]) >= limit || seen[w+"\x00"+p] || !holdsWord(text, w) {
				continue
			}
			seen[w+"\x00"+p] = true
			out[w] = append(out[w], p)
		}
	}
	return out
}

// holdsWord reports whether text holds w with no word character on either
// side, the way git grep -w matches.
func holdsWord(text, w string) bool {
	isWord := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	for i := 0; ; {
		j := strings.Index(text[i:], w)
		if j < 0 {
			return false
		}
		j += i
		if (j == 0 || !isWord(text[j-1])) && (j+len(w) == len(text) || !isWord(text[j+len(w)])) {
			return true
		}
		i = j + 1
	}
}
