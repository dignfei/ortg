package base

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Index is the outline repository facade: the only data entry point for the
// top layer. It orchestrates tsv, fs, entry, doc and render.
type Index struct {
	root string
	t    *table
	disk map[string]fingerprint
}

// Fact is one file's reconciliation facts; no state is decided here.
type Fact struct {
	Path                 string
	Exists               bool
	IsNew                bool
	IsMissing            bool
	FingerprintChanged   bool
	OutlineEmpty         bool
	OutlineOlderThanFile bool
	HasTarget            bool
	TargetDelete         bool
	Ignored              bool
	// Observed marks the middle role: the file is fingerprinted and its drift
	// is reported, but it is never asked for an outline.
	Observed  bool
	Deleted   bool
	NeverSeen bool // row has no fingerprint yet (planned file)
}

// Row is the read view of one table row.
type Row struct {
	Path, Module, Outline, Target string
	TargetDelete                  bool
	Deleted, Ignored              bool
	Exists                        bool
}

// Config is the typed view of the JSON section.
type Config struct {
	IgnoreDirs  []string
	IgnoreFiles []string
	// KeepFiles re-includes paths the ignore rules would drop: ignoring a
	// directory and keeping the handful of files inside it that matter. It
	// overrides IgnoreDirs and IgnoreFiles, never the caller's exclusions —
	// ortg.tsv and overview.txt stay out of the table whatever it says.
	KeepFiles []string
	// ObserveDirs/ObserveFiles carry the middle role between "write an outline
	// for it" and "never look at it": the file is fingerprinted so a change
	// still surfaces, but it is never counted as missing an outline and never
	// rendered into the document. Tests, fixtures and generated config live
	// here. keep_files outranks it; it outranks the ignore rules.
	ObserveDirs  []string
	ObserveFiles []string
	Dirs         map[string]string
}

// ReconcileOptions carries the caller's built-in exclusions and size limit.
type ReconcileOptions struct {
	ExcludeDirs  []string
	ExcludeFiles []string
	MaxSize      int64
}

// Check is the result of validating a submitted entry line.
type Check struct {
	Errors    []string
	TagIssues []string
	ConsLen   int
	// ContractLen is the part of ConsLen in test contract clauses.
	ContractLen int
	C           int
}

// ImportResult counts what an outline document import touched.
type ImportResult struct {
	Sections, Entries, Created int
	Missing                    []string // paths written to Target because their file is absent from disk
}

// ImportOptions controls how ImportOutline decides where to write entries.
// When Excluded is non-nil, it filters files for the "project empty" check.
type ImportOptions struct {
	Excluded func(rel string) bool
	// AllTarget forces every entry into the Target column whether or not its
	// file exists. Bootstrapping wants the split (an existing file's line is
	// current knowledge); submitting a plan does not — it must never overwrite
	// the outline of a file that already exists.
	AllTarget bool
}

var zeroTime time.Time

func tablePath(root string) string { return filepath.Join(root, TableName) }

// Open loads the table at root; ErrNoTable when absent.
func Open(root string) (*Index, error) {
	t, err := openTable(tablePath(root))
	if err != nil {
		return nil, err
	}
	return &Index{root: root, t: t}, nil
}

// Create writes a new empty table with the given header.
func Create(root, header string) (*Index, error) {
	p := tablePath(root)
	if _, err := os.Stat(p); err == nil {
		return nil, fmt.Errorf("%s already exists", p)
	}
	t := newTable(p, header)
	if err := t.save(); err != nil {
		return nil, err
	}
	return &Index{root: root, t: t}, nil
}

// NewMemIndex is an in-memory index for tests; root is used for rendering.
func NewMemIndex(root, header string) *Index {
	return &Index{root: root, t: newTable("", header)}
}

func (x *Index) Root() string { return x.root }

// Discard removes the backing file (bootstrap rollback); memory tables are untouched.
func (x *Index) Discard() error {
	if x.t.path == "" {
		return nil
	}
	return os.Remove(x.t.path)
}

