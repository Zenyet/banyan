package app

import (
	"embed"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/settings"
)

//go:embed icons/*.svg
var iconFiles embed.FS

var icons = map[string]*ui.SVG{}

// icon is the Lucide icon of that name, from icons/.
func icon(name string) *ui.SVG {
	if svg, ok := icons[name]; ok {
		return svg
	}
	data, err := iconFiles.ReadFile("icons/" + name + ".svg")
	if err != nil {
		panic("no icon " + name)
	}
	svg := ui.MustParseSVG(data)
	icons[name] = svg
	return svg
}

const monoFont = "monospace"

// laneColors are the graph's lane colors, close to the JetBrains palette.
var laneColors = []string{"#4d8ee8", "#e0a33d", "#58b25a", "#d85e8a", "#9a6ad6", "#2fb5b0", "#e06c4b", "#8fae3a", "#6a7fe0", "#c78a4a"}

func laneColor(ix int) ui.Color { return ui.Hex(laneColors[ix%len(laneColors)]) }

// selectionBG is the selected row's background, readable in both themes.
var selectionBG = ui.Hex("#4d8ee8").Alpha(0.22)

func hoverBG(t *ui.Theme) ui.Color { return t.SurfaceHover }

func statusColor(status byte, t *ui.Theme) ui.Color {
	switch status {
	case 'A':
		return ui.Hex("#58a65c")
	case 'D':
		return t.TextMuted
	case 'R', 'C', 'M', 'T':
		return ui.Hex("#4d8ee8")
	case '?':
		return ui.Hex("#c75450")
	case 'U':
		return ui.Hex("#d5756c")
	}
	return t.Text
}

// refColors are the background and text color of a ref label.
func refColors(kind git.RefKind, t *ui.Theme) (ui.Color, ui.Color) {
	switch kind {
	case git.RefHead:
		return ui.Hex("#f0c75e"), ui.Hex("#3c2f00")
	case git.RefLocal:
		return ui.Hex("#5f9e5f"), ui.RGB(255, 255, 255)
	case git.RefRemote:
		return ui.Hex("#8a6db8"), ui.RGB(255, 255, 255)
	}
	return t.Surface, t.Text
}

// fileCount is "1 file", "3 files".
func fileCount(n int) string {
	if n == 1 {
		return i18n.T("1 file")
	}
	return i18n.Tf("{} files", n)
}

func formatTime(timestamp int64) string {
	t := time.Unix(timestamp, 0)
	now := time.Now()
	delta := now.Sub(t)
	relative := settings.Read(func(s *settings.Settings) bool { return s.RelativeDates })
	switch {
	case delta < 0 || !relative:
		return t.Format("2006/01/02 15:04")
	case delta < time.Minute:
		return i18n.T("Moments ago")
	case delta < time.Hour:
		return i18n.Tf("{} minutes ago", int(delta.Minutes()))
	case sameDay(t, now):
		return i18n.Tf("Today {}", t.Format("15:04"))
	case sameDay(t, now.AddDate(0, 0, -1)):
		return i18n.Tf("Yesterday {}", t.Format("15:04"))
	}
	return t.Format("2006/01/02 15:04")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func formatFullTime(timestamp int64) string {
	return time.Unix(timestamp, 0).Format("2006/01/02 15:04:05")
}

// tilde writes a path under the home folder with ~.
func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if rest, ok := strings.CutPrefix(path, home); ok && (rest == "" || rest[0] == filepath.Separator) {
			return "~" + rest
		}
	}
	return path
}

// pathNode is a directory of a path tree, or a file when it has no
// children.
type pathNode struct {
	// Name is what the row shows: a file's name, or a directory's, with
	// single-child chains collapsed into it, like JetBrains does.
	Name string
	// Path is the full path of a file, or of a directory.
	Path     string
	Children []*pathNode
	IsDir    bool
}

