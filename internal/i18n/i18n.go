// Package i18n shows the interface in English or Simplified Chinese. The
// code writes its strings in English through T and Tf, which look up their
// translation in zh. Placeholders are {} in order; a translation may write
// {0}, {1} … to put them in another order. What git itself prints stays in
// English, as in JetBrains IDEs.
package i18n

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
)

var chinese atomic.Bool

// Apply uses the language the settings ask for, "en" or "zh"; anything else
// (nothing chosen yet) means the system's.
func Apply(setting string) {
	switch setting {
	case "zh":
		chinese.Store(true)
	case "en":
		chinese.Store(false)
	default:
		chinese.Store(systemPrefersChinese())
	}
}

// IsChinese reports whether the interface is in Chinese.
func IsChinese() bool { return chinese.Load() }

// T is text in the interface language.
func T(text string) string {
	if IsChinese() {
		if zh, ok := zh[text]; ok {
			return zh
		}
	}
	return text
}

// Tf is text translated, with its placeholders filled in.
func Tf(text string, args ...any) string {
	return fill(T(text), args)
}

func fill(template string, args []any) string {
	var out strings.Builder
	next := 0
	rest := template
	for {
		start := strings.IndexByte(rest, '{')
		if start < 0 {
			break
		}
		out.WriteString(rest[:start])
		after := rest[start+1:]
		end := strings.IndexByte(after, '}')
		if end < 0 || !allDigits(after[:end]) {
			out.WriteByte('{')
			rest = after
			continue
		}
		index := next
		if end == 0 {
			next++
		} else {
			n, err := strconv.Atoi(after[:end])
			if err != nil {
				n = -1
			}
			index = n
		}
		if index >= 0 && index < len(args) {
			out.WriteString(fmt.Sprint(args[index]))
		}
		rest = after[end+1:]
	}
	out.WriteString(rest)
	return out.String()
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// systemPrefersChinese reports whether the user's preferred language is
// Chinese.
func systemPrefersChinese() bool {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		value := os.Getenv(key)
		if value != "" && value != "C" && value != "POSIX" {
			return strings.HasPrefix(value, "zh")
		}
	}
	// Apps opened from the Finder get no LANG; ask macOS.
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
		if err == nil {
			for _, part := range strings.FieldsFunc(string(out), func(r rune) bool { return r == '(' || r == ',' || r == '\n' }) {
				part = strings.Trim(strings.TrimSpace(part), `"`)
				if part != "" {
					return strings.HasPrefix(part, "zh")
				}
			}
		}
	}
	return false
}
