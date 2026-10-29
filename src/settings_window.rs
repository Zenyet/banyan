//! The Settings window (⌘,): appearance, git and diff/log preferences.
//! Changes apply at once and are saved, as in macOS settings windows.

use std::cell::Cell;

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Theme, ThemeMode,
    setting::{SettingField, SettingGroup, SettingItem, SettingPage, Settings as SettingsView},
};
use gpui_kit::*;

use crate::git::git_version;
use crate::settings::{Settings, ThemeChoice};
use crate::i18n::tr;

thread_local! {
    static OPEN: Cell<Option<AnyWindowHandle>> = const { Cell::new(None) };
}

/// Show the Settings window, bringing it forward when already open.
pub fn open(cx: &mut App) {
    if let Some(window) = OPEN.get()
        && window.update(cx, |_, window, _| window.activate_window()).is_ok()
    {
        return;
    }
    let options = WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(Bounds::centered(None, size(px(780.), px(560.)), cx))),
        ..Default::default()
    };
    let window = gpui_kit::open_window(options, cx, |window, cx| {
        window.set_window_title(tr!("Settings"));
        cx.new(SettingsWindow::new)
    });
    if let Ok(window) = window {
        OPEN.set(Some(window.0));
    }
}

/// Theme and font size from the settings, for all windows.
pub fn apply_appearance(cx: &mut App) {
    let (theme, font_size) = Settings::read(|s| (s.theme, s.font_size));
    crate::i18n::apply(&Settings::read(|s| s.language.clone()));
    match theme {
        ThemeChoice::System => Theme::sync_system_appearance(None, cx),
        ThemeChoice::Light => Theme::change(ThemeMode::Light, None, cx),
        ThemeChoice::Dark => Theme::change(ThemeMode::Dark, None, cx),
    }
    // Changing the mode reloads the theme, so set the sizes after it.
    let font_size = font_size as f32;
    Theme::update(cx, |theme| {
        theme.font_size = px(font_size);
        theme.mono_font_size = px((font_size * 13. / 16.).round());
    });
}

/// Save a change, then repaint every window with it.
pub fn change(cx: &mut App, f: impl FnOnce(&mut Settings)) {
    Settings::update(f);
    apply_appearance(cx);
    // Views cache their content; a refresh repaints them all.
    cx.refresh_windows();
}

struct SettingsWindow {
    /// The git program last checked and what `--version` said.
    git_check: Option<(String, Result<String, String>)>,
    _check: Option<Task<()>>,
}

impl SettingsWindow {
    fn new(_: &mut Context<Self>) -> Self {
        Self { git_check: None, _check: None }
    }

    /// Check the configured git once per distinct path.
    fn check_git(&mut self, cx: &mut Context<Self>) {
        let program = Settings::read(|s| s.git_program().to_string());
        if self.git_check.as_ref().is_some_and(|(checked, _)| *checked == program) {
            return;
        }
        self.git_check = Some((program.clone(), Ok(tr!("Checking…").into())));
        self._check = Some(cx.spawn(async move |this, cx| {
            let checked = program.clone();
            let result = cx
                .background_executor()
                .spawn(async move { git_version(&checked).map_err(|err| format!("{err:#}")) })
                .await;
            this.update(cx, |this, cx| {
                this.git_check = Some((program, result));
                cx.notify();
            })
            .ok();
        }));
    }

    fn appearance_page() -> SettingPage {
        SettingPage::new(tr!("Appearance")).icon(IconName::Palette).groups([
            SettingGroup::new().title(tr!("Theme")).items([
                SettingItem::new(
                    tr!("Theme"),
                    choice(
                        ThemeChoice::ALL.iter().map(|t| (t.key().to_string(), t.label().to_string())),
                        |s| s.theme.key().to_string(),
                        |s, value| {
                            if let Some(theme) = ThemeChoice::ALL.into_iter().find(|t| t.key() == value) {
                                s.theme = theme;
                            }
                        },
                    ),
                )
                .description(tr!("Sync with OS follows the macOS appearance.")),
                SettingItem::new(
                    tr!("Font size"),
                    choice(
                        (12..=20).map(|size| (size.to_string(), format!("{size} px"))),
                        |s| s.font_size.to_string(),
                        |s, value| s.font_size = value.parse().unwrap_or(s.font_size),
                    ),
                )
                .description(tr!("Size of the interface text; diffs scale with it.")),
                SettingItem::new(
                    tr!("Language"),
                    choice(
                        [("system", tr!("Same as the system")), ("en", "English"), ("zh", "简体中文")]
                            .map(|(key, label)| (key.to_string(), label.to_string())),
                        |s| s.language.clone(),
                        |s, value| s.language = value.to_string(),
                    ),
                )
                .description(tr!("Menus in the menu bar change after a restart.")),
            ]),
            SettingGroup::new().title(tr!("Log")).item(
                SettingItem::new(
                    tr!("Commit dates"),
                    choice(
                        [("relative", tr!("Relative (3 minutes ago)")), ("absolute", tr!("Date and time"))]
                            .map(|(key, label)| (key.to_string(), label.to_string())),
                        |s| if s.relative_dates { "relative" } else { "absolute" }.to_string(),
                        |s, value| s.relative_dates = value == "relative",
                    ),
                ),
            ),
        ])
    }

