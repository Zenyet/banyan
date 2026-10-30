// Package console keeps what the Git console shows, like JetBrains'
// "Console" tab: every git command run by a user action, with its output.
// Background refreshes are not recorded.
package console

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxEntries = 500
	maxOutput  = 8000
)

type Entry struct {
	// Dir is the directory git ran in.
	Dir     string
	Command string
	Output  string
	OK      bool
	At      time.Time
	Millis  int64
}

var (
	mu      sync.Mutex
	entries []Entry
	version uint64
)

func Record(dir string, args []string, stdout, stderr string, ok bool, millis int64) {
	var parts []string
	for _, text := range []string{strings.TrimSpace(stdout), strings.TrimSpace(stderr)} {
		if text != "" {
			parts = append(parts, text)
		}
	}
	output := strings.Join(parts, "\n")
	if len(output) > maxOutput {
		end := maxOutput
		for end > 0 && !utf8.RuneStart(output[end]) {
			end--
		}
		output = output[:end] + "\n…"
	}
	mu.Lock()
	defer mu.Unlock()
	entries = append(entries, Entry{
		Dir:     dir,
		Command: "git " + strings.Join(args, " "),
		Output:  output,
		OK:      ok,
		At:      time.Now(),
		Millis:  millis,
	})
	if len(entries) > maxEntries {
		entries = append([]Entry(nil), entries[len(entries)-maxEntries:]...)
	}
	version++
}

// EntriesFor returns the entries run in root, plus the clone that created
// it.
func EntriesFor(root string) []Entry {
	mu.Lock()
	defer mu.Unlock()
	var out []Entry
	for _, e := range entries {
		if e.Dir == root || (strings.HasPrefix(e.Command, "git clone") && strings.HasSuffix(e.Command, root)) {
			out = append(out, e)
		}
	}
	return out
}

func Clear(root string) {
	mu.Lock()
	defer mu.Unlock()
	kept := entries[:0]
	for _, e := range entries {
		if e.Dir != root {
			kept = append(kept, e)
		}
	}
	entries = kept
	version++
}

// Version changes whenever an entry is recorded or cleared.
func Version() uint64 {
	mu.Lock()
	defer mu.Unlock()
	return version
}
