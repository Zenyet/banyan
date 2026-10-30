// Package patch splits a single-file unified diff into hunks and puts a
// subset of its changed lines back together, for committing only some of a
// file's changes.
package patch

import (
	"strconv"
	"strings"
)

// ChangedLine is a changed line: its hunk's header and its index into the
// hunk's Lines.
type ChangedLine struct {
	Header string
	Index  int
}

type Hunk struct {
	// Header is the @@ … @@ line, which identifies the hunk across
	// refreshes.
	Header string
	Lines  []string
}

func (h *Hunk) Additions() int { return h.count('+') }
func (h *Hunk) Deletions() int { return h.count('-') }

func (h *Hunk) count(prefix byte) int {
	n := 0
	for _, l := range h.Lines {
		if len(l) > 0 && l[0] == prefix {
			n++
		}
	}
	return n
}

// Context is the text after the second @@, usually the enclosing function.
func (h *Hunk) Context() string {
	parts := strings.SplitN(h.Header, "@@", 3)
	if len(parts) < 3 {
		return ""
	}
	return strings.TrimSpace(parts[2])
}

// FirstChangedLine is the line of the first change in the new file,
// skipping leading context.
func (h *Hunk) FirstChangedLine() int {
	leading := 0
	for _, l := range h.Lines {
		if !strings.HasPrefix(l, " ") {
			break
		}
		leading++
	}
	return h.NewStart() + leading
}

// Changes returns the indices into Lines of the added and removed lines.
func (h *Hunk) Changes() []int {
	var out []int
	for i, l := range h.Lines {
		if isChange(l) {
			out = append(out, i)
		}
	}
	return out
}

// NewLineAt is the line in the new file at Lines[ix]; for a removed line,
// the line that follows it.
func (h *Hunk) NewLineAt(ix int) int {
	before := 0
	for _, l := range h.Lines[:ix] {
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "+") {
			before++
		}
	}
	return h.NewStart() + before
}

// NewStart is the first line of the hunk in the new file.
func (h *Hunk) NewStart() int {
	for _, part := range strings.Fields(h.Header) {
		if rng, ok := strings.CutPrefix(part, "+"); ok {
			start, _, _ := strings.Cut(rng, ",")
			if n, err := strconv.Atoi(start); err == nil {
				return n
			}
		}
	}
	return 1
}

type FilePatch struct {
	// Header holds the diff --git, index, --- and +++ lines.
	Header []string
	Hunks  []Hunk
}

func Parse(patch string) FilePatch {
	var file FilePatch
	for _, line := range lines(patch) {
		switch {
		case strings.HasPrefix(line, "@@"):
			file.Hunks = append(file.Hunks, Hunk{Header: line})
		case len(file.Hunks) > 0:
			h := &file.Hunks[len(file.Hunks)-1]
			h.Lines = append(h.Lines, line)
		default:
			file.Header = append(file.Header, line)
		}
	}
	return file
}

// lines splits text into lines as Rust's str::lines does: no empty last
// line, and \r\n endings taken off.
func lines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	out := strings.Split(text, "\n")
	for i, l := range out {
		out[i] = strings.TrimSuffix(l, "\r")
	}
	return out
}

func isChange(line string) bool {
	return strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")
}

// Select returns a patch with only the changed lines keep accepts, given a
// hunk and an index into its Lines, or false if none remain. A left-out
// removal stays as context; a left-out addition is dropped.
//
// Line counts in hunk headers are not adjusted; apply the result with
// git apply --recount, which recomputes them.
func Select(file FilePatch, keep func(h *Hunk, ix int) bool) (string, bool) {
	var body strings.Builder
	for hi := range file.Hunks {
		hunk := &file.Hunks[hi]
		var kept []string
		keptAny := false
		// Whether the previous line made it into the patch, for the
		// "\ No newline at end of file" marker that follows it.
		previousKept := true
		for ix, line := range hunk.Lines {
			switch {
			case strings.HasPrefix(line, `\`):
				if previousKept {
					kept = append(kept, line)
				}
				continue
			case !isChange(line):
				kept = append(kept, line)
				previousKept = true
			case keep(hunk, ix):
				kept = append(kept, line)
				keptAny = true
				previousKept = true
			case strings.HasPrefix(line, "-"):
				kept = append(kept, " "+line[1:])
				previousKept = true
			default:
				previousKept = false
			}
		}
		if !keptAny {
			continue
		}
		body.WriteString(hunk.Header)
		body.WriteByte('\n')
		for _, l := range kept {
			body.WriteString(l)
			body.WriteByte('\n')
		}
	}
	if body.Len() == 0 {
		return "", false
	}
	var out strings.Builder
	for _, l := range file.Header {
		out.WriteString(l)
		out.WriteByte('\n')
	}
	out.WriteString(body.String())
	return out.String(), true
}
