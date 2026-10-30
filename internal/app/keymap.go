package app

import (
	"runtime"
	"slices"
	"strings"

	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/settings"
)

// Command is a command that has (or can be given) a keyboard shortcut: what
// Find Action lists, what Settings › Keyboard Shortcuts lets you change, and
// what the menus bind.
//
// Keys are written as the GPUI app wrote them, with secondary- for ⌘ on
// macOS and Ctrl elsewhere, e.g. secondary-shift-k, so saved shortcuts
// carry over.
type Command struct {
	// ID is its name in the settings file.
	ID    string
	Label string
	// DefaultKeys is the shortcut out of the box; empty for none.
	DefaultKeys string
	// Repo means the command acts on the repository shown, and needs one.
	Repo bool
}

// Keys is the shortcut in effect: the user's, else the default.
func (c *Command) Keys() string {
	return settings.Read(func(s *settings.Settings) string {
		for _, sc := range s.Shortcuts {
			if sc.ID == c.ID {
				return sc.Keys
			}
		}
		return c.DefaultKeys
	})
}

func commands() []Command {
	return []Command{
		{"find_action", i18n.T("Find Action…"), "secondary-shift-a", true},
		{"commit", i18n.T("Commit…"), "secondary-k", true},
		{"push", i18n.T("Push…"), "secondary-shift-k", true},
		{"update", i18n.T("Update Project"), "secondary-t", true},
		{"fetch", i18n.T("Fetch"), "", true},
		{"apply_patch", i18n.T("Apply Patch…"), "", true},
		{"worktrees", i18n.T("Manage Worktrees…"), "", true},
		{"submodules", i18n.T("Submodules…"), "", true},
		{"show_log", i18n.T("Show Log"), "secondary-9", true},
		{"show_commit", i18n.T("Show Commit"), "secondary-0", true},
		{"show_console", i18n.T("Show Console"), "", true},
		{"search_log", i18n.T("Search the Log"), "secondary-f", true},
		{"go_to", i18n.T("Go to Hash, Branch or Tag"), "secondary-g", true},
		{"refresh", i18n.T("Refresh"), "secondary-r", true},
		{"next_repo", i18n.T("Next Repository"), "secondary-shift-]", true},
		{"previous_repo", i18n.T("Previous Repository"), "secondary-shift-[", true},
		{"fetch_all", i18n.T("Fetch All Repositories"), "", true},
		{"update_all", i18n.T("Update All Repositories"), "", true},
		{"open", i18n.T("Open Repository…"), "secondary-o", false},
		{"settings", i18n.T("Settings…"), "secondary-,", false},
		{"close_window", i18n.T("Close Window"), "secondary-w", false},
		{"quit", i18n.T("Quit Banyan"), "secondary-q", false},
	}
}

func commandByID(id string) *Command {
	all := commands()
	if ix := slices.IndexFunc(all, func(c Command) bool { return c.ID == id }); ix >= 0 {
		return &all[ix]
	}
	return nil
}

// validStroke reports whether a key of a shortcut is one the menus take.
func validStroke(stroke string) bool {
	parts := strings.Split(strings.ToLower(stroke), "-")
	key := parts[len(parts)-1]
	mods := parts[:len(parts)-1]
	// "secondary--" binds the minus key.
	if key == "" && len(parts) > 1 {
		key, mods = "-", parts[:len(parts)-2]
	}
	for _, m := range mods {
		switch m {
		case "secondary", "cmd", "ctrl", "alt", "shift":
		default:
			return false
		}
	}
	return key != ""
}

// accelerator is keys as mygo's menus take them, e.g. CmdOrCtrl+Shift+K,
// or "" when they don't parse. Menus take one stroke, so of a chord only
// its first.
func accelerator(keys string) string {
	strokes := strings.Fields(keys)
	if len(strokes) == 0 || !validStroke(strokes[0]) {
		return ""
	}
	parts := strings.Split(strings.ToLower(strokes[0]), "-")
	key := parts[len(parts)-1]
	mods := parts[:len(parts)-1]
	if key == "" {
		key, mods = "-", parts[:len(parts)-2]
	}
	var out []string
	for _, m := range mods {
		switch m {
		case "secondary":
			out = append(out, "CmdOrCtrl")
		case "cmd":
			out = append(out, "Super")
		case "ctrl":
			out = append(out, "Ctrl")
		case "alt":
			out = append(out, "Alt")
		case "shift":
			out = append(out, "Shift")
		}
	}
	switch key {
	case "enter":
		key = "Enter"
	case "up", "down", "left", "right", "home", "end", "escape", "tab", "space", "backspace", "delete":
		key = strings.ToUpper(key[:1]) + key[1:]
	case "pageup":
		key = "PageUp"
	case "pagedown":
		key = "PageDown"
	default:
		key = strings.ToUpper(key)
	}
	return strings.Join(append(out, key), "+")
}

// displayKeys is how keys read on this platform, e.g. ⇧⌘K or
// Ctrl+Shift+K.
func displayKeys(keys string) string {
	var strokes []string
	for _, s := range strings.Fields(keys) {
		strokes = append(strokes, displayStroke(s))
	}
	return strings.Join(strokes, " ")
}

func displayStroke(stroke string) string {
	mac := runtime.GOOS == "darwin"
	parts := strings.Split(stroke, "-")
	key := parts[len(parts)-1]
	mods := parts[:len(parts)-1]
	if key == "" && len(parts) > 1 {
		key, mods = "-", parts[:len(parts)-2]
	}
	var modifiers []string
	for _, m := range mods {
		var label string
		switch {
		case (m == "secondary" || m == "cmd") && mac:
			label = "⌘"
		case m == "secondary" || m == "ctrl" && !mac:
			label = "Ctrl"
		case m == "cmd":
			label = "Super"
		case m == "ctrl":
			label = "⌃"
		case m == "shift" && mac:
			label = "⇧"
		case m == "shift":
			label = "Shift"
		case m == "alt" && mac:
			label = "⌥"
		case m == "alt":
			label = "Alt"
		default:
			label = m
		}
		modifiers = append(modifiers, label)
	}
	switch key {
	case "enter":
		if mac {
			key = "↩"
		} else {
			key = "Enter"
		}
	case "up":
		key = "↑"
	case "down":
		key = "↓"
	case "left":
		key = "←"
	case "right":
		key = "→"
	case "pageup":
		key = "PgUp"
	case "pagedown":
		key = "PgDn"
	case "home":
		key = "Home"
	case "end":
		key = "End"
	case "escape":
		key = "Esc"
	default:
		key = strings.ToUpper(key)
	}
	if mac {
		// macOS lists ⌃ ⌥ ⇧ ⌘ in that order.
		order := map[string]int{"⌃": 0, "⌥": 1, "⇧": 2}
		slices.SortStableFunc(modifiers, func(a, b string) int {
			oa, ok := order[a]
			if !ok {
				oa = 3
			}
			ob, ok := order[b]
			if !ok {
				ob = 3
			}
			return oa - ob
		})
		return strings.Join(modifiers, "") + key
	}
	return strings.Join(append(modifiers, key), "+")
}
