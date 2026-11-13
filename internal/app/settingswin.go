package app

import (
	"context"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/ai"
	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/settings"
)

// settingsShell is the Settings window (⌘,), while it is open: appearance,
// git, diff/log, AI and shortcut preferences. Changes apply at once and are
// saved, as in macOS settings windows.
var settingsShell *Shell

// The pages of the Settings window.
const (
	settingsPageAppearance = iota
	settingsPageVersionControl
	settingsPageDiff
	settingsPageAI
)

// settingsWin is the content of settingsShell, to open it on a page.
var settingsWin *settingsWindow

type settingsWindow struct {
	page int
	// gitChecked is the git program last checked and what --version said.
	gitChecked, gitVersion string
	gitPath, protected     string
	shortcuts              map[string]string
	// identity is the global git identity as last read, for the git
	// identityOf; name and email are the fields editing it, and fallback
	// who git makes commits by while none is set.
	identity            git.Identity
	identityOf          string
	name, email         string
	fallback, saveError string
	saving              bool
	// ai fields are the AI settings being edited; aiTools is whether an
	// ACP agent may use its tools.
	aiCommand, aiBaseURL, aiModel, aiKey, aiInstructions string
	aiTools                                              bool
	// The connection test and what it said.
	aiTesting               bool
	aiTestSeq               int
	aiTestCfg               ai.Config
	aiTestDetail, aiTestErr string
	aiTestCancel            context.CancelFunc
}

func openSettings() { openSettingsAt(settingsPageAppearance) }

// openSettingsAt shows the Settings window on a page, opening it if needed.
func openSettingsAt(page int) {
	if settingsShell != nil {
		settingsWin.page = page
		settingsShell.Win.Show()
		settingsShell.Win.Focus()
		settingsShell.Win.Invalidate()
		return
	}
	s := settings.Get()
	sw := &settingsWindow{
		page:           page,
		gitPath:        s.GitPath,
		protected:      s.ProtectedBranches,
		shortcuts:      map[string]string{},
		aiCommand:      s.AICommand,
		aiBaseURL:      s.AIBaseURL,
		aiModel:        s.AIModel,
		aiKey:          s.AIAPIKey,
		aiInstructions: s.AIInstructions,
		aiTools:        s.AIAllowTools,
	}
	primary := "ctrl-"
	if runtime.GOOS == "darwin" {
		primary = "cmd-"
	}
	for _, cmd := range commands() {
		// In the field, ⌘ or Ctrl by name rather than secondary.
		sw.shortcuts[cmd.ID] = strings.ReplaceAll(cmd.Keys(), "secondary-", primary)
	}
	settingsWin = sw
	settingsShell = newWindow(withSidebar(mygo.WindowOptions{Title: i18n.T("Settings"), Width: 820, Height: 600}),
		func(_ *Shell, c *ui.Context) { sw.view(c) })
	settingsShell.onClosed = func() {
		if settingsWin != nil {
			settingsWin.cancelAITest()
		}
		settingsShell, settingsWin = nil, nil
	}
}

type option struct{ key, label string }

// choice is a drop-down over options bound to one setting: a capsule
// showing the option chosen, which opens the system's menu of them.
func choice(c *ui.Context, options []option, current string, set func(*settings.Settings, string)) {
	t := c.Theme()
	shown := ""
	for _, o := range options {
		if o.key == current {
			shown = o.label
		}
	}
	b := button(c.Key("choice"), "").Label(shown).MinWidth(140).PaddingX(12).Gap(8)
	b.Children(func() {
		ui.Text(c, shown).SingleLine().Grow(1)
		ui.Icon(c, icon("chevron-down")).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
	})
	b.Menu(func(m *ui.Menu) {
		for _, o := range options {
			if m.Item(o.label).Radio(o.key == current).Chosen() {
				key := o.key
				changeSettings(func(s *settings.Settings) { set(s, key) })
			}
		}
	})
}

// firstSetting is whether the next settingRow is the first of its group,
// which draws no separator above it.
var firstSetting bool

// settingRow is a setting: its name and description on the left, its
// control on the right, or below them when vertical.
func settingRow(c *ui.Context, key, title, description string, vertical bool, control func()) {
	t := c.Theme()
	var row ui.Element
	if vertical {
		row = ui.Column(c.Key(key)).Gap(6).PaddingY(10)
	} else {
		row = ui.Row(c.Key(key)).Gap(16).PaddingY(10)
	}
	if !firstSetting {
		row.BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
	}
	firstSetting = false
	row.Children(func() {
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, title)
			if description != "" {
				muted(c, description)
			}
		})
		control()
	})
}

// settingsGroup is a titled group of settings, which rows builds, on a
// rounded card, as macOS's System Settings groups them.
func settingsGroup(c *ui.Context, title, description string, rows func()) {
	t := c.Theme()
	ui.Text(c, title).FontWeight(600).Margin(18, 4, 6, 4)
	if description != "" {
		muted(c, description).Margin(0, 4, 8, 4)
	}
	firstSetting = true
	ui.Column(c.Key(title)).PaddingX(14).Radius(12).Background(t.Surface).Border(1, t.Border.Alpha(0.6)).Children(rows)
}

func (sw *settingsWindow) view(c *ui.Context) {
	t := c.Theme()
	s := settings.Get()
	pages := []struct{ icon, title string }{
		{"settings", i18n.T("Appearance")},
		{"git-branch", i18n.T("Version Control")},
		{"git-compare", i18n.T("Diff and Log")},
		{"sparkles", i18n.T("AI")},
		{"keyboard", i18n.T("Keyboard Shortcuts")},
	}
	ui.Row(c).Fill().Children(func() {
		sidebar(c, 210, func() {
			ui.Column(c).Padding(0, 10, 10, 10).Gap(2).Children(func() {
				for ix, p := range pages {
					item := ui.Row(c.Key(ix)).Height(30).PaddingX(10).Gap(8).Radius(8).Cursor(ui.CursorPointer)
					switch {
					case ix == sw.page:
						item.Background(t.Accent).TextColor(t.AccentText)
					case item.Hovered():
						item.Background(t.Text.Alpha(0.06))
					}
					if item.Clicked() {
						sw.page = ix
					}
					item.Children(func() {
						ui.Icon(c, icon(p.icon)).FontSize(t.FontSize + 1)
						ui.Text(c, p.title).SingleLine()
					})
				}
			})
		})
		ui.Column(c.Key("pane")).Grow(1).MinWidth(0).FillHeight().Background(t.Background).Children(func() {
			paneTitle(c, pages[sw.page].title)
			ui.Scroll(c.Key(sw.page)).Grow(1).Padding(0, 24, 24, 24).Children(func() {
				switch sw.page {
				case settingsPageAppearance:
					sw.appearance(c, &s)
				case settingsPageVersionControl:
					sw.versionControl(c, &s)
				case settingsPageDiff:
					sw.diffAndLog(c, &s)
				case settingsPageAI:
					sw.ai(c, &s)
				default:
					sw.keymap(c)
				}
			})
		})
	})
}

func (sw *settingsWindow) appearance(c *ui.Context, s *settings.Settings) {
	settingsGroup(c, i18n.T("Theme"), "", func() {
		settingRow(c, "theme", i18n.T("Theme"), i18n.T("Sync with OS follows the macOS appearance."), false, func() {
			var options []option
			for _, th := range settings.AllThemes {
				options = append(options, option{th.Key(), th.Label()})
			}
			choice(c, options, s.Theme.Key(), func(s *settings.Settings, v string) {
				for _, th := range settings.AllThemes {
					if th.Key() == v {
						s.Theme = th
					}
				}
			})
		})
		settingRow(c, "font", i18n.T("Font size"), i18n.T("Size of the interface text; diffs scale with it."), false, func() {
			var options []option
			for size := 11; size <= 18; size++ {
				options = append(options, option{strconv.Itoa(size), strconv.Itoa(size) + " px"})
			}
			choice(c, options, strconv.Itoa(s.FontSize), func(s *settings.Settings, v string) { s.FontSize, _ = strconv.Atoi(v) })
		})
		settingRow(c, "language", i18n.T("Language"), "", false, func() {
			// Until one is chosen, the system's language is the one in use.
			current := s.Language
			if current != "en" && current != "zh" {
				current = "en"
				if i18n.IsChinese() {
					current = "zh"
				}
			}
			choice(c, []option{{"en", "English"}, {"zh", "简体中文"}}, current, func(s *settings.Settings, v string) { s.Language = v })
		})
	})
	settingsGroup(c, i18n.T("Log"), "", func() {
		settingRow(c, "dates", i18n.T("Commit dates"), "", false, func() {
			current := "absolute"
			if s.RelativeDates {
				current = "relative"
			}
			choice(c, []option{{"relative", i18n.T("Relative (3 minutes ago)")}, {"absolute", i18n.T("Date and time")}}, current,
				func(s *settings.Settings, v string) { s.RelativeDates = v == "relative" })
		})
	})
}

