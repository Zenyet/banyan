package app

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Zenyet/banyan/internal/ai"
	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/patch"
	"github.com/Zenyet/banyan/internal/settings"
)

// How much of the changes a model is shown: a commit message needs the
// shape of a change, not every line of a generated file.
const (
	// About 8k tokens of diff at the worst, enough for a commit message
	// and cheaper than sending a generated file whole.
	aiMaxDiff     = 32_000
	aiMaxFileDiff = 8_000
)

// aiSnapshot is what the commit will contain, copied from the view for the
// goroutine that reads the diffs.
type aiSnapshot struct {
	root     string
	branch   string
	entries  []git.StatusEntry
	excluded map[string][]patch.ChangedLine
}

func (cv *ChangesView) aiSnapshot() aiSnapshot {
	m := cv.ws.Model
	snap := aiSnapshot{root: m.Repo.Root(), branch: m.CurrentBranch, excluded: map[string][]patch.ChangedLine{}}
	for _, e := range m.Status {
		if !cv.included[e.Path] {
			continue
		}
		snap.entries = append(snap.entries, e)
		if ex := cv.excluded[e.Path]; len(ex) > 0 {
			lines := make([]patch.ChangedLine, 0, len(ex))
			for line := range ex {
				lines = append(lines, line)
			}
			snap.excluded[e.Path] = lines
		}
	}
	return snap
}

// request turns the snapshot into what the provider is told, reading the
// diffs off the main thread.
func (s aiSnapshot) request(repo *git.Repo, language string) (ai.Request, error) {
	req := ai.Request{Root: s.root, Branch: s.branch, Language: language}
	var diff strings.Builder
	for _, e := range s.entries {
		name := string(e.DisplayStatus()) + " " + e.Path
		if e.OrigPath != "" {
			name += " (from " + e.OrigPath + ")"
		}
		req.Files = append(req.Files, name)
		// Lock files and minified output say what kind of change happened
		// by their names; their diffs are noise.
		if noisyDiff(e.Path) {
			continue
		}
		text, err := repo.WorktreeDiff(&e, false)
		if err != nil {
			return req, err
		}
		// A partial commit shows only the lines that will go in.
		if ex := s.excluded[e.Path]; len(ex) > 0 {
			keep := func(h *patch.Hunk, ix int) bool {
				return !slices.Contains(ex, patch.ChangedLine{Header: h.Header, Index: ix})
			}
			if selected, ok := patch.Select(patch.Parse(text), keep); ok {
				text = selected
			}
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if len(text) > aiMaxFileDiff {
			text = truncateUTF8(text, aiMaxFileDiff) + "\n… (the rest of this file's diff was left out)\n"
		}
		room := aiMaxDiff - diff.Len()
		if room <= 0 {
			diff.WriteString("\n… (more changed files were left out)\n")
			break
		}
		if len(text) > room {
			text = truncateUTF8(text, room) + "\n… (the rest of the diff was left out)\n"
		}
		diff.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			diff.WriteByte('\n')
		}
	}
	req.Diff = diff.String()
	if len(req.Files) > 100 {
		req.Files = append(req.Files[:100], "…")
	}
	// The messages of the branch's own commits, for style and language.
	if commits, err := repo.Log(&git.LogFilter{Refs: "HEAD"}, 8); err == nil {
		for _, c := range commits {
			req.Recent = append(req.Recent, c.Subject)
		}
	}
	return req, nil
}

// noisyDiff reports whether a file's diff is not worth the tokens: package
// manager lock files and minified build output.
func noisyDiff(file string) bool {
	base := strings.ToLower(path.Base(file))
	switch base {
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml",
		"bun.lock", "bun.lockb", "cargo.lock", "go.sum", "composer.lock",
		"gemfile.lock", "poetry.lock", "uv.lock", "pipfile.lock", "mix.lock",
		"pubspec.lock", "packages.lock.json":
		return true
	}
	lower := strings.ToLower(file)
	return strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.css") ||
		strings.HasSuffix(lower, ".map")
}

// truncateUTF8 keeps at most n bytes without splitting a character.
func truncateUTF8(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

func aiConfigFromSettings(s *settings.Settings) ai.Config {
	return ai.Config{
		Provider:        ai.ProviderFromKey(s.AIProvider),
		Command:         strings.TrimSpace(s.AICommand),
		BaseURL:         strings.TrimSpace(s.AIBaseURL),
		Model:           strings.TrimSpace(s.AIModel),
		APIKey:          strings.TrimSpace(s.AIAPIKey),
		Instructions:    strings.TrimSpace(s.AIInstructions),
		AllowTools:      s.AIAllowTools,
		SubjectOnly:     s.AISubjectOnly,
		Style:           ai.StyleFromKey(s.AIStyle),
		MessageLanguage: s.AILanguage,
		ClientVersion:   Version,
	}
}

// generateCommitMessage asks the configured provider for a message from
// the checked changes, or stops one being written.
func (cv *ChangesView) generateCommitMessage() {
	if cv.generating {
		cv.cancelGenerate()
		return
	}
	cfg := settings.Read(aiConfigFromSettings)
	if cfg.Provider == ai.ProviderOff {
		openSettingsAt(settingsPageAI)
		cv.ws.toast("warning", i18n.T("Choose how to generate commit messages in Settings › AI"))
		return
	}
	if err := cfg.Validate(); err != nil {
		openSettingsAt(settingsPageAI)
		cv.ws.toast("warning", err.Error())
		return
	}
	if len(cv.included) == 0 {
		cv.ws.toast("warning", i18n.T("Select the changes to describe first"))
		return
	}
	snap := cv.aiSnapshot()
	before := cv.message
	language := "en"
	if i18n.IsChinese() {
		language = "zh"
	}
	ctx, cancel := context.WithCancel(context.Background())
	cv.generating = true
	cv.genSeq++
	seq := cv.genSeq
	cv.genCancel = cancel
	repo := cv.ws.Model.Repo
	go func() {
		req, err := snap.request(repo, language)
		var message string
		if err == nil {
			message, err = ai.Generate(ctx, cfg, req)
		}
		model.Post(func() {
			if seq != cv.genSeq {
				return
			}
			cv.generating, cv.genCancel = false, nil
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				cv.ws.toast("error", i18n.Tf("Could not write a commit message\n{}", err.Error()))
				return
			}
			// What the user typed while the message was written is kept;
			// the generated one is offered instead.
			focus := func() {
				if cv.ws.tab == tabCommit {
					cv.messageBox.Focus()
				}
			}
			if strings.TrimSpace(cv.message) == "" || cv.message == before {
				cv.message = message
				focus()
				return
			}
			if cv.ws.Shell != nil {
				cv.ws.Shell.ToastAction("", i18n.T("The commit message is ready"),
					i18n.T("Use It"), func() {
						cv.message = message
						focus()
					})
			}
		})
	}()
}

// cancelGenerate stops the provider and drops whatever it writes next.
func (cv *ChangesView) cancelGenerate() {
	if cv.genCancel != nil {
		cv.genCancel()
	}
	cv.genSeq++
	cv.generating, cv.genCancel = false, nil
}
