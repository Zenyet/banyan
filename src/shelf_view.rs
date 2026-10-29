//! The Shelf tab: changes put aside with Shelve, like JetBrains' Shelf.
//! Each shelf lists its files with their diffs and can be unshelved, all of
//! it or one file at a time, back into the changelist it came from.

use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Icon, Sizable as _, StyledExt as _,
    button::{Button, ButtonVariants as _},
    h_flex, h_resizable,
    menu::{ContextMenuExt as _, PopupMenuItem},
    resizable_panel, v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::diff_pane::DiffPane;
use crate::git::Shelf;
use crate::log_view::panel_header;
use crate::model::{RepoEvent, RepoModel};
use crate::ui::{confirm, format_time, prompt_text, selection_bg};
use crate::i18n::tr;

pub struct ShelfView {
    model: Entity<RepoModel>,
    /// The shelf selected, by id, and the file of it whose diff shows.
    selected: Option<String>,
    file: Option<String>,
    diff: Entity<DiffPane>,
    _subscription: Subscription,
}

impl ShelfView {
    pub fn new(model: Entity<RepoModel>, cx: &mut Context<Self>) -> Self {
        let diff = cx.new(DiffPane::new);
        let subscription = cx.subscribe(&model, |this, _, event, cx| {
            if let RepoEvent::Changed = event {
                this.sync(cx);
            }
        });
        let mut this = Self { model, selected: None, file: None, diff, _subscription: subscription };
        this.sync(cx);
        this
    }

    fn shelves<'a>(&self, cx: &'a App) -> &'a [Shelf] {
        &self.model.read(cx).shelves
    }

    /// Keep the selection on a shelf and file that still exist.
    fn sync(&mut self, cx: &mut Context<Self>) {
        let shelves = self.shelves(cx).to_vec();
        let shelf = self
            .selected
            .as_ref()
            .and_then(|id| shelves.iter().find(|s| &s.id == id))
            .or_else(|| shelves.first())
            .cloned();
        let file = shelf.as_ref().and_then(|shelf| {
            self.file
                .clone()
                .filter(|f| shelf.files.contains(f))
                .or_else(|| shelf.files.first().cloned())
        });
        let changed = shelf.as_ref().map(|s| &s.id) != self.selected.as_ref() || file != self.file;
        self.selected = shelf.map(|s| s.id);
        self.file = file;
        if changed || self.selected.is_none() {
            self.load_diff(cx);
        }
        cx.notify();
    }

    fn select(&mut self, id: String, file: Option<String>, cx: &mut Context<Self>) {
        self.selected = Some(id);
        self.file = file;
        self.sync(cx);
        self.load_diff(cx);
    }

    fn load_diff(&mut self, cx: &mut Context<Self>) {
        let (Some(id), Some(file)) = (self.selected.clone(), self.file.clone()) else {
            let message = if self.shelves(cx).is_empty() { tr!("Nothing shelved") } else { tr!("Select a file") };
            self.diff.update(cx, |diff, cx| diff.show_message(None, message, cx));
            return;
        };
        let repo = self.model.read(cx).repo.clone();
        self.diff
            .update(cx, |diff, cx| diff.load(file.clone(), move || repo.shelf_file_patch(&id, &file), cx));
    }

    fn render_shelf(&self, shelf: &Shelf, cx: &mut Context<Self>) -> AnyElement {
        let selected = self.selected.as_ref() == Some(&shelf.id);
        let id = shelf.id.clone();
        let files: Vec<AnyElement> = if selected {
            shelf
                .files
                .iter()
                .map(|file| {
                    let (id, path) = (id.clone(), file.clone());
                    let current = self.file.as_ref() == Some(file);
                    let (name, dir) = match file.rsplit_once('/') {
                        Some((dir, name)) => (name.to_string(), dir.to_string()),
                        None => (file.clone(), String::new()),
                    };
                    let model = self.model.clone();
                    h_flex()
                        .id(SharedString::from(format!("shelf-file-{id}-{file}")))
                        .h(px(24.))
                        .pl(px(28.))
                        .pr_2()
                        .gap_1()
                        .text_sm()
                        .when(current, |row| row.bg(selection_bg()))
                        .when(!current, |row| row.hover(|s| s.bg(cx.theme().list_hover)))
                        .child(Icon::new(IconName::File).xsmall().text_color(cx.theme().muted_foreground))
                        .child(div().flex_none().child(name))
                        .child(div().min_w_0().truncate().text_xs().text_color(cx.theme().muted_foreground).child(dir))
                        .on_click(cx.listener({
                            let (id, path) = (id.clone(), path.clone());
                            move |this, _, _, cx| this.select(id.clone(), Some(path.clone()), cx)
                        }))
                        .context_menu(move |menu, _, _| {
                            let (model, id, path) = (model.clone(), id.clone(), path.clone());
                            menu.item(PopupMenuItem::new(tr!("Unshelve This File")).on_click(move |_, _, cx| {
                                let (id, path) = (id.clone(), path.clone());
                                model.update(cx, |m, cx| m.unshelve(id, Some(vec![path]), cx))
                            }))
                        })
                        .into_any_element()
                })
                .collect()
        } else {
            Vec::new()
        };
        let model = self.model.clone();
        let name = shelf.name.clone();
        v_flex()
            .child(
                h_flex()
                    .id(SharedString::from(format!("shelf-{id}")))
                    .h(px(40.))
                    .px_2()
                    .gap_2()
                    .when(selected && self.file.is_none(), |row| row.bg(selection_bg()))
                    .hover(|s| s.bg(cx.theme().list_hover))
                    .child(
                        Icon::new(if selected { IconName::ChevronDown } else { IconName::ChevronRight }).xsmall(),
                    )
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .child(div().text_sm().font_semibold().truncate().child(shelf.name.clone()))
                            .child(
                                div()
                                    .text_xs()
                                    .truncate()
                                    .text_color(cx.theme().muted_foreground)
                                    .child(tr!(
                                        "{} · from {} · {}",
                                        crate::ui::file_count(shelf.files.len()),
                                        shelf.changelist,
                                        format_time(shelf.time)
                                    )),
                            ),
                    )
                    .on_click(cx.listener({
                        let id = id.clone();
                        move |this, _, _, cx| this.select(id.clone(), None, cx)
                    }))
                    .context_menu(move |menu, _, _| shelf_menu(menu, &model, id.clone(), name.clone())),
            )
            .children(files)
            .into_any_element()
    }
}

