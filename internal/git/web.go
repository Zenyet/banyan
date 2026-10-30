package git

import (
	"fmt"
	"strings"
)

type HostKind int

const (
	GitHub HostKind = iota
	GitLab
	Bitbucket
	// Gitea covers Gitea, Forgejo and Codeberg.
	Gitea
	// OtherHost is anything else, linked to the way GitHub lays out its
	// pages.
	OtherHost
)

// WebRemote is the web page of a remote repository, for links to its
// commits and files.
type WebRemote struct {
	Kind HostKind
	// Base is e.g. https://github.com/owner/repo.
	Base string
}

// ParseWebRemote reads a remote's URL in any of git's forms:
// git@host:owner/repo.git, ssh://git@host:22/owner/repo,
// https://user@host/owner/repo.git.
func ParseWebRemote(url string) (WebRemote, bool) {
	url = strings.TrimSpace(url)
	var host, path string
	if _, rest, ok := strings.Cut(url, "://"); ok {
		authority, p, ok := strings.Cut(rest, "/")
		if !ok {
			return WebRemote{}, false
		}
		host = authority[strings.LastIndex(authority, "@")+1:]
		// A port only means something for ssh; the web page is on the
		// default one.
		if !strings.HasPrefix(url, "http") {
			host, _, _ = strings.Cut(host, ":")
		}
		path = p
	} else {
		// scp-like syntax; a local path has no host.
		authority, p, ok := strings.Cut(url, ":")
		if !ok || strings.Contains(authority, "/") || len(authority) < 2 {
			return WebRemote{}, false
		}
		host = authority[strings.LastIndex(authority, "@")+1:]
		path = p
	}
	path = strings.TrimRight(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	if host == "" || path == "" {
		return WebRemote{}, false
	}
	lower := strings.ToLower(host)
	kind := OtherHost
	switch {
	case strings.Contains(lower, "github"):
		kind = GitHub
	case strings.Contains(lower, "gitlab"):
		kind = GitLab
	case strings.Contains(lower, "bitbucket"):
		kind = Bitbucket
	case strings.Contains(lower, "gitea") || strings.Contains(lower, "forgejo") || strings.Contains(lower, "codeberg"):
		kind = Gitea
	}
	scheme := "https"
	if strings.HasPrefix(url, "http://") {
		scheme = "http"
	}
	return WebRemote{Kind: kind, Base: fmt.Sprintf("%s://%s/%s", scheme, host, path)}, true
}

// Name is "GitHub", or the host for an unknown kind.
func (w WebRemote) Name() string {
	switch w.Kind {
	case GitHub:
		return "GitHub"
	case GitLab:
		return "GitLab"
	case Bitbucket:
		return "Bitbucket"
	}
	_, rest, _ := strings.Cut(w.Base, "://")
	host, _, _ := strings.Cut(rest, "/")
	return host
}

func (w WebRemote) CommitURL(id string) string {
	switch w.Kind {
	case GitLab:
		return w.Base + "/-/commit/" + id
	case Bitbucket:
		return w.Base + "/commits/" + id
	}
	return w.Base + "/commit/" + id
}

// FileURL is a file as of rev, at line (from 1) when not 0.
func (w WebRemote) FileURL(rev, path string, line int) string {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		segments[i] = percentEncodeSegment(s)
	}
	path = strings.Join(segments, "/")
	var url string
	switch w.Kind {
	case GitLab:
		url = fmt.Sprintf("%s/-/blob/%s/%s", w.Base, rev, path)
	case Bitbucket:
		url = fmt.Sprintf("%s/src/%s/%s", w.Base, rev, path)
	case Gitea:
		url = fmt.Sprintf("%s/src/commit/%s/%s", w.Base, rev, path)
	default:
		url = fmt.Sprintf("%s/blob/%s/%s", w.Base, rev, path)
	}
	switch {
	case line == 0:
		return url
	case w.Kind == Bitbucket:
		return fmt.Sprintf("%s#lines-%d", url, line)
	}
	return fmt.Sprintf("%s#L%d", url, line)
}

// percentEncodeSegment escapes the characters a URL path segment can't
// hold.
func percentEncodeSegment(segment string) string {
	var out strings.Builder
	for i := 0; i < len(segment); i++ {
		b := segment[i]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte("-._~", b) >= 0 {
			out.WriteByte(b)
		} else {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}