func (sw *settingsWindow) versionControl(c *ui.Context, s *settings.Settings) {
	// Check the configured git once per distinct path.
	if program := s.GitProgram(); program != sw.gitChecked {
		sw.gitChecked, sw.gitVersion = program, i18n.T("Checking…")
		go func() {
			version, err := git.GitVersion(program)
			if err != nil {
				version = i18n.Tf("Not usable: {}", err.Error())
			}
			mygo.RunOnMain(func() {
				if sw.gitChecked == program {
					sw.gitVersion = version
				}
				settingsShell.Win.Invalidate()
			})
		}()
	}
	sw.identityGroup(c, s.GitProgram())
	settingsGroup(c, i18n.T("Git"), "", func() {
		settingRow(c, "open-in", i18n.T("Open repositories in"), "", false, func() {
			current := "tab"
			if s.OpenInNewWindow {
				current = "window"
			}
			choice(c, []option{{"tab", i18n.T("A tab of the current window")}, {"window", i18n.T("A new window")}}, current,
				func(s *settings.Settings, v string) { s.OpenInNewWindow = v == "window" })
		})
		settingRow(c, "git", i18n.T("Path to Git executable"), i18n.Tf("Leave empty to use git from the PATH. {}", sw.gitVersion), true, func() {
			if ui.TextInput(c.Key("git-path"), &sw.gitPath).FillWidth().Changed() {
				path := strings.TrimSpace(sw.gitPath)
				changeSettings(func(s *settings.Settings) { s.GitPath = path })
			}
		})
		settingRow(c, "protected", i18n.T("Protected branches"),
			i18n.T("Never force pushed, and their pushed commits are not rewritten. Separate names with spaces; * matches anything."), true, func() {
				if ui.TextInput(c.Key("protected"), &sw.protected).FillWidth().Changed() {
					p := strings.TrimSpace(sw.protected)
					changeSettings(func(s *settings.Settings) { s.ProtectedBranches = p })
				}
			})
	})
	settingsGroup(c, i18n.T("Update and Fetch"), "", func() {
		settingRow(c, "update", i18n.T("Update Project"), i18n.T("The default in the Update Project dialog."), false, func() {
			var options []option
			for _, m := range git.AllUpdateMethods {
				options = append(options, option{m.Key(), m.Label()})
			}
			choice(c, options, s.UpdateMethod.Key(), func(s *settings.Settings, v string) { s.UpdateMethod = git.UpdateMethodFromKey(v) })
		})
		settingRow(c, "fetch", i18n.T("Fetch in the background"), i18n.T("Keeps remote branches current. Never asks for credentials; stored ones are used."), false, func() {
			var options []option
			for _, minutes := range []int{0, 5, 10, 15, 30, 60} {
				label := i18n.Tf("Every {} minutes", minutes)
				switch minutes {
				case 0:
					label = i18n.T("Never")
				case 60:
					label = i18n.T("Every hour")
				}
				options = append(options, option{strconv.Itoa(minutes), label})
			}
			choice(c, options, strconv.Itoa(s.AutoFetchMinutes), func(s *settings.Settings, v string) { s.AutoFetchMinutes, _ = strconv.Atoi(v) })
		})
	})
}

