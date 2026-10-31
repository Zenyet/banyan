package app

import (
	"embed"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/settings"
)

//go:embed icons/*.svg
var iconFiles embed.FS

var icons = map[string]*ui.SVG{}

// icon is the icon of that name, from icons/ (MingCute and Phosphor).
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
var selectionBG = ui.Hex("#0a84ff").Alpha(0.2)

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
	ui.Row(c).Height(28).PaddingX(12).Children(func() {
		ui.Text(c, title).FontSize(t.FontSize - 1).FontWeight(600).TextColor(t.TextMuted).SingleLine()
	})
}

// muted is small secondary text.
func muted(c *ui.Context, text string) ui.Element {
	t := c.Theme()
	return ui.Text(c, text).FontSize(t.FontSize - 1).TextColor(t.TextMuted)
}

// button is a push button, a capsule as macOS draws them.
func button(c *ui.Context, label string) ui.Element { return ui.Button(c, label).Radius(14) }

// primaryButton is the default button of a dialog, a capsule of the
// accent's color.
func primaryButton(c *ui.Context, label string) ui.Element {
	return ui.PrimaryButton(c, label).Radius(14)
}

// filterButton opens a menu of a filter: a pill showing the filter's
// value, with a chevron.
func filterButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	b := button(c, "").Label(label).Height(24).PaddingX(10).Gap(4).Background(t.Text.Alpha(0.06)).Border(0, ui.Color{})
	b.Children(func() {
		ui.Text(c, label).FontSize(t.FontSize - 1).SingleLine()
		ui.Icon(c, icon("chevron-down")).FontSize(t.FontSize - 3).TextColor(t.TextMuted)
	})
	if b.Hovered() {
		b.Background(t.Text.Alpha(0.1))
	}
	return b
}

// segments is a track of segments, as macOS's segmented controls: the
// segment chosen lifts on a pill.
func segments(c *ui.Context, fn func()) ui.Element {
	t := c.Theme()
	return ui.Row(c).Height(26).Padding(2).Gap(2).Radius(13).Shrink(0).Background(t.Text.Alpha(0.06)).Children(fn)
}

// segment is a segment of a segments track, lifted when on.
func segment(c *ui.Context, label string, on bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Height(22).PaddingX(10).Radius(11).Center().Cursor(ui.CursorPointer)
	if on {
		selectedPill(b, t)
	} else if b.Hovered() {
		b.Background(t.Text.Alpha(0.06))
	}
	b.Children(func() {
		text := ui.Text(c, label).FontSize(t.FontSize - 1).SingleLine()
		if on {
			text.FontWeight(500)
		} else {
			text.TextColor(t.TextMuted)
		}
	})
	return b
}

// iconButton is a small borderless button showing an icon, with a tooltip.
func iconButton(c *ui.Context, name, tip string) ui.Element {
	t := c.Theme()
	b := button(c, "").Label(tip).Tooltip(tip).Padding(4).Background(ui.Color{}).Border(0, ui.Color{})
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
	b := button(c, "").Label(label).PaddingX(8).PaddingY(4).Background(ui.Color{}).Border(0, ui.Color{})
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

// chromeBG is the background of the window's bars, around the content, as
// a macOS window's toolbar.
func chromeBG(t *ui.Theme) ui.Color {
	if t.Dark {
		return ui.Hex("#262628")
	}
	return ui.Hex("#f6f6f8")
}

// glassGroup is a capsule of Liquid Glass holding controls, as macOS's
// toolbars group them.
func glassGroup(c *ui.Context, fn func()) ui.Element {
	return ui.Row(c).Height(36).Padding(3).Gap(2).Radius(18).Shrink(0).Material(glass.Glass{}).Children(fn)
}

// capsuleButton is a borderless button of a glassGroup: an icon, and a
// label unless compact, which then shows as its tooltip; menu adds a
// chevron, for a button opening a menu.
func capsuleButton(c *ui.Context, iconName, label string, compact, menu bool) ui.Element {
	t := c.Theme()
	b := button(c, "").Label(label).Height(30).PaddingX(10).Radius(15).Gap(6).Background(ui.Color{}).Border(0, ui.Color{})
	if compact {
		b.PaddingX(8).Tooltip(label)
	}
	b.Children(func() {
		ui.Icon(c, icon(iconName)).FontSize(t.FontSize + 2)
		if !compact {
			ui.Text(c, label).SingleLine().FontWeight(500)
		}
		if menu {
			ui.Icon(c, icon("chevron-down")).FontSize(t.FontSize - 3).TextColor(t.TextMuted)
		}
	})
	if b.Hovered() {
		b.Background(t.Text.Alpha(0.07))
	}
	return b
}

// selectedPill is the face of the chosen segment of a capsule, as macOS's
// segmented controls lift it.
func selectedPill(e ui.Element, t *ui.Theme) ui.Element {
	if t.Dark {
		return e.Background(ui.RGBA(255, 255, 255, 0.14))
	}
	return e.Background(ui.RGB(255, 255, 255)).Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.12))
}

// withSidebar gives a window a hidden title bar, its traffic lights inset
// over a sidebar of the system's material on macOS, as System Settings.
func withSidebar(o mygo.WindowOptions) mygo.WindowOptions {
	o.TitleBarStyle, o.TitleBarHeight = mygo.TitleBarHiddenInset, titleBarHeight
	if runtime.GOOS == "darwin" {
		o.Vibrancy = mygo.VibrancySidebar
	}
	return o
}

// sidebar is the sidebar of a window withSidebar, under the room its
// window controls take: of the window's material where it shows one, else
// of the chrome's gray.
func sidebar(c *ui.Context, width float32, fn func()) ui.Element {
	t := c.Theme()
	side := ui.Column(c.Key("sidebar")).Width(width).FillHeight().Shrink(0).BorderWidth(0, 1, 0, 0).BorderColor(t.Border)
	if !c.Vibrancy() {
		side.Background(chromeBG(t))
	}
	return side.Children(func() {
		ui.Box(c).Height(titleBarHeight).Shrink(0).DragWindow()
		fn()
	})
}

// paneTitle is the title of the pane beside a sidebar, in the window's
// title bar, which it drags, clear of the window controls on the right.
func paneTitle(c *ui.Context, title string) {
	t := c.Theme()
	bar := c.TitleBar()
	ui.Row(c.Key("title")).Height(titleBarHeight).Shrink(0).Padding(0, bar.Right+16, 0, 24).DragWindow().Children(func() {
		ui.Text(c, title).FontSize(t.FontSize + 4).Bold().SingleLine()
	})
}

// emptyMessage fills its parent with a muted message in the middle.
func emptyMessage(c *ui.Context, text string) {
	ui.Column(c).Grow(1).Center().Padding(16).Children(func() {
		ui.Text(c, text).TextColor(c.Theme().TextMuted).TextAlign(ui.Center)
	})
}

func sqrt(x float32) float32 { return float32(math.Sqrt(float64(x))) }

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }
