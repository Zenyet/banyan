// Package ai writes commit messages from the changes to commit. It talks to
// a model over HTTP, or to an agent the user already runs, over the Agent
// Client Protocol (ACP).
package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Zenyet/banyan/internal/i18n"
)

// Provider is where commit messages come from.
type Provider string

const (
	// ProviderOff turns AI commit messages off.
	ProviderOff Provider = ""
	// ProviderACP runs an ACP agent: the one the user already uses, with
	// its own login and model.
	ProviderACP Provider = "acp"
	// ProviderOpenAI calls OpenAI's chat completions.
	ProviderOpenAI Provider = "openai"
	// ProviderAnthropic calls Anthropic's messages API.
	ProviderAnthropic Provider = "anthropic"
	// ProviderCompatible calls any OpenAI-compatible chat completions, such
	// as Ollama, LM Studio or OpenRouter.
	ProviderCompatible Provider = "compatible"
)

var AllProviders = []Provider{ProviderOff, ProviderACP, ProviderOpenAI, ProviderAnthropic, ProviderCompatible}

// Style is the shape of the subject line.
type Style string

const (
	// StyleRepository follows the repository's recent messages.
	StyleRepository Style = ""
	// StyleConventional uses Conventional Commits' type prefixes.
	StyleConventional Style = "conventional"
	// StyleGitmoji starts the subject with an emoji.
	StyleGitmoji Style = "gitmoji"
	// StylePlain writes the subject without a prefix or emoji.
	StylePlain Style = "plain"
	// StyleCodeBuddy follows CodeBuddy's commit message style: the
	// repository's conventions, a short subject, and a body for a change
	// that spans several parts.
	StyleCodeBuddy Style = "codebuddy"
)

var AllStyles = []Style{StyleRepository, StyleCodeBuddy, StyleConventional, StyleGitmoji, StylePlain}

// Label is the style's name in the interface language.
func (s Style) Label() string {
	switch s {
	case StyleConventional:
		return i18n.T("Conventional Commits")
	case StyleGitmoji:
		return i18n.T("Gitmoji")
	case StylePlain:
		return i18n.T("Plain")
	case StyleCodeBuddy:
		return "CodeBuddy"
	}
	return i18n.T("Match the repository")
}

// StyleFromKey is the style saved under key, the repository's for none.
func StyleFromKey(key string) Style {
	for _, s := range AllStyles {
		if string(s) == key {
			return s
		}
	}
	return StyleRepository
}

// Label is the provider's name in the interface language.
func (p Provider) Label() string {
	switch p {
	case ProviderACP:
		return i18n.T("ACP agent")
	case ProviderOpenAI:
		return "OpenAI"
	case ProviderAnthropic:
		return "Anthropic"
	case ProviderCompatible:
		return i18n.T("OpenAI-compatible")
	}
	return i18n.T("Off")
}

// ProviderFromKey is the provider saved under key, off for none.
func ProviderFromKey(key string) Provider {
	for _, p := range AllProviders {
		if string(p) == key {
			return p
		}
	}
	return ProviderOff
}

// Config is how commit messages are generated, as the settings keep it.
type Config struct {
	Provider Provider
	// Command is the command of the ACP agent, run in the repository.
	Command string
	// BaseURL is the HTTP API's base; empty means the provider's own.
	BaseURL string
	Model   string
	APIKey  string
	// Instructions are extra words for the model, such as a commit style.
	Instructions string
	// AllowTools lets an ACP agent use its tools; it may then run commands
	// in the repository.
	AllowTools bool
	// SubjectOnly writes the subject line alone, as git logs show it. Off,
	// the model may add a body after a blank line.
	SubjectOnly bool
	// Style is the shape of the subject, or the repository's for none.
	Style Style
	// MessageLanguage is "repository" to match recent commits' language,
	// "en" or "zh" to choose one, or empty for the interface language.
	MessageLanguage string
	// ClientVersion is Banyan's version, which ACP agents are told.
	ClientVersion string
}