// identityGroup shows and edits who commits are made by: user.name and
// user.email in the global git config, which program reads.
func (sw *settingsWindow) identityGroup(c *ui.Context, program string) {
	t := c.Theme()
	if program != sw.identityOf {
		sw.identityOf = program
		sw.loadIdentity()
	}
	edited := git.Identity{Name: strings.TrimSpace(sw.name), Email: strings.TrimSpace(sw.email)}
	valid := edited.Email == "" || strings.Contains(edited.Email, "@")
	settingsGroup(c, i18n.T("Identity"), i18n.T("Who your commits are by, in your global git config (~/.gitconfig). A repository can still set its own."), func() {
		settingRow(c, "name", i18n.T("Name"), "", false, func() {
			ui.TextInput(c.Key("name"), &sw.name).Placeholder(i18n.T("Your Name")).Width(260)
		})
		settingRow(c, "email", i18n.T("Email"), "", false, func() {
			ui.TextInput(c.Key("email"), &sw.email).Placeholder("you@example.com").Width(260)
		})
		ui.Row(c.Key("identity-save")).Gap(12).PaddingY(10).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			switch {
			case sw.saveError != "":
				ui.Text(c, sw.saveError).TextColor(t.Danger).Grow(1).Shrink(1)
			case !valid:
				ui.Text(c, i18n.T("An email address needs an @.")).TextColor(t.Danger).Grow(1).Shrink(1)
			case !sw.identity.IsSet() && sw.fallback != "":
				ui.Text(c, i18n.Tf("Not set: git makes your commits as {}, from your login and this computer's name.", sw.fallback)).
					TextColor(t.Warning).Grow(1).Shrink(1)
			case !sw.identity.IsSet():
				ui.Text(c, i18n.T("Not set: git refuses to commit until it is.")).TextColor(t.Warning).Grow(1).Shrink(1)
			default:
				muted(c, i18n.T("Commits are made as this.")).Grow(1).Shrink(1)
			}
			if primaryButton(c.Key("save"), i18n.T("Save")).Disabled(edited == sw.identity || !valid || sw.saving).Clicked() {
				sw.saveIdentity(edited)
			}
		})
	})
}

// loadIdentity reads the global identity, and who git falls back to,
// off the main thread.
func (sw *settingsWindow) loadIdentity() {
	program := sw.identityOf
	go func() {
		id, fallback := git.GlobalIdentity(), git.FallbackIdent()
		mygo.RunOnMain(func() {
			if sw.identityOf != program {
				return
			}
			sw.identity, sw.fallback = id, fallback
			sw.name, sw.email = id.Name, id.Email
			if settingsShell != nil {
				settingsShell.Win.Invalidate()
			}
		})
	}()
}

// saveIdentity writes id to the global git config, then reads it back.
func (sw *settingsWindow) saveIdentity(id git.Identity) {
	sw.saving, sw.saveError = true, ""
	go func() {
		err := git.SetGlobalIdentity(id)
		mygo.RunOnMain(func() {
			sw.saving = false
			if err != nil {
				sw.saveError = i18n.Tf("Could not save: {}", err.Error())
			}
			sw.loadIdentity()
		})
	}()
}

func (sw *settingsWindow) diffAndLog(c *ui.Context, s *settings.Settings) {
	settingsGroup(c, i18n.T("Diff"), "", func() {
		settingRow(c, "diff", i18n.T("Show diffs"), "", false, func() {
			current := "split"
			if s.UnifiedDiff {
				current = "unified"
			}
			choice(c, []option{{"split", i18n.T("Side by side")}, {"unified", i18n.T("Unified")}}, current,
				func(s *settings.Settings, v string) { s.UnifiedDiff = v == "unified" })
		})
		settingRow(c, "whitespace", i18n.T("Whitespace changes"), i18n.T("Also the Ignore Whitespace button above every diff."), false, func() {
			current := "show"
			if s.IgnoreWhitespace {
				current = "ignore"
			}
			choice(c, []option{{"show", i18n.T("Show")}, {"ignore", i18n.T("Ignore")}}, current,
				func(s *settings.Settings, v string) { s.IgnoreWhitespace = v == "ignore" })
		})
	})
	settingsGroup(c, i18n.T("Log"), "", func() {
		settingRow(c, "page", i18n.T("Commits loaded at a time"), i18n.T("More are loaded when you scroll to the end. Applies from the next refresh."), false, func() {
			var options []option
			for _, n := range []int{1000, 2000, 5000, 10000, 20000} {
				options = append(options, option{strconv.Itoa(n), strconv.Itoa(n)})
			}
			choice(c, options, strconv.Itoa(s.LogPageSize), func(s *settings.Settings, v string) { s.LogPageSize, _ = strconv.Atoi(v) })
		})
	})
}

