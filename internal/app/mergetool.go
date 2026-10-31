package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/conflict"
	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
)

const (
	mergeLineHeight = 20
	mergeGutter     = 30
	// foldOver is how long a clean run gets before it's folded down to its
	// edges.
	foldOver    = 10
	foldContext = 3
)

// mergeTool is the conflict resolution dialog, like JetBrains' "Merge
// Revisions" window: yours on the left, theirs on the right and the result
// in the middle. Each conflict takes yours (»), theirs («), both, or text
// typed in.
type mergeTool struct {
	m            *model.Model
	path         string
	ours, theirs string
	segments     []conflict.Segment
	// resolutions has one per conflict, in order.
	resolutions []*conflict.Resolution
	// editing is the conflict whose result is being typed, -1 for none.
	editing  int
	text     string
	unfolded map[int]bool
	// current is the conflict the arrows last moved to; reveal scrolls to
	// it in the next frame.
	current    int
	reveal     bool
	undo, redo [][]*conflict.Resolution
}

// shown is a line as shown: without its ending, tabs as spaces.
func shown(line string) string { return strings.ReplaceAll(conflict.Display(line), "\t", "    ") }

func openMergeToolIn(s *Shell, m *model.Model, path string) {
	text, err := m.Repo.MergeConflictText(path)
	if err != nil {
		m.Fail(err)
		return
	}
	mt := &mergeTool{m: m, path: path, ours: "yours", theirs: "theirs", segments: conflict.Parse(text), editing: -1, unfolded: map[int]bool{}}
	if op := m.Operation; op != nil {
		mt.ours, mt.theirs = op.Ours, op.Theirs
	}
	for _, seg := range mt.segments {
		if seg.Conflict != nil {
			mt.resolutions = append(mt.resolutions, nil)
		}
	}
	s.Open(&Dialog{Title: i18n.Tf("Merge Revisions for {}", path), Width: 1280, Body: mt.view})
}

func (mt *mergeTool) record() {
	mt.undo = append(mt.undo, slices.Clone(mt.resolutions))
	mt.redo = nil
}

func (mt *mergeTool) set(ix int, r *conflict.Resolution) {
	if mt.editing == ix {
		mt.editing = -1
	}
	same := (mt.resolutions[ix] == nil) == (r == nil) && (r == nil || *mt.resolutions[ix] == *r)
	if !same {
		mt.record()
		mt.resolutions[ix] = r
	}
}

// accept takes one side. Taking the other side of a conflict that already
// took one keeps both, in the order they were taken, as JetBrains does.
func (mt *mergeTool) accept(ix int, ours bool) {
	kind := conflict.Theirs
	switch r := mt.resolutions[ix]; {
	case r != nil && r.Kind == conflict.Theirs && ours:
		kind = conflict.TheirsThenOurs
	case r != nil && r.Kind == conflict.Ours && !ours:
		kind = conflict.OursThenTheirs
	case ours:
		kind = conflict.Ours
	}
	mt.set(ix, &conflict.Resolution{Kind: kind})
}

func (mt *mergeTool) acceptAll(ours bool) {
	mt.editing = -1
	kind := conflict.Theirs
	if ours {
		kind = conflict.Ours
	}
	mt.record()
	for i := range mt.resolutions {
		mt.resolutions[i] = &conflict.Resolution{Kind: kind}
	}
}

func (mt *mergeTool) undoStep() {
	if n := len(mt.undo); n > 0 {
		mt.redo = append(mt.redo, mt.resolutions)
		mt.resolutions, mt.undo = mt.undo[n-1], mt.undo[:n-1]
		mt.editing = -1
	}
}

func (mt *mergeTool) redoStep() {
	if n := len(mt.redo); n > 0 {
		mt.undo = append(mt.undo, mt.resolutions)
		mt.resolutions, mt.redo = mt.redo[n-1], mt.redo[:n-1]
		mt.editing = -1
	}
}

func (mt *mergeTool) conflictAt(ix int) *conflict.Conflict {
	n := 0
	for _, seg := range mt.segments {
		if seg.Conflict != nil {
			if n == ix {
				return seg.Conflict
			}
			n++
		}
	}
	return nil
}

