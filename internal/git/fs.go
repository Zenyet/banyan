package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FileStat is what the repository's file system says of a path.
type FileStat struct {
	Size    int64
	ModTime time.Time
	Mode    os.FileMode
}

// ReadFile reads a file of the repository; path is absolute in the
// repository's file system, as Root and Path give it.
func (r *Repo) ReadFile(file string) ([]byte, error) {
	if r.ssh == nil {
		return os.ReadFile(file)
	}
	data, err := r.ssh.runScript(context.Background(), "cat "+QuoteArg(file), nil)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// WriteFile writes a file of the repository, through ssh for a remote one;
// the mode is its permissions.
func (r *Repo) WriteFile(file string, data []byte, mode os.FileMode) error {
	if r.ssh == nil {
		return os.WriteFile(file, data, mode)
	}
	script := "cat > " + QuoteArg(file) + " && chmod " + strconv.FormatUint(uint64(mode.Perm()), 8) + " " + QuoteArg(file)
	_, err := r.ssh.runScript(context.Background(), script, data)
	return err
}

// MkdirAll creates a directory of the repository and its parents.
func (r *Repo) MkdirAll(dir string, mode os.FileMode) error {
	if r.ssh == nil {
		return os.MkdirAll(dir, mode)
	}
	_, err := r.ssh.runScript(context.Background(), "mkdir -p "+QuoteArg(dir), nil)
	return err
}

// Remove removes one file of the repository.
func (r *Repo) Remove(file string) error {
	if r.ssh == nil {
		return os.Remove(file)
	}
	_, err := r.ssh.runScript(context.Background(), "rm -f "+QuoteArg(file), nil)
	return err
}

// RemoveAll removes a path of the repository with everything in it.
func (r *Repo) RemoveAll(file string) error {
	if r.ssh == nil {
		return os.RemoveAll(file)
	}
	_, err := r.ssh.runScript(context.Background(), "rm -rf "+QuoteArg(file), nil)
	return err
}

// ReadDirs returns the names of the directories in dir.
func (r *Repo) ReadDirs(dir string) ([]string, error) {
	if r.ssh == nil {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		return names, nil
	}
	out, err := r.ssh.runScript(context.Background(), "find "+QuoteArg(dir)+" -mindepth 1 -maxdepth 1 -type d -print0", nil)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		names = append(names, path.Base(p))
	}
	return names, nil
}

// Stat reports a file of the repository's size, time and permissions.
func (r *Repo) Stat(file string) (FileStat, error) {
	if r.ssh == nil {
		info, err := os.Stat(file)
		if err != nil {
			return FileStat{}, err
		}
		return FileStat{Size: info.Size(), ModTime: info.ModTime(), Mode: info.Mode().Perm()}, nil
	}
	// GNU stat, then the BSD one of macOS hosts; -c fails there.
	script := "stat -c '%s %Y %a' -- " + QuoteArg(file) + " 2>/dev/null || stat -f '%z %m %Lp' " + QuoteArg(file)
	out, err := r.ssh.runScript(context.Background(), script, nil)
	if err != nil {
		return FileStat{}, err
	}
	f := strings.Fields(string(out))
	if len(f) < 3 {
		return FileStat{}, fmt.Errorf("%s: cannot read the file's details", file)
	}
	size, errSize := strconv.ParseInt(f[0], 10, 64)
	sec, errTime := strconv.ParseInt(f[1], 10, 64)
	perm, errPerm := strconv.ParseUint(f[2], 8, 32)
	if errSize != nil || errTime != nil || errPerm != nil {
		return FileStat{}, fmt.Errorf("%s: cannot read the file's details", file)
	}
	return FileStat{Size: size, ModTime: time.Unix(sec, 0), Mode: os.FileMode(perm)}, nil
}

// Exists reports whether a path of the repository exists.
func (r *Repo) Exists(file string) bool { return r.existing(file)[0] }

