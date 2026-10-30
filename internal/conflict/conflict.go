// Package conflict reads a conflicted file as git's three-way merge leaves
// it: text that merged cleanly, and conflicts whose yours, base and theirs
// versions the merge tool chooses between.
package conflict

import (
	"strings"

	"github.com/Zenyet/banyan/internal/git"
)

// Segment is lines that merged cleanly (Conflict nil), or a conflict.
type Segment struct {
	// Clean holds the lines with their line endings.
	Clean    []string
	Conflict *Conflict
}

type Conflict struct {
	Ours, Base, Theirs []string
}

type ResolutionKind int

const (
	Ours ResolutionKind = iota
	Theirs
	OursThenTheirs
	TheirsThenOurs
	// Custom is text typed in the merge tool.
	Custom
)

type Resolution struct {
	Kind ResolutionKind
	Text string
}

// Resolved returns the lines that replace the conflict.
func (c *Conflict) Resolved(r Resolution) []string {
	switch r.Kind {
	case Ours:
		return append([]string(nil), c.Ours...)
	case Theirs:
		return append([]string(nil), c.Theirs...)
	case OursThenTheirs:
		return concat(c.Ours, c.Theirs)
	case TheirsThenOurs:
		return concat(c.Theirs, c.Ours)
	}
	lines := SplitLines(r.Text)
	terminate(lines)
	return lines
}

// SplitLines splits text into lines with their endings; a last line without
// one is kept as it is.
func SplitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Display is a line's text without its ending.
func Display(line string) string {
	return strings.TrimRight(line, "\r\n")
}

func terminate(lines []string) {
	if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		lines[n-1] += "\n"
	}
}

func concat(first, second []string) []string {
	lines := append([]string(nil), first...)
	if len(second) > 0 {
		terminate(lines)
	}
	return append(lines, second...)
}

// Parse splits git merge-file --diff3 output (markers of git.MarkerSize)
// into segments.
func Parse(text string) []Segment {
	const (
		clean = iota
		ours
		base
		theirs
	)
	marker := func(c byte, line string) bool {
		line = Display(line)
		m := strings.Repeat(string(c), git.MarkerSize)
		rest, ok := strings.CutPrefix(line, m)
		return ok && (rest == "" || strings.HasPrefix(rest, " "))
	}
	var segments []Segment
	var cleanLines []string
	conflict := &Conflict{}
	part := clean
	for _, line := range SplitLines(text) {
		switch {
		case part == clean && marker('<', line):
			if len(cleanLines) > 0 {
				segments = append(segments, Segment{Clean: cleanLines})
				cleanLines = nil
			}
			part = ours
		case part == clean:
			cleanLines = append(cleanLines, line)
		case (part == ours || part == base) && marker('=', line):
			part = theirs
		case part == ours && marker('|', line):
			part = base
		case part == ours:
			conflict.Ours = append(conflict.Ours, line)
		case part == base:
			conflict.Base = append(conflict.Base, line)
		case part == theirs && marker('>', line):
			segments = append(segments, Segment{Conflict: conflict})
			conflict = &Conflict{}
			part = clean
		default:
			conflict.Theirs = append(conflict.Theirs, line)
		}
	}
	if len(cleanLines) > 0 {
		segments = append(segments, Segment{Clean: cleanLines})
	}
	return segments
}

// ResultText is the merged file with resolutions[i] in place of the i-th
// conflict, or false while a conflict is unresolved.
func ResultText(segments []Segment, resolutions []*Resolution) (string, bool) {
	var text strings.Builder
	conflicts := 0
	for _, s := range segments {
		lines := s.Clean
		if s.Conflict != nil {
			if conflicts >= len(resolutions) || resolutions[conflicts] == nil {
				return "", false
			}
			lines = s.Conflict.Resolved(*resolutions[conflicts])
			conflicts++
		}
		for _, l := range lines {
			text.WriteString(l)
		}
	}
	return text.String(), true
}
