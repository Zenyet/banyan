//! The interface in English or Simplified Chinese. The code writes its
//! strings in English through `tr!`, which looks up their translation in
//! [`zh::TABLE`]. Placeholders are `{}` in order; a translation may write
//! `{0}`, `{1}` … to put them in another order. What git itself prints stays
//! in English, as in JetBrains IDEs.

use std::collections::HashMap;
use std::fmt::Display;
use std::sync::LazyLock;
use std::sync::atomic::{AtomicBool, Ordering};

mod zh;

/// `tr!("Commit")`, or with arguments `tr!("Push '{}'", name)`.
macro_rules! tr {
    ($text:literal) => {
        $crate::i18n::translate($text)
    };
    ($text:literal, $($arg:expr),+ $(,)?) => {
        $crate::i18n::translate_args($text, &[$(&$arg as &dyn std::fmt::Display),+])
    };
}
pub(crate) use tr;

static CHINESE: AtomicBool = AtomicBool::new(false);

static TABLE: LazyLock<HashMap<&'static str, &'static str>> = LazyLock::new(|| zh::TABLE.iter().copied().collect());

/// Use the language the settings ask for: `en`, `zh`, or the system's.
pub fn apply(setting: &str) {
    let chinese = match setting {
        "zh" => true,
        "en" => false,
        _ => system_prefers_chinese(),
    };
    CHINESE.store(chinese, Ordering::Relaxed);
}

pub fn is_chinese() -> bool {
    CHINESE.load(Ordering::Relaxed)
}

/// `text` in the interface language.
pub fn translate(text: &'static str) -> &'static str {
    if is_chinese() { TABLE.get(text).copied().unwrap_or(text) } else { text }
}

/// `text` translated, with its placeholders filled in.
pub fn translate_args(text: &'static str, args: &[&dyn Display]) -> String {
    fill(translate(text), args)
}

fn fill(template: &str, args: &[&dyn Display]) -> String {
    let mut out = String::new();
    let mut next = 0;
    let mut rest = template;
    while let Some(start) = rest.find('{') {
        out.push_str(&rest[..start]);
        let after = &rest[start + 1..];
        match after.find('}') {
            Some(end) if after[..end].chars().all(|c| c.is_ascii_digit()) => {
                let index = if end == 0 {
                    next += 1;
                    next - 1
                } else {
                    after[..end].parse().unwrap_or(usize::MAX)
                };
                if let Some(arg) = args.get(index) {
                    out.push_str(&arg.to_string());
                }
                rest = &after[end + 1..];
            }
            _ => {
                out.push('{');
                rest = after;
            }
        }
    }
    out.push_str(rest);
    out
}

/// Whether the user's preferred language is Chinese.
fn system_prefers_chinese() -> bool {
    let from_env = ["LC_ALL", "LC_MESSAGES", "LANG"]
        .iter()
        .filter_map(|key| std::env::var(key).ok())
        .find(|value| !value.is_empty() && value != "C" && value != "POSIX");
    if let Some(value) = from_env {
        return value.starts_with("zh");
    }
    // Apps opened from the Finder get no LANG; ask macOS.
    #[cfg(target_os = "macos")]
    if let Ok(output) = std::process::Command::new("defaults").args(["read", "-g", "AppleLanguages"]).output() {
        let text = String::from_utf8_lossy(&output.stdout);
        if let Some(first) = text.split(['(', ',', '\n']).map(|s| s.trim().trim_matches('"')).find(|s| !s.is_empty()) {
            return first.starts_with("zh");
        }
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fills_placeholders_in_order_or_by_number() {
        assert_eq!(fill("Merge '{}' into '{}'", &[&"a", &"b"]), "Merge 'a' into 'b'");
        assert_eq!(fill("将“{0}”合并到“{1}”", &[&"a", &"b"]), "将“a”合并到“b”");
        assert_eq!(fill("“{1}”变基到“{0}”", &[&"a", &"b"]), "“b”变基到“a”");
        assert_eq!(fill("{x} stays", &[&1]), "{x} stays");
    }

    /// Every `tr!` string in the sources has a Chinese translation with the
    /// same placeholders.
    #[test]
    fn every_string_is_translated() {
        let mut missing = Vec::new();
        for entry in std::fs::read_dir("src").unwrap() {
            let path = entry.unwrap().path();
            if path.extension().is_none_or(|e| e != "rs") {
                continue;
            }
            let source = std::fs::read_to_string(&path).unwrap();
            for (start, _) in source.match_indices("tr!(\"") {
                let rest = &source[start + 5..];
                let Some(end) = rest.find("\"") else { continue };
                let text = &rest[..end];
                match TABLE.get(text) {
                    None => missing.push(format!("{}: {text}", path.display())),
                    Some(zh) => {
                        let count = |s: &str| s.matches('{').count();
                        if count(zh) != count(text) {
                            missing.push(format!("placeholders differ: {text} → {zh}"));
                        }
                    }
                }
            }
        }
        assert!(missing.is_empty(), "untranslated:\n{}", missing.join("\n"));
    }
}
