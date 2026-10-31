//! "Commit Options": author, sign-off, signing and hooks for the next
//! commit, plus how this repository signs its commits, like the options in
//! JetBrains' commit window.

use gpui_kit::component::{
    ActiveTheme as _, StyledExt as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    checkbox::Checkbox,
    h_flex,
    input::{Input, InputState},
    v_flex,
};
use gpui_kit::*;

use crate::git::{CommitOptions, SigningConfig, is_valid_author};
use crate::i18n::tr;
use crate::model::{RepoEvent, RepoModel};

/// Edit `options`; `apply` receives them when the dialog is confirmed.
pub fn open(
    model: Entity<RepoModel>,
    options: CommitOptions,
    apply: impl Fn(CommitOptions, &mut App) + 'static,
    window: &mut Window,
    cx: &mut App,
) {
    let signing = model.read(cx).repo.signing();
    let view = cx.new(|cx| CommitOptionsDialog::new(model, options, signing, Box::new(apply), window, cx));
    window.open_dialog(cx, move |dialog, _, _| dialog.title(tr!("Commit Options")).w(px(520.)).child(view.clone()));
}

struct CommitOptionsDialog {
    model: Entity<RepoModel>,
    author: Entity<InputState>,
    sign_off: bool,
    sign: bool,
    skip_hooks: bool,
    signing: SigningConfig,
    always_sign: bool,
    key: Entity<InputState>,
    apply: Box<dyn Fn(CommitOptions, &mut App)>,
}

impl CommitOptionsDialog {
    fn new(
        model: Entity<RepoModel>,
        options: CommitOptions,
        signing: SigningConfig,
        apply: Box<dyn Fn(CommitOptions, &mut App)>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        let author = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder(tr!("Name <email>, or empty for yourself"))
                .default_value(options.author.clone().unwrap_or_default())
        });
        let key = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder(tr!("Default key"))
                .default_value(signing.key.clone().unwrap_or_default())
        });
        Self {
            model,
            author,
            sign_off: options.sign_off,
            sign: options.sign.unwrap_or(signing.always),
            skip_hooks: options.skip_hooks,
            always_sign: signing.always,
            signing,
            key,
            apply,
        }
    }

    fn confirm(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let author = self.author.read(cx).value().trim().to_string();
        if !author.is_empty() && !is_valid_author(&author) {
            self.model.update(cx, |_, cx| {
                cx.emit(RepoEvent::OpFailed(tr!("Write the author as Name <email>").into()))
            });
            return;
        }
        // Repository settings first, so the commit's own choice is judged against them.
        let key = self.key.read(cx).value().trim().to_string();
        let always = self.always_sign;
        let old_key = self.signing.key.clone().unwrap_or_default();
        if always != self.signing.always || key != old_key {
            self.model.update(cx, |m, cx| {
                m.run(
                    tr!("Change signing settings"),
                    move |repo| {
                        repo.set_always_sign(always)?;
                        repo.set_signing_key(&key)
                    },
                    cx,
                )
            });
        }
        let options = CommitOptions {
            author: (!author.is_empty()).then_some(author),
            sign_off: self.sign_off,
            sign: (self.sign != always).then_some(self.sign),
            skip_hooks: self.skip_hooks,
        };
        (self.apply)(options, cx);
        window.close_dialog(cx);
    }
}

impl Render for CommitOptionsDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let note = |text: String, cx: &App| div().pl(px(24.)).text_xs().text_color(cx.theme().muted_foreground).child(text);
        let format = match self.signing.format.as_str() {
            "ssh" => "SSH",
            "x509" => "X.509",
            _ => "GPG",
        };
        let key_note = match &self.signing.key {
            Some(key) => tr!("Signs with {} key {}", format, key),
            None => tr!("Signs with your default {} key", format),
        };
        v_flex()
            .gap_3()
            .text_sm()
            .child(div().font_semibold().child(tr!("This commit")))
            .child(
                v_flex()
                    .gap_1()
                    .child(div().child(tr!("Author")))
                    .child(Input::new(&self.author)),
            )
            .child(
                v_flex().gap_1().child(
                    Checkbox::new("sign-off")
                        .label(tr!("Sign-off"))
                        .checked(self.sign_off)
                        .on_click(cx.listener(|this, checked: &bool, _, cx| {
                            this.sign_off = *checked;
                            cx.notify();
                        })),
                )
                .child(note(tr!("Adds a Signed-off-by line with your name").into(), cx)),
            )
            .child(
                v_flex()
                    .gap_1()
                    .child(
                        Checkbox::new("sign")
                            .label(tr!("Sign the commit"))
                            .checked(self.sign)
                            .on_click(cx.listener(|this, checked: &bool, _, cx| {
                                this.sign = *checked;
                                cx.notify();
                            })),
                    )
                    .child(note(key_note, cx)),
            )
            .child(
                v_flex()
                    .gap_1()
                    .child(
                        Checkbox::new("skip-hooks")
                            .label(tr!("Skip Git hooks"))
                            .checked(self.skip_hooks)
                            .on_click(cx.listener(|this, checked: &bool, _, cx| {
                                this.skip_hooks = *checked;
                                cx.notify();
                            })),
                    )
                    .child(note(tr!("Does not run the pre-commit and commit-msg hooks").into(), cx)),
            )
            .child(div().h(px(1.)).bg(cx.theme().border))
            .child(div().font_semibold().child(tr!("This repository")))
            .child(
                Checkbox::new("always-sign")
                    .label(tr!("Sign every commit"))
                    .checked(self.always_sign)
                    .on_click(cx.listener(|this, checked: &bool, _, cx| {
                        this.always_sign = *checked;
                        this.sign = *checked;
                        cx.notify();
                    })),
            )
            .child(
                v_flex()
                    .gap_1()
                    .child(div().child(tr!("Signing key")))
                    .child(Input::new(&self.key))
                    .child(
                        div()
                            .text_xs()
                            .text_color(cx.theme().muted_foreground)
                            .child(tr!("A GPG key ID, or for SSH signing the path to a public key.")),
                    ),
            )
            .child(
                h_flex()
                    .justify_end()
                    .gap_2()
                    .child(Button::new("options-cancel").label(tr!("Cancel")).on_click(|_, window, cx| window.close_dialog(cx)))
                    .child(
                        Button::new("options-ok")
                            .label(tr!("OK"))
                            .primary()
                            .on_click(cx.listener(|this, _, window, cx| this.confirm(window, cx))),
                    ),
            )
    }
}