// IsDir reports whether a path of the repository is a directory.
func (r *Repo) IsDir(file string) bool {
	if r.ssh == nil {
		info, err := os.Stat(file)
		return err == nil && info.IsDir()
	}
	_, err := r.ssh.runScript(context.Background(), "test -d "+QuoteArg(file), nil)
	return err == nil
}

// existing reports which of paths exist, in one ssh run for a remote
// repository.
func (r *Repo) existing(paths ...string) []bool {
	found := make([]bool, len(paths))
	if r.ssh == nil {
		for i, p := range paths {
			if _, err := os.Stat(p); err == nil {
				found[i] = true
			}
		}
		return found
	}
	var script strings.Builder
	for _, p := range paths {
		script.WriteString("if [ -e " + QuoteArg(p) + " ]; then echo 1; else echo 0; fi\n")
	}
	out, err := r.ssh.runScript(context.Background(), script.String(), nil)
	if err != nil {
		return found
	}
	ls := strings.Fields(string(out))
	for i := range paths {
		if i < len(ls) && ls[i] == "1" {
			found[i] = true
		}
	}
	return found
}

// Path joins rel to the repository root with the separators of the
// repository's file system: a remote repository's paths are always on a
// Unix host.
func (r *Repo) Path(rel ...string) string {
	parts := append([]string{r.root}, rel...)
	if r.ssh != nil {
		return path.Join(parts...)
	}
	for i := 1; i < len(parts); i++ {
		parts[i] = filepath.FromSlash(parts[i])
	}
	return filepath.Join(parts...)
}

// Remote reports whether the repository is on another machine, reached over
// SSH.
func (r *Repo) Remote() bool { return r.ssh != nil }

// SSHHost is the destination of the repository's ssh connection, "" for a
// repository on this machine.
func (r *Repo) SSHHost() string {
	if r.ssh == nil {
		return ""
	}
	return r.ssh.Host
}

// location is what the console and the settings name the repository by.
func (r *Repo) location() string {
	if r.ssh != nil {
		return SSHLocation(r.ssh.Host, r.root)
	}
	return r.root
}

// Location is where the repository is remembered and named: its folder, or
// ssh://user@host/path for one over SSH.
func (r *Repo) Location() string { return r.location() }

// Label names the repository in the interface: its folder, or
// user@host:path for one over SSH.
func (r *Repo) Label() string {
	if r.ssh == nil {
		return r.root
	}
	return r.ssh.Host + ":" + r.root
}

// join joins paths with the separators of the repository's file system.
func (r *Repo) join(parts ...string) string {
	if r.ssh != nil {
		return path.Join(parts...)
	}
	return filepath.Join(parts...)
}

// OpenAt opens another working tree of the repository, on the machine the
// repository is on.
func (r *Repo) OpenAt(dir string) (*Repo, error) {
	if r.ssh != nil {
		return OpenSSH(r.ssh.Host, dir)
	}
	return Open(dir)
}

// baseName is the last element of a path of the repository.
func (r *Repo) baseName(p string) string {
	if r.ssh != nil {
		return path.Base(p)
	}
	return filepath.Base(p)
}

// JoinPath joins parts with the separators of the repository's file
// system, as Path does with the root.
func (r *Repo) JoinPath(parts ...string) string { return r.join(parts...) }

// dirOf is the directory a path of the repository is in.
func (r *Repo) dirOf(p string) string {
	if r.ssh != nil {
		return path.Dir(p)
	}
	return filepath.Dir(p)
}

// ReadHead reads at most n bytes from the start of a file, as telling text
// from binary needs.
func (r *Repo) ReadHead(file string, n int) ([]byte, error) {
	if r.ssh == nil {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		buf := make([]byte, n)
		read, err := f.Read(buf)
		if err != nil && err != io.EOF {
			return nil, err
		}
		return buf[:read], nil
	}
	return r.ssh.runScript(context.Background(), "head -c "+strconv.Itoa(n)+" "+QuoteArg(file), nil)
}