// aiConfig is the AI settings as the generator takes them.
func (sw *settingsWindow) aiConfig(s *settings.Settings) ai.Config {
	return ai.Config{
		Provider:      ai.ProviderFromKey(s.AIProvider),
		Command:       strings.TrimSpace(sw.aiCommand),
		BaseURL:       strings.TrimSpace(sw.aiBaseURL),
		Model:         strings.TrimSpace(sw.aiModel),
		APIKey:        strings.TrimSpace(sw.aiKey),
		Instructions:  strings.TrimSpace(sw.aiInstructions),
		AllowTools:    sw.aiTools,
		ClientVersion: Version,
	}
}

// ai is Settings › AI: where commit messages come from, and how.
func (sw *settingsWindow) ai(c *ui.Context, s *settings.Settings) {
	t := c.Theme()
	provider := ai.ProviderFromKey(s.AIProvider)
	muted(c, i18n.Tf("The ✨ button above the commit message writes one from the checked changes; {} does too.", displayKeys("secondary-shift-g"))).
		Margin(18, 4, 8, 4)

	settingsGroup(c, i18n.T("Provider"), i18n.T("Commit messages can come from an agent you already use, or from a model's API."), func() {
		settingRow(c, "ai-provider", i18n.T("Generate with"), "", false, func() {
			var options []option
			for _, p := range ai.AllProviders {
				options = append(options, option{string(p), p.Label()})
			}
			choice(c, options, string(provider), func(s *settings.Settings, v string) { s.AIProvider = v })
		})
	})
	if provider == ai.ProviderOff {
		muted(c, i18n.T("Choose a provider to turn AI commit messages on.")).Margin(18, 10, 6, 6)
		return
	}
	if provider == ai.ProviderACP {
		settingsGroup(c, i18n.T("ACP agent"),
			i18n.T("Banyan runs this command in the repository and asks it over the Agent Client Protocol. The agent uses its own login and model, and never leaves the repository."),
			func() {
				settingRow(c, "ai-command", i18n.T("Agent command"), i18n.T("For example an ACP adapter for the agent you use. Use its full path if Banyan cannot find it."), true, func() {
					ui.Row(c).Gap(8).Children(func() {
						if ui.TextInput(c.Key("ai-command-input"), &sw.aiCommand).
							Placeholder("npx -y @agentclientprotocol/claude-agent-acp").Grow(1).Changed() {
							command := strings.TrimSpace(sw.aiCommand)
							changeSettings(func(s *settings.Settings) { s.AICommand = command })
						}
						button(c.Key("examples"), i18n.T("Examples")).Menu(func(m *ui.Menu) {
							for _, example := range []struct{ name, command string }{
								{"Claude Code", "npx -y @agentclientprotocol/claude-agent-acp"},
								{"Codex", "npx -y @zed-industries/codex-acp"},
								{"Gemini CLI", "gemini --experimental-acp"},
							} {
								if m.Item(example.name).Chosen() {
									sw.aiCommand = example.command
									changeSettings(func(s *settings.Settings) { s.AICommand = example.command })
								}
							}
						})
					})
				})
				settingRow(c, "ai-tools", i18n.T("Let the agent use its tools"),
					i18n.T("The agent may read files and run commands in the repository. Turn off to answer from the diff alone."), false, func() {
						tools := sw.aiTools
						if ui.Checkbox(c.Key("ai-tools-check"), &tools, i18n.T("Allow")).Changed() {
							sw.aiTools = tools
							changeSettings(func(s *settings.Settings) { s.AIAllowTools = tools })
						}
					})
			})
	} else {
		base, model := "https://api.openai.com/v1", "gpt-4.1-mini"
		switch provider {
		case ai.ProviderAnthropic:
			base, model = "https://api.anthropic.com", "claude-sonnet-4-5"
		case ai.ProviderCompatible:
			base, model = "http://localhost:11434/v1", "llama3.2"
		}
		settingsGroup(c, i18n.T("Model"), i18n.T("Banyan sends the diff of the checked changes and asks for a commit message."), func() {
			settingRow(c, "ai-base-url", i18n.T("Base URL"), i18n.Tf("Leave empty for {}.", base), true, func() {
				if ui.TextInput(c.Key("ai-base-url-input"), &sw.aiBaseURL).Placeholder(base).FillWidth().Changed() {
					value := strings.TrimSpace(sw.aiBaseURL)
					changeSettings(func(s *settings.Settings) { s.AIBaseURL = value })
				}
			})
			settingRow(c, "ai-model", i18n.T("Model"), "", true, func() {
				if ui.TextInput(c.Key("ai-model-input"), &sw.aiModel).Placeholder(model).FillWidth().Changed() {
					value := strings.TrimSpace(sw.aiModel)
					changeSettings(func(s *settings.Settings) { s.AIModel = value })
				}
			})
			description := i18n.T("Stored in Banyan's settings file, which only you can read.")
			if provider == ai.ProviderCompatible {
				description = i18n.T("Stored in Banyan's settings file. Leave empty for a server that needs no key.")
			}
			settingRow(c, "ai-key", i18n.T("API key"), description, true, func() {
				if ui.TextInput(c.Key("ai-key-input"), &sw.aiKey).Password().Placeholder("sk-…").FillWidth().Changed() {
					value := strings.TrimSpace(sw.aiKey)
					changeSettings(func(s *settings.Settings) { s.AIAPIKey = value })
				}
			})
		})
	}
	settingsGroup(c, i18n.T("Commit Message"), i18n.T("Most logs show only the first line; a body can explain why a change was made."), func() {
		settingRow(c, "ai-message", i18n.T("Length"), "", false, func() {
			current := "body"
			if s.AISubjectOnly {
				current = "one"
			}
			choice(c, []option{
				{"one", i18n.T("One line")},
				{"body", i18n.T("Subject and body")},
			}, current, func(s *settings.Settings, v string) { s.AISubjectOnly = v == "one" })
		})
		settingRow(c, "ai-style", i18n.T("Style"), i18n.T("The shape of the subject line."), false, func() {
			var options []option
			for _, style := range ai.AllStyles {
				options = append(options, option{string(style), style.Label()})
			}
			choice(c, options, s.AIStyle, func(s *settings.Settings, v string) { s.AIStyle = v })
		})
	})
	settingsGroup(c, i18n.T("Custom Prompt"), i18n.T("Optional words added to every request: a commit style, a tone, a language, or what to emphasize."), func() {
		settingRow(c, "ai-instructions", i18n.T("Custom instructions"), "", true, func() {
			if ui.TextArea(c.Key("ai-instructions-input"), &sw.aiInstructions).Lines(2, 6).
				Placeholder(i18n.T("e.g. Use Conventional Commits, mention the affected package")).FillWidth().Changed() {
				value := strings.TrimSpace(sw.aiInstructions)
				changeSettings(func(s *settings.Settings) { s.AIInstructions = value })
			}
		})
	})

	cfg := sw.aiConfig(s)
	problem := cfg.Validate()
	ui.Row(c.Key("ai-ready")).FillWidth().Padding(18, 6, 6, 6).Gap(8).Children(func() {
		if problem != nil {
			ui.Icon(c, icon("triangle-alert")).TextColor(t.Warning)
			ui.Text(c, i18n.Tf("Not ready: {}", problem.Error())).TextColor(t.Warning).Grow(1).Shrink(1)
		} else {
			ui.Icon(c, icon("circle-check")).TextColor(t.Success)
			ui.Text(c, i18n.T("Ready to write commit messages")).TextColor(t.Success).Grow(1).Shrink(1)
		}
		testing := sw.aiTesting && sw.aiTestCfg == cfg
		if testing {
			if button(c.Key("ai-test"), i18n.T("Cancel")).Clicked() {
				sw.cancelAITest()
			}
		} else if button(c.Key("ai-test"), i18n.T("Test Connection")).
			Tooltip(i18n.T("Sends a small request to the provider.")).
			Disabled(problem != nil).Clicked() {
			sw.testAI(cfg)
		}
	})
	switch {
	case sw.aiTesting && sw.aiTestCfg == cfg:
		ui.Row(c.Key("ai-test-status")).FillWidth().Padding(0, 18, 6, 6).Gap(8).Children(func() {
			ui.Spinner(c).Size(14, 14)
			muted(c, i18n.T("Testing the connection…")).SingleLine()
		})
	case sw.aiTestCfg == cfg && sw.aiTestErr != "":
		ui.Row(c.Key("ai-test-status")).FillWidth().Padding(0, 18, 6, 6).Gap(8).Children(func() {
			ui.Icon(c, icon("triangle-alert")).TextColor(t.Danger)
			ui.Text(c, sw.aiTestErr).TextColor(t.Danger).MaxLines(4).Grow(1).Shrink(1).Selectable()
		})
	case sw.aiTestCfg == cfg && sw.aiTestDetail != "":
		ui.Row(c.Key("ai-test-status")).FillWidth().Padding(0, 18, 6, 6).Gap(8).Children(func() {
			ui.Icon(c, icon("circle-check")).TextColor(t.Success)
			ui.Text(c, i18n.Tf("Connected: {}", sw.aiTestDetail)).TextColor(t.Success).Grow(1).Shrink(1)
		})
	}
}