// pathTree groups paths by directory, directories first.
func pathTree(paths []string) []*pathNode {
	type dir struct {
		dirs  map[string]*dir
		files []string
	}
	root := &dir{dirs: map[string]*dir{}}
	for _, p := range paths {
		node := root
		parts := strings.Split(p, "/")
		for _, part := range parts[:len(parts)-1] {
			next, ok := node.dirs[part]
			if !ok {
				next = &dir{dirs: map[string]*dir{}}
				node.dirs[part] = next
			}
			node = next
		}
		node.files = append(node.files, p)
	}
	var build func(d *dir, prefix string) []*pathNode
	build = func(d *dir, prefix string) []*pathNode {
		var out []*pathNode
		names := make([]string, 0, len(d.dirs))
		for name := range d.dirs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			child := d.dirs[name]
			for len(child.files) == 0 && len(child.dirs) == 1 {
				for sub, grandchild := range child.dirs {
					name += "/" + sub
					child = grandchild
				}
			}
			full := name
			if prefix != "" {
				full = prefix + "/" + name
			}
			out = append(out, &pathNode{Name: name, Path: full, IsDir: true, Children: build(child, full)})
		}
		files := append([]string(nil), d.files...)
		sort.Slice(files, func(i, j int) bool { return filepath.Base(files[i]) < filepath.Base(files[j]) })
		for _, f := range files {
			out = append(out, &pathNode{Name: f[strings.LastIndexByte(f, '/')+1:], Path: f})
		}
		return out
	}
	return build(root, "")
}

// treeRow is a row of a path tree as a list shows it.
type treeRow struct {
	Node  *pathNode
	Depth int
}

// flatten lists the rows of a tree shown, with the directories in closed
// left closed.
func flatten(nodes []*pathNode, closed map[string]bool) []treeRow {
	var rows []treeRow
	var walk func(nodes []*pathNode, depth int)
	walk = func(nodes []*pathNode, depth int) {
		for _, n := range nodes {
			rows = append(rows, treeRow{n, depth})
			if n.IsDir && !closed[n.Path] {
				walk(n.Children, depth+1)
			}
		}
	}
	walk(nodes, 0)
	return rows
}

// short is a revision as menus name it: a commit id cut to eight
// characters, a branch as it is.
func short(rev string) string {
	if len(rev) == 40 && isHex(rev) {
		return rev[:8]
	}
	return rev
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// panelHeader is the small title over a panel.
func panelHeader(c *ui.Context, title string) {
	t := c.Theme()
	ui.Row(c).Height(28).PaddingX(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, title).FontSize(t.FontSize - 1).Bold().TextColor(t.TextMuted).SingleLine()
	})
}

// muted is small secondary text.
func muted(c *ui.Context, text string) ui.Element {
	t := c.Theme()
	return ui.Text(c, text).FontSize(t.FontSize - 1).TextColor(t.TextMuted)
}

// iconButton is a small borderless button showing an icon, with a tooltip.
func iconButton(c *ui.Context, name, tip string) ui.Element {
	t := c.Theme()
	b := ui.Button(c, "").Label(tip).Tooltip(tip).Padding(4).Background(ui.Color{}).Border(0, ui.Color{})
	b.Children(func() { ui.Icon(c, icon(name)).FontSize(t.FontSize + 1) })
	if b.Hovered() {
		b.Background(hoverBG(t))
	}
	return b
}

// ghostButton is a borderless button with an optional icon before its
// label.
func ghostButton(c *ui.Context, iconName, label string) ui.Element {
	t := c.Theme()
	b := ui.Button(c, "").Label(label).PaddingX(8).PaddingY(4).Background(ui.Color{}).Border(0, ui.Color{})
	b.Children(func() {
		if iconName != "" {
			ui.Icon(c, icon(iconName)).FontSize(t.FontSize)
		}
		ui.Text(c, label).SingleLine()
	})
	if b.Hovered() {
		b.Background(hoverBG(t))
	}
	return b
}

// emptyMessage fills its parent with a muted message in the middle.
func emptyMessage(c *ui.Context, text string) {
	ui.Column(c).Grow(1).Center().Padding(16).Children(func() {
		ui.Text(c, text).TextColor(c.Theme().TextMuted).TextAlign(ui.Center)
	})
}

func sqrt(x float32) float32 { return float32(math.Sqrt(float64(x))) }

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }
