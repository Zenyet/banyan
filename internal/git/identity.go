package git

import (
	"os"
	"strings"
)

// Identity is who commits are made by: git's user.name and user.email.
type Identity struct{ Name, Email string }

// IsSet reports whether both the name and the email are given.
func (id Identity) IsSet() bool { return id.Name != "" && id.Email != "" }

// home is where git runs for what belongs to no repository.
func home() string {
	if dir, err := os.UserHomeDir(); err == nil {
		return dir
	}
	return os.TempDir()
}

// GlobalIdentity is the identity in the user's global git config, its
// fields empty where unset.
func GlobalIdentity() Identity {
	get := func(key string) string {
		out, err := run(home(), []string{"config", "--global", "--get", key}, false)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	return Identity{Name: get("user.name"), Email: get("user.email")}
}

// SetGlobalIdentity writes id to the user's global git config, unsetting
// a field left empty.
func SetGlobalIdentity(id Identity) error {
	for _, f := range []struct{ key, value string }{{"user.name", id.Name}, {"user.email", id.Email}} {
		value := strings.TrimSpace(f.value)
		if value == "" {
			// Unsetting what isn't set fails with status 5, which is fine.
			run(home(), []string{"config", "--global", "--unset", f.key}, true)
			continue
		}
		if _, err := run(home(), []string{"config", "--global", f.key, value}, true); err != nil {
			return err
		}
	}
	return nil
}

// FallbackIdent is who git makes commits by when no identity is set, as
// "zy <zy@zydeMacBook-Air.local>": the login and the computer's name. It
// is empty when git refuses to guess.
func FallbackIdent() string {
	out, err := run(home(), []string{"var", "GIT_AUTHOR_IDENT"}, false)
	if err != nil {
		return ""
	}
	// "Name <email> 1730000000 +0800": leave out the time.
	ident := strings.TrimSpace(out)
	if i := strings.LastIndexByte(ident, '>'); i >= 0 {
		ident = ident[:i+1]
	}
	return ident
}