/// Unshelve, rename or delete a shelf.
fn shelf_menu(
    menu: gpui_kit::component::menu::PopupMenu,
    model: &Entity<RepoModel>,
    id: String,
    name: String,
) -> gpui_kit::component::menu::PopupMenu {
    let (unshelve_model, unshelve_id) = (model.clone(), id.clone());
    let (rename_model, rename_id, old) = (model.clone(), id.clone(), name.clone());
    let (delete_model, delete_id) = (model.clone(), id);
    menu.item(PopupMenuItem::new(tr!("Unshelve")).on_click(move |_, _, cx| {
        let id = unshelve_id.clone();
        unshelve_model.update(cx, |m, cx| m.unshelve(id, None, cx))
    }))
    .item(PopupMenuItem::new(tr!("Rename...")).on_click(move |_, window, cx| {
        let (model, id) = (rename_model.clone(), rename_id.clone());
        prompt_text(tr!("Rename Shelf"), tr!("Rename"), old.clone(), move |new, _, cx| {
            let id = id.clone();
            model.update(cx, |m, cx| m.run(tr!("Rename shelf"), move |repo| repo.rename_shelf(&id, &new), cx))
        }, window, cx);
    }))
    .separator()
    .item(PopupMenuItem::new(tr!("Delete...")).on_click(move |_, window, cx| {
        let (model, id) = (delete_model.clone(), delete_id.clone());
        confirm(
            tr!("Delete Shelf"),
            tr!("Delete the shelf '{}'? Its changes are lost.", name),
            tr!("Delete"),
            move |_, cx| {
                let id = id.clone();
                model.update(cx, |m, cx| m.run(tr!("Delete shelf"), move |repo| repo.delete_shelf(&id), cx))
            },
            window,
            cx,
        );
    }))
}

impl Render for ShelfView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let shelves = self.shelves(cx).to_vec();
        let rows: Vec<AnyElement> = shelves.iter().map(|shelf| self.render_shelf(shelf, cx)).collect();
        let selected = self.selected.clone();
        let list = if shelves.is_empty() {
            v_flex()
                .flex_1()
                .items_center()
                .justify_center()
                .gap_2()
                .p_4()
                .text_color(cx.theme().muted_foreground)
                .child(Icon::new(IconName::Inbox).large())
                .child(div().text_sm().child(tr!("Nothing shelved")))
                .child(
                    div()
                        .text_xs()
                        .text_center()
                        .child(tr!("Shelve changes from the Commit tab to put them aside and bring them back later.")),
                )
                .into_any_element()
        } else {
            div().id("shelves").flex_1().min_h_0().overflow_y_scroll().py_1().children(rows).into_any_element()
        };
        let toolbar = h_flex()
            .flex_none()
            .h(px(36.))
            .px_2()
            .gap_1()
            .border_b_1()
            .border_color(cx.theme().border)
            .child(
                Button::new("unshelve")
                    .label(tr!("Unshelve"))
                    .icon(IconName::Undo2)
                    .small()
                    .ghost()
                    .tooltip(tr!("Bring the selected shelf back into its changelist"))
                    .disabled(selected.is_none())
                    .on_click(cx.listener(|this, _, _, cx| {
                        if let Some(id) = this.selected.clone() {
                            this.model.update(cx, |m, cx| m.unshelve(id, None, cx))
                        }
                    })),
            );
        h_resizable("shelf")
            .child(
                resizable_panel().size(px(420.)).size_range(px(280.)..px(900.)).child(
                    v_flex()
                        .size_full()
                        .bg(cx.theme().sidebar)
                        .child(panel_header(tr!("Shelf"), cx))
                        .child(toolbar)
                        .child(list),
                ),
            )
            .child(resizable_panel().child(crate::ui::cached(self.diff.clone())))
    }
}