// Validate says what keeps the configuration from generating a message.
func (c Config) Validate() error {
	switch c.Provider {
	case ProviderACP:
		if strings.TrimSpace(c.Command) == "" {
			return errors.New(i18n.T("Set the command of the ACP agent"))
		}
	case ProviderOpenAI, ProviderAnthropic:
		if strings.TrimSpace(c.Model) == "" {
			return errors.New(i18n.T("Set the model"))
		}
		if strings.TrimSpace(c.APIKey) == "" {
			return errors.New(i18n.T("Set the API key"))
		}
	case ProviderCompatible:
		if strings.TrimSpace(c.BaseURL) == "" {
			return errors.New(i18n.T("Set the base URL"))
		}
		if strings.TrimSpace(c.Model) == "" {
			return errors.New(i18n.T("Set the model"))
		}
	case ProviderOff:
		return errors.New(i18n.T("Choose how to generate commit messages in Settings › AI"))
	default:
		return errors.New(i18n.T("Unknown AI provider"))
	}
	return nil
}

// Request is what the model is told about the commit.
type Request struct {
	// Root is the repository's folder.
	Root string
	// Branch is the current branch, or HEAD.
	Branch string
	// Files are the changes to commit, one "M path" per file.
	Files []string
	// Diff is the patch of exactly those changes, already shortened when
	// it is very long.
	Diff string
	// Recent are recent commit messages of the repository, for style and
	// language.
	Recent []string
	// Language is the interface language, "en" or "zh", used when the
	// recent messages say nothing about the language to write in.
	Language string
}

// Check tests the configured provider with a small request and says what
// answered, as "OpenAI · gpt-4.1-mini" or the ACP agent's name.
func Check(ctx context.Context, cfg Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	if cfg.Provider == ProviderACP {
		return checkACP(ctx, cfg)
	}
	return checkHTTP(ctx, cfg)
}

// Generate writes a commit message through the configured provider.
func Generate(ctx context.Context, cfg Config, req Request) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	text := prompt(cfg, req)
	var out string
	var err error
	switch cfg.Provider {
	case ProviderACP:
		out, err = generateACP(ctx, cfg, req, text)
	case ProviderOpenAI, ProviderAnthropic, ProviderCompatible:
		out, err = generateHTTP(ctx, cfg, text)
	default:
		err = errors.New(i18n.T("Unknown AI provider"))
	}
	if err != nil {
		return "", err
	}
	message := Clean(out)
	if cfg.SubjectOnly {
		message = firstLine(message)
	}
	if message == "" {
		return "", errors.New(i18n.T("The provider returned no message"))
	}
	return message, nil
}