// testAI checks the configured provider with a small request; the answer
// shows under the settings, next to the Test Connection button.
func (sw *settingsWindow) testAI(cfg ai.Config) {
	if sw.aiTestCancel != nil {
		sw.aiTestCancel()
	}
	sw.aiTestSeq++
	seq := sw.aiTestSeq
	ctx, cancel := context.WithCancel(context.Background())
	sw.aiTestCancel = cancel
	sw.aiTesting = true
	sw.aiTestCfg = cfg
	sw.aiTestDetail, sw.aiTestErr = "", ""
	go func() {
		detail, err := ai.Check(ctx, cfg)
		mygo.RunOnMain(func() {
			if seq != sw.aiTestSeq {
				return
			}
			sw.aiTesting = false
			sw.aiTestCancel = nil
			if err != nil {
				sw.aiTestErr = err.Error()
			} else {
				sw.aiTestDetail = detail
			}
			if settingsShell != nil {
				settingsShell.Win.Invalidate()
			}
		})
	}()
}

// cancelAITest stops a connection test, as closing the window does.
func (sw *settingsWindow) cancelAITest() {
	if sw.aiTestCancel != nil {
		sw.aiTestCancel()
	}
	sw.aiTestSeq++
	sw.aiTestCancel = nil
	sw.aiTesting = false
}