// fileRole is the three-way role of a path: the caller's structural exclusions
// are absolute, then keep_files pulls a path back to index, then observe_* puts
// it under watch, then the ignore rules drop it.
type fileRole int

const (
	roleIndex fileRole = iota
	roleObserve
	roleExcluded
)

func (x *Index) roleOf(rel string, opts ReconcileOptions, cfg Config) fileRole {
	switch {
	case ignored(rel, opts.ExcludeDirs, opts.ExcludeFiles):
		return roleExcluded
	case ignored(rel, nil, cfg.KeepFiles):
		return roleIndex
	case ignored(rel, cfg.ObserveDirs, cfg.ObserveFiles):
		return roleObserve
	case ignored(rel, cfg.IgnoreDirs, cfg.IgnoreFiles):
		return roleExcluded
	}
	return roleIndex
}

// Observed reports whether rel carries the observe role, so callers can keep it
// out of the rendered document without re-deriving the rules.
func (x *Index) Observed(rel string, opts ReconcileOptions) bool {
	return x.roleOf(rel, opts, x.Config()) == roleObserve
}

// Excluded reports whether rel is hit by the caller's exclusions or the JSON ignore rules.
func (x *Index) Excluded(rel string, opts ReconcileOptions) bool {
	return x.roleOf(rel, opts, x.Config()) == roleExcluded
}

// Batch takes the lock, runs fn and saves once.
func (x *Index) Batch(fn func() error) error { return x.t.batch(fn) }

// Reconcile enumerates the repository, fingerprints files and returns facts.
func (x *Index) Reconcile(opts ReconcileOptions) ([]Fact, error) {
	cfg := x.Config()
	dirs := append(append([]string{}, opts.ExcludeDirs...), cfg.IgnoreDirs...)
	// Only directory rules prune the nested-repository discovery walk; a file
	// pattern must never take a whole directory out of the search. A directory
	// that holds a kept file is walked anyway — pruning it here would put the
	// file out of reach of the keep rule for good.
	files, err := listFiles(x.root, func(rel string) bool {
		return ignored(rel, dirs, nil) && !keepsUnder(rel, cfg.KeepFiles)
	})
	if err != nil {
		return nil, err
	}
	x.disk = map[string]fingerprint{}
	ruleIgnored := map[string]bool{}
	observed := map[string]bool{}
	for _, rel := range files {
		switch x.roleOf(rel, opts, cfg) {
		case roleExcluded:
			ruleIgnored[rel] = true
			continue
		case roleObserve:
			observed[rel] = true
		}
		full := filepath.Join(x.root, filepath.FromSlash(rel))
		if opts.MaxSize > 0 {
			if st, err := os.Stat(full); err == nil && st.Size() > opts.MaxSize {
				ruleIgnored[rel] = true
				continue
			}
		}
		fp, err := fingerprintFile(full)
		if err != nil {
			continue
		}
		x.disk[rel] = fp
	}
	var facts []Fact
	for rel, fp := range x.disk {
		r := x.t.get(rel)
		f := Fact{Path: rel, Exists: true, IsNew: r == nil, OutlineEmpty: true}
		if r != nil {
			f.FingerprintChanged = r.CRC != fp.CRC
			f.OutlineEmpty = r.Outline == ""
			f.OutlineOlderThanFile = !r.OutlineTime.After(fp.Mtime)
			f.HasTarget, f.TargetDelete, f.Deleted = r.Target != "", r.TargetDelete, r.Deleted
		}
		facts = append(facts, f)
	}
	for _, r := range x.t.list() {
		if _, ok := x.disk[r.Path]; ok {
			continue
		}
		// A row the scan no longer enumerates but that still sits on disk
		// carries knowledge worth keeping only when it has an outline or a
		// plan; a bare skeleton row is leftover noise (the file moved out of
		// scope, e.g. into a directory its own repository ignores), so report
		// it as ignored and let Purge drop it.
		if r.Outline == "" && r.Target == "" && !r.TargetDelete {
			facts = append(facts, Fact{Path: r.Path, Exists: true, OutlineEmpty: true, Ignored: true, Deleted: r.Deleted})
			continue
		}
		// Fallback: file may be gitignored but still on disk (e.g. .gitignore
		// explicitly lists it, so git ls-files omits it).
		full := filepath.Join(x.root, filepath.FromSlash(r.Path))
		if fp, err := fingerprintFile(full); err == nil {
			x.disk[r.Path] = fp
			f := Fact{Path: r.Path, Exists: true, FingerprintChanged: r.CRC != fp.CRC,
				OutlineEmpty: r.Outline == "", OutlineOlderThanFile: !r.OutlineTime.After(fp.Mtime),
				HasTarget: r.Target != "", TargetDelete: r.TargetDelete, Deleted: r.Deleted}
			facts = append(facts, f)
			continue
		}
		facts = append(facts, Fact{Path: r.Path, IsMissing: true, OutlineEmpty: r.Outline == "", HasTarget: r.Target != "",
			TargetDelete: r.TargetDelete, Deleted: r.Deleted, Ignored: ruleIgnored[r.Path], NeverSeen: r.CRC == 0 && r.Mtime.IsZero()})
	}
	for i := range facts {
		facts[i].Observed = observed[facts[i].Path]
		if ruleIgnored[facts[i].Path] {
			facts[i].Ignored = true
		}
	}
	for rel := range ruleIgnored {
		if x.t.get(rel) == nil {
			facts = append(facts, Fact{Path: rel, Exists: true, Ignored: true, OutlineEmpty: true})
		}
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].Path < facts[j].Path })
	return facts, nil
}