// prompt is what the provider is asked. It stands on its own: the diff of
// the commit is included, so an agent needs no tools to see the changes.
func prompt(cfg Config, req Request) string {
	var b strings.Builder
	b.WriteString("Write a Git commit message for the changes below.\n\n")
	if name := strings.TrimSpace(lastPath(req.Root)); name != "" {
		b.WriteString("Repository: ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = "HEAD"
	}
	b.WriteString("Current branch: ")
	b.WriteString(branch)
	b.WriteString("\n\n")
	if len(req.Files) > 0 {
		b.WriteString("Files that will be committed:\n")
		for _, f := range req.Files {
			b.WriteString(f)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	if len(req.Recent) > 0 {
		b.WriteString("Recent commit messages, for their style and language:\n")
		for _, m := range req.Recent {
			m = strings.TrimSpace(m)
			if m != "" {
				b.WriteString("- ")
				b.WriteString(m)
				b.WriteByte('\n')
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString("The changes, as a unified diff of exactly what will be committed:\n")
	b.WriteString("<diff>\n")
	b.WriteString(req.Diff)
	if !strings.HasSuffix(req.Diff, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("</diff>\n\n")
	subjectLimit := 72
	if cfg.Style == StyleCodeBuddy {
		subjectLimit = 50
	}
	b.WriteString("Rules:\n")
	b.WriteString("- Reply with only the commit message: no preamble, quotes or code fences.\n")
	b.WriteString(fmt.Sprintf("- Write one short line: a subject of at most %d characters, in the imperative mood, saying what the key change is.\n", subjectLimit))
	b.WriteString("- Be specific, with concrete details; do not list file names. Do not end the subject with a period.\n")
	if cfg.SubjectOnly {
		b.WriteString("- Do not add a body, description, explanation or bullet points.\n")
	} else if cfg.Style == StyleCodeBuddy {
		b.WriteString("- If the change spans several parts or is complex, add a body after a blank line, with one \"- \" line per part, each at most 72 characters.\n")
		b.WriteString("- A simple change needs only the subject.\n")
	} else {
		b.WriteString("- Leave a blank line before the body. Add a short body only when it explains why the change was made, with lines of at most 72 characters.\n")
	}
	b.WriteString("- Describe what changed and why, only from the diff; do not invent changes.\n")
	// The message's language: the interface's unless chosen otherwise, the
	// repository's when asked for.
	language := languageName(req.Language)
	matchLanguage := false
	switch cfg.MessageLanguage {
	case "repository":
		matchLanguage = true
	case "en":
		language = "English"
	case "zh":
		language = "Simplified Chinese"
	}
	if matchLanguage {
		b.WriteString("- Match the language of the recent commit messages. If there are none, write in ")
		b.WriteString(language)
		b.WriteString(".\n")
	} else {
		b.WriteString("- Write in ")
		b.WriteString(language)
		if cfg.MessageLanguage == "" {
			b.WriteString(", the language of Banyan's interface")
		}
		b.WriteString("; if the additional instructions ask for another language, follow them.\n")
	}
	if cfg.Style == StyleRepository || cfg.Style == StyleCodeBuddy {
		b.WriteString("- Match the style of the recent commit messages, including a type prefix such as fix: or feat: if they use one.\n")
	}
	switch cfg.Style {
	case StyleCodeBuddy:
		b.WriteString("- Prefer the repository's own conventions over a fixed format; do not add a type prefix or emoji unless recent commits use one.\n")
	case StyleConventional:
		b.WriteString("- Use Conventional Commits: start the subject with a lowercase type and optional scope, as in fix(api): ... or feat: ... The types are fix, feat, build, chore, ci, docs, style, refactor, perf and test.\n")
	case StyleGitmoji:
		b.WriteString("- Start the subject with one Gitmoji emoji that fits the change, then a space, as in 🐛 fix the crash.\n")
	case StylePlain:
		b.WriteString("- Write the subject without a type prefix or emoji.\n")
	}
	b.WriteString("- Do not say that a tool or AI wrote the message.\n")
	if instructions := strings.TrimSpace(cfg.Instructions); instructions != "" {
		b.WriteString("\nAdditional instructions:\n")
		b.WriteString(instructions)
		b.WriteByte('\n')
	}
	return b.String()
}

func languageName(code string) string {
	if strings.HasPrefix(strings.ToLower(code), "zh") {
		return "Simplified Chinese"
	}
	return "English"
}

// Clean keeps just the commit message from what a provider wrote: the last
// code fence if it used one, without a line introducing it or quotes
// around it.
func Clean(text string) string {
	text = strings.TrimSpace(text)
	// A provider that wraps the message in a fence, perhaps after saying
	// what it did, has the message in the last fence.
	if end := strings.LastIndex(text, "```"); end >= 0 {
		if start := strings.LastIndex(text[:end], "```"); start >= 0 {
			block := text[start+3 : end]
			// The fence's language, on its first line.
			if first, rest, ok := strings.Cut(block, "\n"); ok {
				switch strings.ToLower(strings.TrimSpace(first)) {
				case "", "text", "txt", "markdown", "md", "git", "diff", "commit":
					block = rest
				}
			}
			text = strings.TrimSpace(block)
		}
	}
	// "Here is the commit message:" and the like.
	for {
		line, rest, ok := strings.Cut(text, "\n")
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if !ok || !strings.HasSuffix(lower, ":") ||
			!(strings.Contains(lower, "commit message") || strings.Contains(lower, "here is")) {
			break
		}
		text = strings.TrimSpace(rest)
	}
	// Single quotes around the whole message.
	if len(text) >= 2 {
		first, last := text[0], text[len(text)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			inner := text[1 : len(text)-1]
			if !strings.ContainsRune(inner, rune(first)) {
				text = strings.TrimSpace(inner)
			}
		}
	}
	return text
}

// firstLine is the first non-empty line of text.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// lastPath is the last element of a slash- or backslash-separated path.
func lastPath(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), `/\`)
	if ix := strings.LastIndexAny(path, `/\`); ix >= 0 {
		return path[ix+1:]
	}
	return path
}
