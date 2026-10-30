// Package settings keeps the preferences of the Settings window, recently
// opened repositories and recent commit messages. They are stored as
// key=value lines in Banyan/settings under the user's application data
// folder, and kept in memory so that views can read them while building.
package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
)

const (
	maxRecent   = 10
	maxMessages = 20
)

type ThemeChoice int

const (
	ThemeSystem ThemeChoice = iota
	ThemeLight
	ThemeDark
)

var AllThemes = []ThemeChoice{ThemeSystem, ThemeLight, ThemeDark}

func (t ThemeChoice) Label() string {
	switch t {
	case ThemeLight:
		return i18n.T("Light")
	case ThemeDark:
		return i18n.T("Dark")
	}
	return i18n.T("Sync with OS")
}

// Key is its name in the settings file.
func (t ThemeChoice) Key() string {
	switch t {
	case ThemeLight:
		return "light"
	case ThemeDark:
		return "dark"
	}
	return "system"
}

// Shortcut is a shortcut changed from the default: a command id and its
// keys, empty for none; see package keymap.
type Shortcut struct {
	ID, Keys string
}

type Settings struct {
	Theme ThemeChoice
	// FontSize is the base UI font size in DIPs; other text sizes scale
	// with it. Saved as ui_font_size: the font_size of the GPUI app was on
	// another scale.
	FontSize int
	// RelativeDates shows "3 minutes ago" rather than a date and time in
	// the log.
	RelativeDates bool
	// GitPath is the git executable; empty means git from the PATH.
	GitPath string
	// UpdateMethod is how Update Project brings in the tracked branch, by
	// default.
	UpdateMethod git.UpdateMethod
	// AutoFetchMinutes fetches in the background this often; 0 turns it
	// off.
	AutoFetchMinutes int
	// UnifiedDiff shows diffs unified instead of side by side.
	UnifiedDiff bool
	// IgnoreWhitespace leaves whitespace-only changes out of diffs.
	IgnoreWhitespace bool
	// Language is "en" or "zh"; empty until chosen, when the system's
	// language decides.
	Language string
	// LogPageSize is how many commits the log loads at a time.
	LogPageSize int
	// ProtectedBranches are branch names or * patterns, separated by spaces
	// or commas, that are never force pushed nor have pushed commits
	// rewritten.
	ProtectedBranches string
	// OpenInNewWindow opens repositories in a window of their own, not as
	// a tab.
	OpenInNewWindow bool
	Recent          []string
	// Session is the repositories open in the last window used, the one
	// shown first.
	Session []string
	// Messages are commit messages, newest first.
	Messages  []string
	Shortcuts []Shortcut
}

func Default() Settings {
	return Settings{
		FontSize:          13,
		RelativeDates:     true,
		UpdateMethod:      git.UpdateMerge,
		LogPageSize:       5000,
		ProtectedBranches: "main master",
	}
}

var (
	mu      sync.RWMutex
	current *Settings
)

func load() *Settings {
	if current == nil {
		var text string
		if path := file(); path != "" {
			text = readOrMigrate(path)
		}
		s := parse(text)
		current = &s
	}
	return current
}

// Get returns a copy of the current settings.
func Get() Settings {
	mu.Lock()
	defer mu.Unlock()
	s := *load()
	s.Recent = append([]string(nil), s.Recent...)
	s.Session = append([]string(nil), s.Session...)
	s.Messages = append([]string(nil), s.Messages...)
	s.Shortcuts = append([]Shortcut(nil), s.Shortcuts...)
	return s
}

// Read reads settings without copying them, e.g. while building a view.
func Read[R any](f func(*Settings) R) R {
	mu.Lock()
	defer mu.Unlock()
	return f(load())
}

// Update changes the settings and saves them.
func Update(f func(*Settings)) {
	mu.Lock()
	s := load()
	f(s)
	text := s.text()
	mu.Unlock()
	path := file()
	if path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(text), 0o644)
}

// IsProtected reports whether branch (a local name, or a remote one such as
// origin/main, matched without the remote) is protected.
func (s *Settings) IsProtected(branch string) bool {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	_, short, hasRemote := strings.Cut(branch, "/")
	for _, pattern := range strings.FieldsFunc(s.ProtectedBranches, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if globMatch(pattern, branch) || (hasRemote && globMatch(pattern, short)) {
			return true
		}
	}
	return false
}

// GitProgram is the git executable to run.
func (s *Settings) GitProgram() string {
	if p := strings.TrimSpace(s.GitPath); p != "" {
		return p
	}
	return "git"
}

func AddRecent(repo string) {
	Update(func(s *Settings) {
		s.Recent = prepend(s.Recent, repo, maxRecent)
	})
}

func RemoveRecent(repo string) {
	Update(func(s *Settings) { s.Recent = without(s.Recent, repo) })
}

func AddMessage(message string) {
	Update(func(s *Settings) {
		s.Messages = prepend(s.Messages, message, maxMessages)
	})
}

func prepend(list []string, item string, max int) []string {
	list = append([]string{item}, without(list, item)...)
	if len(list) > max {
		list = list[:max]
	}
	return list
}