func (mt *mergeTool) edit(ix int) {
	c := mt.conflictAt(ix)
	r := mt.resolutions[ix]
	if r == nil {
		r = &conflict.Resolution{Kind: conflict.OursThenTheirs}
	}
	mt.text = strings.TrimRight(strings.Join(c.Resolved(*r), ""), "\n")
	mt.editing = ix
}

func (mt *mergeTool) finishEdit() {
	if mt.editing >= 0 {
		mt.record()
		mt.resolutions[mt.editing] = &conflict.Resolution{Kind: conflict.Custom, Text: mt.text}
		mt.editing = -1
	}
}

// goTo moves to the next or previous conflict, preferring unresolved ones.
func (mt *mergeTool) goTo(forward bool) {
	total := len(mt.resolutions)
	if total == 0 {
		return
	}
	var order []int
	for step := 1; step <= total; step++ {
		if forward {
			order = append(order, (mt.current+step)%total)
		} else {
			order = append(order, (mt.current+total-step)%total)
		}
	}
	mt.current = order[0]
	for _, ix := range order {
		if mt.resolutions[ix] == nil {
			mt.current = ix
			break
		}
	}
	mt.reveal = true
}

func (mt *mergeTool) apply(d *Dialog) {
	mt.finishEdit()
	text, ok := conflict.ResultText(mt.segments, mt.resolutions)
	if !ok {
		return
	}
	path := mt.path
	d.Close()
	model.Run(mt.m, i18n.Tf("Resolve {}", path), func(r *git.Repo) (string, error) {
		if err := os.WriteFile(filepath.Join(r.Root(), filepath.FromSlash(path)), []byte(text), 0o644); err != nil {
			return "", err
		}
		return r.ResolveConflict(path, nil)
	})
}

func (mt *mergeTool) view(c *ui.Context, d *Dialog) {
	t := c.Theme()
	total := len(mt.resolutions)
	resolved := 0
	for _, r := range mt.resolutions {
		if r != nil {
			resolved++
		}
	}
	done := resolved == total && mt.editing < 0
	// ⌘Z / Ctrl+Z undoes a choice, ⌘⇧Z redoes it; while a result is being
	// typed the text box gets the keys first.
	if mt.editing < 0 {
		if c.Shortcut(ui.Cmd, ui.KeyZ) {
			mt.undoStep()
		}
		if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyZ) {
			mt.redoStep()
		}
	}
	title := func(label, name string) {
		ui.Row(c).Grow(1).Basis(0).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, label).FontWeight(600)
			muted(c, name).SingleLine()
		})
	}
	ui.Row(c.Key("header")).Children(func() {
		title(i18n.T("Yours"), mt.ours)
		ui.Box(c).Width(mergeGutter)
		ui.Row(c).Grow(1).Basis(0).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, i18n.T("Result")).FontWeight(600).Grow(1)
			if iconButton(c.Key("prev"), "arrow-up", i18n.T("Previous Conflict")).Clicked() {
				mt.goTo(false)
			}
			if iconButton(c.Key("next"), "arrow-down", i18n.T("Next Conflict")).Clicked() {
				mt.goTo(true)
			}
		})
		ui.Box(c).Width(mergeGutter)
		title(i18n.T("Theirs"), mt.theirs)
	})
	ui.Scroll(c.Key("rows")).MaxHeight(580).Border(1, t.Border).Radius(t.Radius).Font(monoFont).Children(func() {
		line, ix := 1, 0
		for si, seg := range mt.segments {
			if seg.Conflict == nil {
				mt.cleanRows(c, si, seg.Clean, line)
				line += len(seg.Clean)
				continue
			}
			row := mt.conflictRows(c, ix, seg.Conflict, line)
			if mt.reveal && ix == mt.current {
				row.ScrollIntoView()
				mt.reveal = false
			}
			if r := mt.resolutions[ix]; r != nil {
				line += len(seg.Conflict.Resolved(*r))
			}
			ix++
		}
	})
	status := i18n.Tf("{} of {} conflicts resolved", resolved, total)
	if total == 0 {
		status = i18n.T("No conflicts left; the changes merged cleanly")
	}
	undoKeys, redoKeys := "Ctrl+Z", "Ctrl+Shift+Z"
	if runtime.GOOS == "darwin" {
		undoKeys, redoKeys = "⌘Z", "⇧⌘Z"
	}
	ui.Row(c.Key("footer")).Gap(8).Children(func() {
		if button(c.Key("yours"), i18n.T("Accept Yours")).Tooltip(i18n.T("Take your side for every conflict")).Clicked() {
			mt.acceptAll(true)
		}
		if button(c.Key("theirs"), i18n.T("Accept Theirs")).Tooltip(i18n.T("Take their side for every conflict")).Clicked() {
			mt.acceptAll(false)
		}
		if iconButton(c.Key("undo"), "undo-2", i18n.Tf("Undo ({})", undoKeys)).Disabled(len(mt.undo) == 0).Clicked() {
			mt.undoStep()
		}
		if iconButton(c.Key("redo"), "redo-2", i18n.Tf("Redo ({})", redoKeys)).Disabled(len(mt.redo) == 0).Clicked() {
			mt.redoStep()
		}
		color := t.TextMuted
		if done {
			color = t.Success
		}
		ui.Text(c, status).TextColor(color).Grow(1)
		if button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
			d.Close()
		}
		if primaryButton(c.Key("apply"), i18n.T("Apply")).Disabled(!done).Clicked() {
			mt.apply(d)
		}
	})
}