// Inspect returns the facts for one path without a full scan (hook use).
func (x *Index) Inspect(rel string) (Fact, bool) {
	r := x.t.get(rel)
	fp, err := fingerprintFile(filepath.Join(x.root, filepath.FromSlash(rel)))
	f := Fact{Path: rel, Exists: err == nil, IsNew: r == nil && err == nil, IsMissing: r != nil && err != nil, OutlineEmpty: true}
	if r == nil {
		return f, false
	}
	f.OutlineEmpty, f.HasTarget, f.TargetDelete, f.Deleted, f.Ignored = r.Outline == "", r.Target != "", r.TargetDelete, r.Deleted, r.Ignored
	f.NeverSeen = r.CRC == 0 && r.Mtime.IsZero()
	if err == nil {
		f.FingerprintChanged = r.CRC != fp.CRC
		f.OutlineOlderThanFile = !r.OutlineTime.After(fp.Mtime)
	}
	return f, true
}

// TouchedPaths returns the paths of non-ignored, non-tombstone rows that carry
// an outline and whose file mtime is not older than the outline time. It stats
// each row and reads no content, so a caller that must not fingerprint the
// whole repository (a hook fired on every shell command) can narrow the set
// first and confirm the survivors with Inspect.
func (x *Index) TouchedPaths() []string {
	var out []string
	for _, r := range x.t.list() {
		if r.Ignored || r.Deleted || r.Outline == "" {
			continue
		}
		st, err := os.Stat(filepath.Join(x.root, filepath.FromSlash(r.Path)))
		if err != nil || r.OutlineTime.After(st.ModTime().UTC().Truncate(time.Second)) {
			continue
		}
		out = append(out, r.Path)
	}
	sort.Strings(out)
	return out
}

// MentionedPaths returns the paths of live rows that the given shell command
// names, either by repository-relative path or by a base name that belongs to
// exactly one row. It stats nothing and reads nothing: a hook that fires on
// every shell command uses it to narrow the table to the files the command
// could have read or written, then confirms the survivors with Inspect.
// TouchedPaths cannot serve here because it skips rows without an outline,
// which is precisely the set a shell read has to establish cognition for.
func (x *Index) MentionedPaths(cmd string) []string {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	rows := x.t.list()
	seen := map[string]int{}
	for _, r := range rows {
		if r.Ignored || r.Deleted {
			continue
		}
		seen[path.Base(r.Path)]++
	}
	var out []string
	for _, r := range rows {
		if r.Ignored || r.Deleted {
			continue
		}
		b := path.Base(r.Path)
		if mentions(cmd, r.Path, true) || (b != r.Path && seen[b] == 1 && mentions(cmd, b, false)) {
			out = append(out, r.Path)
		}
	}
	sort.Strings(out)
	return out
}