    fn git_page(&self) -> SettingPage {
        let version: SharedString = match &self.git_check {
            Some((_, Ok(version))) => version.clone().into(),
            Some((_, Err(err))) => tr!("Not usable: {}", err).into(),
            None => "".into(),
        };
        SettingPage::new(tr!("Version Control")).icon(IconName::GitBranch).groups([
            SettingGroup::new().title(tr!("Git")).items([
                SettingItem::new(
                    tr!("Path to Git executable"),
                    SettingField::input(
                        |_| Settings::read(|s| s.git_path.clone()).into(),
                        |value: SharedString, cx| {
                            Settings::update(|s| s.git_path = value.trim().to_string());
                            cx.refresh_windows();
                        },
                    ),
                )
                .description(tr!("Leave empty to use git from the PATH. {}", version))
                .layout(Axis::Vertical),
            ]),
            SettingGroup::new().title(tr!("Update and Fetch")).items([
                SettingItem::new(
                    tr!("Update Project"),
                    choice(
                        [("merge", tr!("Merge incoming changes")), ("rebase", tr!("Rebase onto incoming changes"))]
                            .map(|(key, label)| (key.to_string(), label.to_string())),
                        |s| if s.update_rebase { "rebase" } else { "merge" }.to_string(),
                        |s, value| s.update_rebase = value == "rebase",
                    ),
                )
                .description(tr!("The default in the Update Project dialog.")),
                SettingItem::new(
                    tr!("Fetch in the background"),
                    choice(
                        [0, 5, 10, 15, 30, 60].map(|minutes| {
                            let label = match minutes {
                                0 => tr!("Never").to_string(),
                                60 => tr!("Every hour").to_string(),
                                m => tr!("Every {} minutes", m),
                            };
                            (minutes.to_string(), label)
                        }),
                        |s| s.auto_fetch_minutes.to_string(),
                        |s, value| s.auto_fetch_minutes = value.parse().unwrap_or(0),
                    ),
                )
                .description(tr!("Keeps remote branches current. Never asks for credentials; stored ones are used.")),
            ]),
        ])
    }

    fn diff_log_page() -> SettingPage {
        SettingPage::new(tr!("Diff and Log")).icon(IconName::GitCompare).groups([
            SettingGroup::new().title(tr!("Diff")).items([
                SettingItem::new(
                    tr!("Show diffs"),
                    choice(
                        [("split", tr!("Side by side")), ("unified", tr!("Unified"))]
                            .map(|(key, label)| (key.to_string(), label.to_string())),
                        |s| if s.unified_diff { "unified" } else { "split" }.to_string(),
                        |s, value| s.unified_diff = value == "unified",
                    ),
                ),
                SettingItem::new(
                    tr!("Whitespace changes"),
                    choice(
                        [("show", tr!("Show")), ("ignore", tr!("Ignore"))].map(|(key, label)| (key.to_string(), label.to_string())),
                        |s| if s.ignore_whitespace { "ignore" } else { "show" }.to_string(),
                        |s, value| s.ignore_whitespace = value == "ignore",
                    ),
                )
                .description(tr!("Also the Ignore Whitespace button above every diff.")),
            ]),
            SettingGroup::new().title(tr!("Log")).item(
                SettingItem::new(
                    tr!("Commits loaded at a time"),
                    choice(
                        [1000, 2000, 5000, 10000, 20000].map(|n| (n.to_string(), n.to_string())),
                        |s| s.log_page_size.to_string(),
                        |s, value| s.log_page_size = value.parse().unwrap_or(s.log_page_size),
                    ),
                )
                .description(tr!("More are loaded when you scroll to the end. Applies from the next refresh.")),
            ),
        ])
    }
}

/// A dropdown over `options` (key, label) bound to one setting.
fn choice(
    options: impl IntoIterator<Item = (String, String)>,
    get: fn(&Settings) -> String,
    set: fn(&mut Settings, &str),
) -> SettingField<SharedString> {
    SettingField::dropdown(
        options.into_iter().map(|(key, label)| (key.into(), label.into())).collect(),
        move |_| Settings::read(get).into(),
        move |value: SharedString, cx| change(cx, |s| set(s, &value)),
    )
}

impl Render for SettingsWindow {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        self.check_git(cx);
        div().size_full().bg(cx.theme().background).text_color(cx.theme().foreground).child(
            SettingsView::new("settings").sidebar_width(px(200.)).pages([
                Self::appearance_page(),
                self.git_page(),
                Self::diff_log_page(),
            ]),
        )
    }
}