// lines shows lines in color, numbered from first when not 0. Long lines
// wrap; every column keeps the line-number gutter so they wrap alike.
func mergeLines(c *ui.Context, lines []string, first int, color ui.Color) {
	t := c.Theme()
	for ix, l := range lines {
		ui.Row(c.Key(ix)).AlignItems(ui.Start).MinHeight(mergeLineHeight).Children(func() {
			number := ""
			if first > 0 {
				number = fmt.Sprint(first + ix)
			}
			ui.Text(c, number).Width(36).PaddingEnd(8).TextAlign(ui.End).TextColor(t.TextMuted).FontSize(t.FontSize - 1)
			ui.Text(c, shown(l)).Grow(1).Shrink(1).MinWidth(0).PaddingStart(8).TextColor(color)
		})
	}
}

func (mt *mergeTool) cleanRows(c *ui.Context, si int, lines []string, first int) {
	t := c.Theme()
	threeColumns := func(key string, lines []string, first int) {
		ui.Row(c.Key(key)).AlignItems(ui.Start).Children(func() {
			ui.Column(c.Key("ours")).Grow(1).Basis(0).MinWidth(0).Children(func() { mergeLines(c, lines, 0, t.TextMuted) })
			ui.Box(c).Width(mergeGutter)
			ui.Column(c.Key("result")).Grow(1).Basis(0).MinWidth(0).Children(func() { mergeLines(c, lines, first, t.Text) })
			ui.Box(c).Width(mergeGutter)
			ui.Column(c.Key("theirs")).Grow(1).Basis(0).MinWidth(0).Children(func() { mergeLines(c, lines, 0, t.TextMuted) })
		})
	}
	head, tail := foldContext, foldContext
	if si == 0 {
		head = 0
	}
	if si+1 == len(mt.segments) {
		tail = 0
	}
	ui.Column(c.Key(si)).Children(func() {
		if len(lines) <= foldOver || mt.unfolded[si] || head+tail >= len(lines) {
			threeColumns("all", lines, first)
			return
		}
		hidden := len(lines) - head - tail
		threeColumns("top", lines[:head], first)
		fold := ui.Row(c.Key("fold")).Height(mergeLineHeight).Center().Background(t.Surface).Cursor(ui.CursorPointer)
		if fold.Clicked() {
			mt.unfolded[si] = true
		}
		fold.Children(func() { muted(c, i18n.Tf("⋯ {} lines without conflicts", hidden)) })
		threeColumns("bottom", lines[len(lines)-tail:], first+head+hidden)
	})
}