// UsesItems splits an outline line's Uses field into its items: separated
// by commas or enumeration commas outside parentheses, each item's
// parenthetical note — "util.rs(Sink, Sunk)" — cut off; "-" and an
// unparsable line give none.
func UsesItems(line string) []string {
	e, err := parseEntry(line)
	if err != nil {
		return nil
	}
	return splitItems(e.Uses)
}

// KeysOf returns the contract keys an outline line declares in its optional
// Keys field, roles in parentheses cut off.
func KeysOf(line string) []string {
	e, err := parseEntry(line)
	if err != nil {
		return nil
	}
	return splitItems(e.Keys)
}

// GuardTests returns the tests the "必须保持" clauses of text cite — an
// outline line or a contract's text — in order, once each.
func GuardTests(text string) []string { return testNames(text) }

// HeadContent returns rel's content at git HEAD; false for a file HEAD does
// not have or a root in no repository.
func (x *Index) HeadContent(rel string) (string, bool) { return headContent(x.root, rel) }

// ChangedFiles maps the files that differ from HEAD and are still on disk
// (untracked ones included) to their modification times; ok is false outside git.
func (x *Index) ChangedFiles() (map[string]time.Time, bool) { return gitChanged(x.root) }

// HeadCommit returns HEAD's commit id and commit time.
func (x *Index) HeadCommit() (string, time.Time, bool) { return gitHead(x.root) }

// GrepWords finds, per word, up to limit tracked files holding it as a whole word.
func (x *Index) GrepWords(words []string, limit int) map[string][]string {
	return gitGrepWords(x.root, words, limit)
}

// Contracts returns the header's contract table, key → text.
func (x *Index) Contracts() map[string]string { return contracts(x.t.header) }

// KeyHolders lists the live rows whose outline or target outline declares
// key, sorted.
func (x *Index) KeyHolders(key string) []string {
	var out []string
	for _, r := range x.t.list() {
		if r.Ignored || r.Deleted {
			continue
		}
		if slices.Contains(KeysOf(r.Outline), key) || slices.Contains(KeysOf(r.Target), key) {
			out = append(out, r.Path)
		}
	}
	sort.Strings(out)
	return out
}

// literalScanMax skips files too large to be source in FilesContaining.
const literalScanMax = 2 << 20

// FilesContaining lists the live rows whose file on disk holds lit, sorted;
// files over 2 MiB are skipped. It finds where a literal contract key — a
// route, a message type, a config key — is used without being declared.
func (x *Index) FilesContaining(lit string) []string {
	if lit == "" {
		return nil
	}
	var out []string
	for _, r := range x.t.list() {
		if r.Ignored || r.Deleted || strings.HasSuffix(r.Path, "/") {
			continue
		}
		p := filepath.Join(x.root, filepath.FromSlash(r.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() || st.Size() > literalScanMax {
			continue
		}
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), lit) {
			out = append(out, r.Path)
		}
	}
	sort.Strings(out)
	return out
}

// ResolvesUses reports whether a Uses item names something in the repository
// by its repo-relative path: a live row, or — written with a trailing slash —
// a directory on disk.
func (x *Index) ResolvesUses(item string) bool {
	if strings.HasSuffix(item, "/") {
		st, err := os.Stat(filepath.Join(x.root, filepath.FromSlash(strings.TrimSuffix(item, "/"))))
		return err == nil && st.IsDir()
	}
	r := x.t.get(item)
	return r != nil && !r.Deleted && !r.Ignored
}