func without(list []string, item string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != item {
			out = append(out, x)
		}
	}
	return out
}

// globMatch matches pattern, with * standing for any run of characters, /
// included.
func globMatch(pattern, text string) bool {
	parts := strings.Split(pattern, "*")
	rest, ok := strings.CutPrefix(text, parts[0])
	if !ok {
		return false
	}
	parts = parts[1:]
	if len(parts) == 0 {
		return rest == ""
	}
	last := parts[len(parts)-1]
	for _, part := range parts[:len(parts)-1] {
		ix := strings.Index(rest, part)
		if ix < 0 {
			return false
		}
		rest = rest[ix+len(part):]
	}
	return len(rest) >= len(last) && strings.HasSuffix(rest, last)
}

// escape keeps a value on one line.
func escape(value string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(value)
}

func unescape(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c != '\\' {
			out.WriteByte(c)
			continue
		}
		i++
		switch {
		case i >= len(value):
			out.WriteByte('\\')
		case value[i] == 'n':
			out.WriteByte('\n')
		default:
			out.WriteByte(value[i])
		}
	}
	return out.String()
}

// file is Banyan/settings in the platform's per-user application data
// folder.
func file() string {
	var dir string
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, "Library", "Application Support")
	case "windows":
		dir = os.Getenv("APPDATA")
	default:
		dir = os.Getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(dir) {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			dir = filepath.Join(home, ".config")
		}
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "Banyan", "settings")
}

// readOrMigrate reads the settings, or those of GitBrains, the app's
// earlier name, if Banyan has none yet.
func readOrMigrate(path string) string {
	if text, err := os.ReadFile(path); err == nil {
		return string(text)
	}
	old := filepath.Join(filepath.Dir(filepath.Dir(path)), "GitBrains", "settings")
	text, err := os.ReadFile(old)
	if err != nil {
		return ""
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, text, 0o644)
	return string(text)
}

func parse(text string) Settings {
	s := Default()
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
		if !ok {
			continue
		}
		switch key {
		case "theme":
			for _, t := range AllThemes {
				if t.Key() == value {
					s.Theme = t
				}
			}
		case "ui_font_size":
			if n, err := strconv.Atoi(value); err == nil {
				s.FontSize = n
			}
		case "dates":
			s.RelativeDates = value != "absolute"
		case "git":
			s.GitPath = value
		case "update":
			s.UpdateMethod = git.UpdateMethodFromKey(value)
		case "auto_fetch":
			s.AutoFetchMinutes, _ = strconv.Atoi(value)
		case "diff":
			s.UnifiedDiff = value == "unified"
		case "whitespace":
			s.IgnoreWhitespace = value == "ignore"
		case "language":
			s.Language = value
		case "log_page":
			if n, err := strconv.Atoi(value); err == nil {
				s.LogPageSize = n
			}
		case "protected":
			s.ProtectedBranches = value
		case "recent":
			s.Recent = append(s.Recent, value)
		case "session":
			s.Session = append(s.Session, value)
		case "open_in":
			s.OpenInNewWindow = value == "window"
		case "message":
			s.Messages = append(s.Messages, unescape(value))
		case "shortcut":
			id, keys, _ := strings.Cut(value, " ")
			s.Shortcuts = append(s.Shortcuts, Shortcut{ID: id, Keys: strings.TrimSpace(keys)})
		}
	}
	return s
}

func (s *Settings) text() string {
	var b strings.Builder
	line := func(key, value string) {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	choose := func(on bool, yes, no string) string {
		if on {
			return yes
		}
		return no
	}
	line("theme", s.Theme.Key())
	line("ui_font_size", strconv.Itoa(s.FontSize))
	line("dates", choose(s.RelativeDates, "relative", "absolute"))
	line("git", s.GitPath)
	line("update", s.UpdateMethod.Key())
	line("auto_fetch", strconv.Itoa(s.AutoFetchMinutes))
	line("diff", choose(s.UnifiedDiff, "unified", "split"))
	line("whitespace", choose(s.IgnoreWhitespace, "ignore", "show"))
	line("language", s.Language)
	line("log_page", strconv.Itoa(s.LogPageSize))
	line("protected", s.ProtectedBranches)
	line("open_in", choose(s.OpenInNewWindow, "window", "tab"))
	for _, repo := range s.Recent {
		line("recent", repo)
	}
	for _, repo := range s.Session {
		line("session", repo)
	}
	for _, message := range s.Messages {
		line("message", escape(message))
	}
	for _, sc := range s.Shortcuts {
		line("shortcut", sc.ID+" "+sc.Keys)
	}
	return b.String()
}

func init() {
	// git reads what it needs of the settings through these.
	git.Program = func() string { return Read(func(s *Settings) string { return s.GitProgram() }) }
	git.IgnoreWhitespace = func() bool { return Read(func(s *Settings) bool { return s.IgnoreWhitespace }) }
	git.IsProtected = func(branch string) bool { return Read(func(s *Settings) bool { return s.IsProtected(branch) }) }
}