func (mt *mergeTool) conflictRows(c *ui.Context, ix int, cf *conflict.Conflict, first int) ui.Element {
	t := c.Theme()
	r := mt.resolutions[ix]
	oursBG, theirsBG := ui.Hex("#3b82f6").Alpha(0.12), ui.Hex("#f59e0b").Alpha(0.12)
	unresolvedBG, resolvedBG := ui.Hex("#ef4444").Alpha(0.12), ui.Hex("#22c55e").Alpha(0.14)
	usesOurs, usesTheirs := true, true
	if r != nil {
		switch r.Kind {
		case conflict.Ours:
			usesTheirs = false
		case conflict.Theirs:
			usesOurs = false
		case conflict.Custom:
			usesOurs, usesTheirs = false, false
		}
	}
	noLines := func(text string) {
		ui.Text(c, text).Italic().TextColor(t.TextMuted).Height(mergeLineHeight).PaddingStart(8)
	}
	side := func(key string, lines []string, used bool, bg ui.Color) {
		col := ui.Column(c.Key(key)).Grow(1).Basis(0).MinWidth(0).PaddingY(4).Background(bg)
		if !used {
			col.Opacity(0.45)
		}
		col.Children(func() {
			if len(lines) == 0 {
				noLines(i18n.T("(no lines)"))
			}
			mergeLines(c, lines, 0, t.Text)
		})
	}
	arrow := func(key, name, tip string, ours bool) {
		ui.Column(c.Key(key)).Width(mergeGutter).Padding(4, 0, 0, 0).AlignItems(ui.Center).Children(func() {
			if iconButton(c.Key("b"), name, tip).Clicked() {
				mt.accept(ix, ours)
			}
		})
	}
	toolbar := func(resolved bool) {
		ui.Row(c.Key("toolbar")).Height(24).PaddingX(4).Gap(4).Font("").FontSize(t.FontSize - 1).Children(func() {
			label, color := i18n.T("Conflict · base shown"), t.Danger
			if resolved {
				label, color = i18n.T("Resolved"), t.Success
			}
			ui.Text(c, label).TextColor(color).Grow(1).PaddingStart(4)
			if !resolved && toggleButton(c.Key("both"), i18n.T("Accept Both"), false).Tooltip(i18n.T("Yours, then theirs")).Clicked() {
				mt.set(ix, &conflict.Resolution{Kind: conflict.OursThenTheirs})
			}
			if iconButton(c.Key("edit"), "pencil", i18n.T("Edit the Result")).Clicked() {
				mt.edit(ix)
			}
			if resolved && iconButton(c.Key("undo"), "undo-2", i18n.T("Undo")).Clicked() {
				mt.set(ix, nil)
			}
		})
	}
	return ui.Row(c.Key(fmt.Sprintf("conflict-%d", ix))).AlignItems(ui.Stretch).MarginY(4).Children(func() {
		side("ours", cf.Ours, usesOurs, oursBG)
		arrow("take-ours", "chevrons-right", i18n.T("Accept Yours"), true)
		middle := ui.Column(c.Key("middle")).Grow(1).Basis(0).MinWidth(0)
		switch {
		case mt.editing == ix:
			middle.Padding(4).Gap(4).Background(resolvedBG).Children(func() {
				ui.TextArea(c.Key("text"), &mt.text).Lines(3, 20).FillWidth().AutoFocus()
				ui.Row(c).Gap(4).Justify(ui.End).Font("").Children(func() {
					if toggleButton(c.Key("cancel"), i18n.T("Cancel"), false).Clicked() {
						mt.editing = -1
					}
					if primaryButton(c.Key("done"), i18n.T("Done")).Clicked() {
						mt.finishEdit()
					}
				})
			})
		case r != nil:
			middle.Background(resolvedBG).Children(func() {
				toolbar(true)
				lines := cf.Resolved(*r)
				if len(lines) == 0 {
					noLines(i18n.T("(no lines)"))
				}
				mergeLines(c, lines, first, t.Text)
			})
		default:
			middle.Background(unresolvedBG).Children(func() {
				toolbar(false)
				if len(cf.Base) == 0 {
					noLines(i18n.T("(not in the common ancestor)"))
				}
				mergeLines(c, cf.Base, 0, t.TextMuted)
			})
		}
		arrow("take-theirs", "chevrons-left", i18n.T("Accept Theirs"), false)
		side("theirs", cf.Theirs, usesTheirs, theirsBG)
	})
}