func (sw *settingsWindow) keymap(c *ui.Context) {
	t := c.Theme()
	settingsGroup(c, i18n.T("Commands"), i18n.T("Type keys such as cmd-shift-k (ctrl-shift-k on Windows and Linux), or leave empty for none."), func() {
		for _, cmd := range commands() {
			def := i18n.T("none")
			if cmd.DefaultKeys != "" {
				def = displayKeys(cmd.DefaultKeys)
			}
			settingRow(c, cmd.ID, cmd.Label, i18n.Tf("Default: {}", def), false, func() {
				value := sw.shortcuts[cmd.ID]
				if ui.TextInput(c.Key("keys"), &value).Width(200).Font(monoFont).Changed() {
					sw.shortcuts[cmd.ID] = value
					keys := strings.ToLower(strings.TrimSpace(value))
					// Applied as soon as it's typed, once it reads as keys.
					if keys != "" && accelerator(keys) == "" {
						return
					}
					isDefault := accelerator(keys) == accelerator(cmd.DefaultKeys)
					id := cmd.ID
					changeSettings(func(s *settings.Settings) {
						s.Shortcuts = slices.DeleteFunc(s.Shortcuts, func(sc settings.Shortcut) bool { return sc.ID == id })
						if !isDefault {
							s.Shortcuts = append(s.Shortcuts, settings.Shortcut{ID: id, Keys: keys})
						}
					})
				}
			})
		}
	})
	settingsGroup(c, i18n.T("Fixed"), "", func() {
		for _, fixed := range []struct {
			label string
			keys  []string
		}{
			{i18n.T("Commit from the message box"), []string{"secondary-enter"}},
			{i18n.T("From the search box to the commits"), []string{"enter"}},
			{i18n.T("Previous / next commit"), []string{"up", "down"}},
			{i18n.T("First / last commit"), []string{"home", "end"}},
			{i18n.T("Page up / down"), []string{"pageup", "pagedown"}},
			{i18n.T("Undo a choice in the merge tool"), []string{"secondary-z"}},
			{i18n.T("Redo a choice in the merge tool"), []string{"secondary-shift-z"}},
		} {
			var shown []string
			for _, k := range fixed.keys {
				shown = append(shown, displayKeys(k))
			}
			settingRow(c, fixed.label, fixed.label, "", false, func() {
				ui.Box(c).PaddingX(8).PaddingY(2).Radius(4).Border(1, t.Border).Background(t.Surface).Children(func() {
					ui.Text(c, strings.Join(shown, "  /  ")).Font(monoFont)
				})
			})
		}
	})
}
