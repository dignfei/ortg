package base

import (
	"path"
	"sort"
	"strings"
)

// Prefixes are the texts the top layer supplies for special row forms.
type Prefixes struct {
	Deleted      string
	Planned      string
	Target       string
	TargetDelete string
	Body         string
	// TargetSame replaces a target outline identical to the current one: a
	// plan that changes the code but not the outline need not repeat it.
	TargetSame string
}

// RenderOptions selects rows, titles directories and supplies prefixes.
type RenderOptions struct {
	Root     string
	Include  func(Row) bool
	DirTitle map[string]string
	Prefix   Prefixes
	// OmitHeader leaves the header out: an incremental send whose header is
	// already in the reader's context.
	OmitHeader bool
}

// render writes the header, a body marker, then one directory section per
// directory with its rows; a row without outline renders as its bare name.
func render(header string, rows []Row, opts RenderOptions) string {
	var b strings.Builder
	if header != "" && !opts.OmitHeader {
		b.WriteString(header + "\n\n")
	}
	if opts.Prefix.Body != "" {
		b.WriteString(opts.Prefix.Body + "\n")
	}
	byDir := map[string][]Row{}
	kept := make([]Row, 0, len(rows))
	for _, r := range rows {
		if opts.Include != nil && !opts.Include(r) {
			continue
		}
		kept = append(kept, r)
		d := path.Dir(r.Path)
		byDir[d] = append(byDir[d], r)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	root := strings.TrimSuffix(strings.ReplaceAll(opts.Root, "\\", "/"), "/")
	for _, d := range dirs {
		abs := root + "/"
		if d != "." {
			abs = root + "/" + d + "/"
		}
		title := opts.DirTitle[d]
		if title != "" {
			title += " "
		}
		b.WriteString("\n===" + title + abs + "===\n")
		list := byDir[d]
		sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
		for _, r := range list {
			name := path.Base(r.Path)
			switch {
			case r.Outline != "" && r.Deleted:
				b.WriteString(opts.Prefix.Deleted + r.Outline + "\n")
			case r.Outline != "":
				b.WriteString(r.Outline + "\n")
			case !r.Exists && !r.Deleted && (r.Target != "" || r.TargetDelete):
				b.WriteString(opts.Prefix.Planned + name + "\n")
			case r.Deleted:
				b.WriteString(opts.Prefix.Deleted + name + "\n")
			default:
				b.WriteString(name + "\n")
			}
			switch {
			case r.TargetDelete:
				b.WriteString(opts.Prefix.TargetDelete + "\n")
			case r.Target != "" && r.Target == r.Outline && opts.Prefix.TargetSame != "":
				b.WriteString(opts.Prefix.TargetSame + "\n")
			case r.Target != "":
				b.WriteString(opts.Prefix.Target + r.Target + "\n")
			}
		}
	}
	return b.String()
}
