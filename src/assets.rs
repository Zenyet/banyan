//! The app's assets: GPUI Kit's default icon bundle plus the few Lucide
//! icons used here beyond it (in `assets/icons`), rather than the whole
//! catalog of some 1,800 icons.

use std::borrow::Cow;

use gpui_kit::assets::Assets;
use gpui_kit::{AssetSource, Result, SharedString};

macro_rules! icon {
    ($file:literal) => {
        (concat!("icons/", $file), include_bytes!(concat!("../assets/icons/", $file)).as_slice())
    };
}

const EXTRA: &[(&str, &[u8])] = &[
    ("images/banyan.png", include_bytes!("../packaging/linux/banyan.png")),
    icon!("archive.svg"),
    icon!("arrow-down-to-line.svg"),
    icon!("arrow-up-from-line.svg"),
    icon!("chevrons-left.svg"),
    icon!("chevrons-right.svg"),
    icon!("clock.svg"),
    icon!("download.svg"),
    icon!("folder-git-2.svg"),
    icon!("git-branch.svg"),
    icon!("git-commit-horizontal.svg"),
    icon!("git-compare.svg"),
    icon!("git-graph.svg"),
    icon!("git-merge.svg"),
    icon!("pencil.svg"),
    icon!("refresh-ccw.svg"),
    icon!("tag.svg"),
    icon!("trash.svg"),
    icon!("x.svg"),
];

pub struct AppAssets;

impl AssetSource for AppAssets {
    fn load(&self, path: &str) -> Result<Option<Cow<'static, [u8]>>> {
        match EXTRA.iter().find(|(extra, _)| *extra == path) {
            Some((_, data)) => Ok(Some(Cow::Borrowed(data))),
            None => Assets.load(path),
        }
    }

    fn list(&self, path: &str) -> Result<Vec<SharedString>> {
        let mut list = Assets.list(path)?;
        list.extend(EXTRA.iter().filter(|(extra, _)| extra.starts_with(path)).map(|(extra, _)| (*extra).into()));
        Ok(list)
    }
}

#[cfg(test)]
mod tests {
    use super::AppAssets;
    use gpui_kit::AssetSource as _;

    /// Every `IconName::X` in the sources must be loadable, or it would draw
    /// as nothing; add a missing one to `assets/icons`.
    #[test]
    fn every_icon_used_is_bundled() {
        let mut missing = Vec::new();
        for entry in std::fs::read_dir("src").unwrap() {
            let source = std::fs::read_to_string(entry.unwrap().path()).unwrap();
            for (start, prefix) in source.match_indices("IconName::") {
                let rest = &source[start + prefix.len()..];
                let name: String = rest.chars().take_while(|c| c.is_ascii_alphanumeric()).collect();
                if name.is_empty() {
                    continue;
                }
                // FolderGit2 -> folder-git-2, as the Lucide file names go.
                let mut file = String::new();
                for (i, c) in name.chars().enumerate() {
                    let previous = name.chars().nth(i.wrapping_sub(1));
                    let starts_word = c.is_ascii_uppercase() || (c.is_ascii_digit() && !previous.is_some_and(|p| p.is_ascii_digit()));
                    if i > 0 && starts_word {
                        file.push('-');
                    }
                    file.push(c.to_ascii_lowercase());
                }
                let path = format!("icons/{file}.svg");
                if !matches!(AppAssets.load(&path), Ok(Some(_))) {
                    missing.push(path);
                }
            }
        }
        assert!(missing.is_empty(), "icons not bundled: {missing:?}");
    }
}