// Dependents lists the live rows whose outline names rel in its Uses field —
// the files the global outline says depend on rel. An item matches by path,
// as a directory ("lib/storage/") holding rel, or — for outlines written
// before Uses had to be paths — by bare file name when that name is unique in
// the table; package and module names match nothing. The result is sorted.
func (x *Index) Dependents(rel string) []string {
	rows := x.t.list()
	seen := map[string]int{}
	for _, r := range rows {
		if !r.Ignored && !r.Deleted {
			seen[path.Base(r.Path)]++
		}
	}
	base := path.Base(rel)
	var out []string
	for _, r := range rows {
		if r.Ignored || r.Deleted || r.Path == rel || r.Outline == "" {
			continue
		}
		for _, it := range UsesItems(r.Outline) {
			dir := strings.HasSuffix(it, "/")
			it = strings.TrimSuffix(it, "/")
			if it == rel || (dir && strings.HasPrefix(rel, it+"/")) || (seen[base] == 1 && it == base) {
				out = append(out, r.Path)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// mentions reports whether name occurs in cmd as a whole path token, so that
// index.go matches neither myindex.go nor index.golden. A relative path may be
// preceded by a slash (the command may spell it absolutely); a bare base name
// may not, or vendor/index.go would be read as this repository's index.go.
func mentions(cmd, name string, slashBefore bool) bool {
	for i := 0; i+len(name) <= len(cmd); {
		j := strings.Index(cmd[i:], name)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !isPathByte(cmd[j-1]) || (slashBefore && cmd[j-1] == '/')
		after := j+len(name) >= len(cmd) || !isPathByte(cmd[j+len(name)])
		if before && after {
			return true
		}
		i = j + 1
	}
	return false
}

func isPathByte(c byte) bool {
	switch {
	case c == '.' || c == '-' || c == '_' || c == '/':
		return true
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return c >= 0x80 // a UTF-8 continuation or lead byte: still inside a name
}

// Row returns the read view of one row.
func (x *Index) Row(rel string) (Row, bool) {
	r := x.t.get(rel)
	if r == nil {
		return Row{}, false
	}
	return x.rowView(r), true
}

func (x *Index) rowView(r *record) Row {
	exists := true
	if x.disk != nil {
		_, exists = x.disk[r.Path]
	} else if _, err := os.Stat(filepath.Join(x.root, filepath.FromSlash(r.Path))); err != nil {
		exists = false
	}
	return Row{Path: r.Path, Module: r.moduleOrDerived(), Outline: r.Outline, Target: r.Target, TargetDelete: r.TargetDelete, Deleted: r.Deleted, Ignored: r.Ignored, Exists: exists}
}

func (x *Index) fp(rel string) (fingerprint, bool) {
	if fp, ok := x.disk[rel]; ok {
		return fp, true
	}
	fp, err := fingerprintFile(filepath.Join(x.root, filepath.FromSlash(rel)))
	return fp, err == nil
}

func (x *Index) row(rel string) *record {
	if r := x.t.get(rel); r != nil {
		return r
	}
	return &record{Path: rel}
}

// AddSkeleton adds a row with the current fingerprint and no outline.
func (x *Index) AddSkeleton(rel string) error {
	r := x.row(rel)
	if fp, ok := x.fp(rel); ok {
		r.CRC, r.Mtime = fp.CRC, fp.Mtime
	}
	r.Deleted = false
	return x.t.upsert(r)
}

// SetDeleted records whether the file is gone from disk (tombstone).
func (x *Index) SetDeleted(rel string, v bool) error {
	r := x.t.get(rel)
	if r == nil || r.Deleted == v {
		return nil
	}
	r.Deleted = v
	return x.t.upsert(r)
}

func (x *Index) SetIgnored(rel string, v bool) error {
	r := x.t.get(rel)
	if r == nil || r.Ignored == v {
		return nil
	}
	r.Ignored = v
	return x.t.upsert(r)
}

// Align advances fingerprint and outline time to now for the given paths.
func (x *Index) Align(rels []string) error {
	for _, rel := range rels {
		r := x.t.get(rel)
		if r == nil {
			continue
		}
		if fp, ok := x.fp(rel); ok {
			r.CRC, r.Mtime, r.Deleted = fp.CRC, fp.Mtime, false
		}
		r.OutlineTime = Now()
		if err := x.t.upsert(r); err != nil {
			return err
		}
	}
	return nil
}

// SetOutline stores the entry line as the current outline and aligns it.
func (x *Index) SetOutline(rel, line string) error {
	r := x.row(rel)
	r.Outline, r.OutlineTime = strings.TrimSpace(line), Now()
	if fp, ok := x.fp(rel); ok {
		r.CRC, r.Mtime, r.Deleted = fp.CRC, fp.Mtime, false
	}
	return x.t.upsert(r)
}

// SetTarget stores a target outline; a missing path gets a zero-fingerprint row.
func (x *Index) SetTarget(rel, line string) error {
	r := x.row(rel)
	r.Target, r.TargetTime, r.TargetDelete = strings.TrimSpace(line), Now(), false
	return x.t.upsert(r)
}

func (x *Index) SetTargetDelete(rel string) error {
	r := x.row(rel)
	r.Target, r.TargetTime, r.TargetDelete = "", Now(), true
	return x.t.upsert(r)
}

func (x *Index) ClearTarget(rel string) error {
	r := x.t.get(rel)
	if r == nil {
		return nil
	}
	r.Target, r.TargetTime, r.TargetDelete = "", zeroTime, false
	return x.t.upsert(r)
}

// Purge removes rows that are both deleted and marked for target deletion,
// plus rows hit by the ignore rules, and returns their paths.
func (x *Index) Purge() ([]string, error) {
	var gone []string
	for _, r := range x.t.list() {
		if r.Ignored || (r.Deleted && r.TargetDelete) {
			if err := x.t.del(r.Path); err != nil {
				return gone, err
			}
			gone = append(gone, r.Path)
		}
	}
	return gone, nil
}

// CheckEntry validates a submitted line against format and dictionary.
func (x *Index) CheckEntry(rel, line string) Check {
	var c Check
	if strings.ContainsAny(line, "\t\n\r") {
		c.Errors = append(c.Errors, "纲要行不能包含制表符或换行")
		return c
	}
	e, err := parseEntry(line)
	if err != nil {
		c.Errors = append(c.Errors, err.Error())
		return c
	}
	if strings.TrimSuffix(e.Name, "/") != path.Base(rel) { // "vendored/" names a directory
		c.Errors = append(c.Errors, fmt.Sprintf("文件名 %s 与路径 %s 不一致", e.Name, rel))
	}
	c.TagIssues = checkTag(e, dicts(x.t.header))
	c.ConsLen, c.ContractLen, c.C = consLen(e), contractLen(e), e.TC
	return c
}

func (x *Index) Header() string { return x.t.header }

// SetHeader replaces the header; it must declare the A, B and E dictionaries.
// Blank lines are dropped: the table format ends the header at the first one.
func (x *Index) SetHeader(h string) error {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(h, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, "#") {
			return fmt.Errorf("纲要头每行必须以#开头: %.40s", l)
		}
		lines = append(lines, l)
	}
	h = strings.Join(lines, "\n")
	d := dicts(h)
	for _, axis := range []string{"A", "B", "E"} {
		if len(d[axis]) == 0 {
			return fmt.Errorf("纲要头缺少 #%s 字典行", axis)
		}
	}
	x.t.header = h
	if x.t.inBatch {
		return nil
	}
	return x.t.save()
}

func (x *Index) Config() Config {
	return Config{IgnoreDirs: x.t.cfg.strings("ignore_dirs"), IgnoreFiles: x.t.cfg.strings("ignore_files"),
		KeepFiles: x.t.cfg.strings("keep_files"), ObserveDirs: x.t.cfg.strings("observe_dirs"),
		ObserveFiles: x.t.cfg.strings("observe_files"), Dirs: x.t.cfg.stringMap("dirs")}
}

func (x *Index) SetConfig(c Config) error {
	x.t.cfg.set("ignore_dirs", nonNil(c.IgnoreDirs))
	x.t.cfg.set("ignore_files", nonNil(c.IgnoreFiles))
	x.t.cfg.set("keep_files", nonNil(c.KeepFiles))
	x.t.cfg.set("observe_dirs", nonNil(c.ObserveDirs))
	x.t.cfg.set("observe_files", nonNil(c.ObserveFiles))
	if c.Dirs == nil {
		c.Dirs = map[string]string{}
	}
	x.t.cfg.set("dirs", c.Dirs)
	if x.t.inBatch {
		return nil
	}
	return x.t.save()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Rows returns all rows sorted by path.
func (x *Index) Rows() []Row {
	var out []Row
	for _, r := range x.t.list() {
		out = append(out, x.rowView(r))
	}
	return out
}

// Render produces the outline document text for the model.
func (x *Index) Render(opts RenderOptions) string {
	if opts.Root == "" {
		opts.Root = x.root
	}
	return render(x.t.header, x.Rows(), opts)
}

// ImportOutline parses an outline document and writes every entry into the
// target columns; the document header becomes the table header.
// It is a shorthand for ImportOutlineWith(text, ImportOptions{}).
func (x *Index) ImportOutline(text string) (ImportResult, error) {
	return x.ImportOutlineWith(text, ImportOptions{})
}

// ImportOutlineWith parses an outline document and splits entries by file
// existence on disk:
//   - Empty project (no real code files) → all entries go to Target (backward compat).
//   - File exists → entry goes to Outline with current fingerprint.
//   - File missing → entry goes to Target and the path is appended to Missing.
//
// opts.AllTarget turns the split off: every entry lands in Target and Missing
// stays empty, which is what submitting a plan means.
//
// The document header becomes the table header. Path remapping (inferOldRoot /
// remapRoot) is preserved for documents authored in a different repo location.
func (x *Index) ImportOutlineWith(text string, opts ImportOptions) (ImportResult, error) {
	doc, err := parseDocument(text)
	if err != nil {
		return ImportResult{}, err
	}
	var res ImportResult
	if doc.Header != "" {
		if err := x.SetHeader(doc.Header); err != nil {
			return res, err
		}
	}
	empty := x.projectEmpty(opts)
	// Pre-compute inferred old root for path remapping when the document was
	// authored in a different repo location (e.g. after copy/move).
	inferredOldRoot := inferOldRoot(doc.Sections)
	for _, sec := range doc.Sections {
		relDir, ok := Rel(x.root, filepath.FromSlash(sec.Dir))
		if !ok && inferredOldRoot != "" {
			relDir, ok = remapRoot(x.root, inferredOldRoot, sec.Dir)
		}
		if !ok {
			return res, fmt.Errorf("目录 %s 不在仓库 %s 内", sec.Dir, x.root)
		}
		res.Sections++
		for _, de := range sec.Entries {
			if de.Line == "" && de.Target == "" && !de.TargetDelete {
				continue // bare skeleton name carries nothing to import
			}
			rel := de.Name
			if relDir != "." {
				rel = path.Join(relDir, de.Name)
			}
			if x.t.get(rel) == nil {
				res.Created++
			}
			r := x.row(rel)
			// Decide: Outline (file exists, non-empty project) or Target.
			writeOutline := !opts.AllTarget && !empty && de.Line != "" && fileExists(filepath.Join(x.root, filepath.FromSlash(rel)))
			if writeOutline {
				r.Outline, r.OutlineTime = de.Line, Now()
				if fp, ok := x.fp(rel); ok {
					r.CRC, r.Mtime, r.Deleted = fp.CRC, fp.Mtime, false
				}
			}
			// Target column: explicit #目标: annotation, or the whole entry when
			// the file is absent (or the project is empty / bare skeleton).
			if de.TargetDelete {
				r.Target, r.TargetTime, r.TargetDelete = "", Now(), true
			} else if de.Target != "" {
				r.Target, r.TargetTime, r.TargetDelete = de.Target, Now(), false
			} else if !writeOutline {
				r.Target, r.TargetTime, r.TargetDelete = de.Line, Now(), false
			} else {
				// File exists, no explicit #目标: → clear any stale target.
				r.Target, r.TargetTime, r.TargetDelete = "", zeroTime, false
			}
			if !empty && !writeOutline && !opts.AllTarget {
				res.Missing = append(res.Missing, rel)
			}
			if err := x.t.upsert(r); err != nil {
				return res, err
			}
			res.Entries++
		}
	}
	return res, nil
}

// projectEmpty reports whether the repository has zero real code files after
// applying the caller's exclusion rules.
func (x *Index) projectEmpty(opts ImportOptions) bool {
	files, err := listFiles(x.root, func(rel string) bool {
		return opts.Excluded != nil && opts.Excluded(rel)
	})
	if err != nil {
		return true
	}
	for _, rel := range files {
		if opts.Excluded == nil || !opts.Excluded(rel) {
			return false
		}
	}
	return true
}

// fileExists is a tiny helper: true when path is a regular file or directory.
func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// inferOldRoot guesses the original repo root from section directory paths.
// The shortest absolute path is typically the repo root itself (e.g.
// /home/user/repo/) while longer paths are subdirectories.
func inferOldRoot(sections []docSection) string {
	shortest := ""
	for _, s := range sections {
		if s.Dir == "" {
			continue
		}
		if shortest == "" || len(s.Dir) < len(shortest) {
			shortest = s.Dir
		}
	}
	if shortest == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.ToSlash(filepath.Clean(shortest)), "/")
}

// remapRoot converts a section directory path from an old repo root to a
// relative path under the new root. It strips the inferred old root prefix
// and checks that the resulting relative path exists in the new root.
func remapRoot(newRoot, oldRoot, secDir string) (string, bool) {
	sd := filepath.ToSlash(filepath.Clean(secDir))
	if sd == oldRoot {
		return ".", true
	}
	prefix := oldRoot + "/"
	if !strings.HasPrefix(sd, prefix) {
		return "", false
	}
	suffix := sd[len(prefix):]
	target := filepath.Join(newRoot, filepath.FromSlash(suffix))
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		return "", false
	}
	return suffix, true
}

// Related returns up to limit cross-file call relations of rel from the local
// CBM call graph; nil when CBM is absent, unindexed, slow or failing.
func (x *Index) Related(rel string, limit int, timeout time.Duration) []Relation {
	return cbmRelated(x.root, rel, limit, timeout)
}

// RefreshGraph re-indexes the CBM call graph in the background of this
// long-lived process. A no-op when CBM is absent or disabled.
func (x *Index) RefreshGraph() { cbmRefresh(x.root) }

// IndexGraph re-indexes the CBM call graph now, waiting at most timeout: a
// hook that must answer from the graph as it stands after an edit. A no-op
// when CBM is absent or disabled; a failure only leaves the graph stale.
func (x *Index) IndexGraph(timeout time.Duration) {
	bin := cbmPath()
	if bin == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := cbmIndexCmd(ctx, bin, x.root)
	cmd.WaitDelay = 200 * time.Millisecond
	if err := cmd.Run(); err != nil {
		Debugf("cbm index %s: %v", x.root, err)
	}
}

// GraphEnabled reports whether the CBM call graph is in use: the binary is on
// PATH and ORTG_CBM is not "off". Guard Spawn("graph") with it — under go test
// the executable is the test binary, which would rerun the suite.
func (x *Index) GraphEnabled() bool { return cbmPath() != "" }

// WarmGraph starts CBM's permanent daemon and then re-indexes this root,
// synchronously. A no-op when CBM is absent or disabled.
func (x *Index) WarmGraph() error { return cbmWarm(x.root) }

// Spawn runs this same executable with args in the repository root as a
// detached background process: stdio is /dev/null and nothing waits on it,
// so a hook can hand off work that outlasts the host's hook timeout.
func (x *Index) Spawn(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = x.root
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
