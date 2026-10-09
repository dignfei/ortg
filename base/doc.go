package base

import (
	"fmt"
	"regexp"
	"strings"
)

// docEntry is one outline line; Line is empty for a skeleton (bare name) or
// a planned/deleted anchor, in which case Name carries the file name.
type docEntry struct {
	Name         string
	Line         string
	Target       string
	TargetDelete bool
}

type docSection struct {
	Dir     string
	Entries []docEntry
}

type document struct {
	Header   string
	Sections []docSection
}

var sectionRe = regexp.MustCompile(`^===(.*?)((?:[A-Za-z]:)?/[^=\s]*)===\s*$`)

const (
	// A target outline follows its entry as "#目标: <line>" or "#目标:删除";
	// older markers are the migrate package's business.
	targetPrefix  = "#目标:"
	plannedPrefix = "#计划新建 "
	deletedPrefix = "#已删除 "
)

var bareNameRe = regexp.MustCompile(`^[^\s\[\]|]+$`)

// parseDocument parses an outline document: leading # lines are the header,
// ===描述 /abs/dir/=== opens a section, entry lines are validated, and a
// #目标: line right after an entry belongs to it. Paths are not converted.
func parseDocument(text string) (document, error) {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\uFEFF")
	var doc document
	var hdr []string
	var cur *docSection
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			doc.Sections = append(doc.Sections, docSection{Dir: m[2]})
			cur = &doc.Sections[len(doc.Sections)-1]
			continue
		}
		if strings.HasPrefix(line, "#") {
			switch {
			case cur == nil:
				if strings.TrimSpace(line) != tsvVersionLine {
					hdr = append(hdr, line)
				}
			case strings.HasPrefix(line, plannedPrefix):
				cur.Entries = append(cur.Entries, docEntry{Name: strings.TrimSpace(strings.TrimPrefix(line, plannedPrefix))})
			case strings.HasPrefix(line, deletedPrefix):
				rest := strings.TrimSpace(strings.TrimPrefix(line, deletedPrefix))
				if e, err := parseEntry(rest); err == nil {
					cur.Entries = append(cur.Entries, docEntry{Name: e.Name, Line: rest})
				} else {
					cur.Entries = append(cur.Entries, docEntry{Name: rest})
				}
			case strings.HasPrefix(line, targetPrefix) && len(cur.Entries) > 0:
				last := &cur.Entries[len(cur.Entries)-1]
				body := strings.TrimSpace(strings.TrimPrefix(line, targetPrefix))
				if body == "删除" {
					last.TargetDelete = true
				} else {
					last.Target = body
				}
			}
			continue
		}
		if cur == nil {
			return doc, fmt.Errorf("第 %d 行: 目录段之前出现条目", n+1)
		}
		if bareNameRe.MatchString(line) {
			cur.Entries = append(cur.Entries, docEntry{Name: line})
			continue
		}
		e, err := parseEntry(line)
		if err != nil {
			return doc, fmt.Errorf("第 %d 行: %v", n+1, err)
		}
		cur.Entries = append(cur.Entries, docEntry{Name: e.Name, Line: line})
	}
	doc.Header = strings.Join(hdr, "\n")
	return doc, nil
}
