// Package changelists keeps JetBrains-style changelists: local changes
// sorted into named lists, one of them active, where new changes go. Git
// knows nothing of them; they are kept in the repository's git directory, in
// banyan/changelists.
package changelists

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Default is the list every repository starts with.
const Default = "Changes"

type Changelists struct {
	// names are in display order; never empty.
	names  []string
	active string
	// files maps each changed file to its list.
	files map[string]string
}

func New() *Changelists {
	return &Changelists{names: []string{Default}, active: Default, files: map[string]string{}}
}

func (c *Changelists) Names() []string { return c.names }
func (c *Changelists) Active() string  { return c.active }

// ListOf is the list path is in; files not yet sorted go to the active one.
func (c *Changelists) ListOf(path string) string {
	if list, ok := c.files[path]; ok {
		return list
	}
	return c.active
}

// Sync puts files that just changed into the active list, and forgets files
// that no longer have changes. It reports whether anything moved.
func (c *Changelists) Sync(changed []string) bool {
	set := make(map[string]bool, len(changed))
	for _, p := range changed {
		set[p] = true
	}
	moved := false
	for path := range c.files {
		if !set[path] {
			delete(c.files, path)
			moved = true
		}
	}
	for _, path := range changed {
		if _, ok := c.files[path]; !ok {
			c.files[path] = c.active
			moved = true
		}
	}
	return moved
}

func (c *Changelists) Create(name string) error {
	name, err := c.checkNewName(name)
	if err != nil {
		return err
	}
	c.names = append(c.names, name)
	return nil
}

func (c *Changelists) Rename(old, new string) error {
	if old == strings.TrimSpace(new) {
		return nil
	}
	new, err := c.checkNewName(new)
	if err != nil {
		return err
	}
	ix := slices.Index(c.names, old)
	if ix < 0 {
		return fmt.Errorf("there is no changelist named %s", old)
	}
	c.names[ix] = new
	if c.active == old {
		c.active = new
	}
	for path, list := range c.files {
		if list == old {
			c.files[path] = new
		}
	}
	return nil
}

// Delete removes a list; its files go to the active list, or to the first
// other one when it was the active list.
func (c *Changelists) Delete(name string) error {
	if len(c.names) == 1 {
		return errors.New("the last changelist cannot be deleted")
	}
	c.names = slices.DeleteFunc(c.names, func(n string) bool { return n == name })
	if c.active == name {
		c.active = c.names[0]
	}
	for path, list := range c.files {
		if list == name {
			c.files[path] = c.active
		}
	}
	return nil
}

func (c *Changelists) SetActive(name string) error {
	if !slices.Contains(c.names, name) {
		return fmt.Errorf("there is no changelist named %s", name)
	}
	c.active = name
	return nil
}

// MoveTo moves paths into list to, creating it if needed.
func (c *Changelists) MoveTo(paths []string, to string) error {
	if !slices.Contains(c.names, to) {
		if err := c.Create(to); err != nil {
			return err
		}
	}
	for _, p := range paths {
		c.files[p] = to
	}
	return nil
}

func (c *Changelists) checkNewName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "\n\t") {
		return "", errors.New("a changelist needs a name on one line")
	}
	if slices.Contains(c.names, name) {
		return "", fmt.Errorf("there is already a changelist named %s", name)
	}
	return name, nil
}

func parse(text string) *Changelists {
	c := &Changelists{files: map[string]string{}}
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "list":
			if value != "" && !slices.Contains(c.names, value) {
				c.names = append(c.names, value)
			}
		case "active":
			c.active = value
		case "file":
			if list, path, ok := strings.Cut(value, "\t"); ok {
				c.files[path] = list
			}
		}
	}
	if len(c.names) == 0 {
		c.names = []string{Default}
	}
	if !slices.Contains(c.names, c.active) {
		c.active = c.names[0]
	}
	for path, list := range c.files {
		if !slices.Contains(c.names, list) {
			delete(c.files, path)
		}
	}
	return c
}

func (c *Changelists) text() string {
	var b strings.Builder
	for _, name := range c.names {
		fmt.Fprintf(&b, "list=%s\n", name)
	}
	fmt.Fprintf(&b, "active=%s\n", c.active)
	paths := make([]string, 0, len(c.files))
	for p := range c.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if !strings.Contains(p, "\n") {
			fmt.Fprintf(&b, "file=%s\t%s\n", c.files[p], p)
		}
	}
	return b.String()
}

// Load reads the changelists of the repository whose git directory is
// gitDir.
func Load(gitDir string) *Changelists {
	text, err := os.ReadFile(filepath.Join(gitDir, "banyan", "changelists"))
	if err != nil {
		return New()
	}
	return parse(string(text))
}

func (c *Changelists) Save(gitDir string) error {
	dir := filepath.Join(gitDir, "banyan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "changelists"), []byte(c.text()), 0o644)
}
