//! Splitting a single-file unified diff into hunks and putting a subset back
//! together, for committing only some of a file's changes.

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Hunk {
    /// The `@@ … @@` line, which identifies the hunk across refreshes.
    pub header: String,
    pub lines: Vec<String>,
}

impl Hunk {
    pub fn additions(&self) -> usize {
        self.lines.iter().filter(|l| l.starts_with('+')).count()
    }

    pub fn deletions(&self) -> usize {
        self.lines.iter().filter(|l| l.starts_with('-')).count()
    }

    /// The text after the second `@@`, usually the enclosing function.
    pub fn context(&self) -> &str {
        self.header
            .splitn(3, "@@")
            .nth(2)
            .map(str::trim)
            .unwrap_or_default()
    }

    /// Line of the first change in the new file, skipping leading context.
    pub fn first_changed_line(&self) -> usize {
        let leading_context = self
            .lines
            .iter()
            .take_while(|line| line.starts_with(' '))
            .count();
        self.new_start() + leading_context
    }

    /// First line of the hunk in the new file.
    pub fn new_start(&self) -> usize {
        self.header
            .split_whitespace()
            .find_map(|part| part.strip_prefix('+'))
            .and_then(|range| range.split(',').next())
            .and_then(|start| start.parse().ok())
            .unwrap_or(1)
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct FilePatch {
    /// `diff --git`, `index`, `---` and `+++` lines.
    pub header: Vec<String>,
    pub hunks: Vec<Hunk>,
}

pub fn parse(patch: &str) -> FilePatch {
    let mut file = FilePatch::default();
    for line in patch.lines() {
        if line.starts_with("@@") {
            file.hunks.push(Hunk {
                header: line.to_string(),
                lines: Vec::new(),
            });
        } else if let Some(hunk) = file.hunks.last_mut() {
            hunk.lines.push(line.to_string());
        } else {
            file.header.push(line.to_string());
        }
    }
    file
}

/// A patch with only the hunks `keep` accepts, or `None` if none remain.
///
/// Line numbers in later hunks may be off once earlier hunks are left out;
/// apply the result with `git apply --recount`, which tolerates that.
pub fn select(file: &FilePatch, keep: impl Fn(&Hunk) -> bool) -> Option<String> {
    let hunks: Vec<&Hunk> = file.hunks.iter().filter(|h| keep(h)).collect();
    if hunks.is_empty() {
        return None;
    }
    let mut out = String::new();
    for line in &file.header {
        out.push_str(line);
        out.push('\n');
    }
    for hunk in hunks {
        out.push_str(&hunk.header);
        out.push('\n');
        for line in &hunk.lines {
            out.push_str(line);
            out.push('\n');
        }
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    const PATCH: &str = "diff --git a/f.txt b/f.txt
index 1111111..2222222 100644
--- a/f.txt
+++ b/f.txt
@@ -1,3 +1,3 @@ fn first()
-one
+ONE
 two
 three
@@ -8,3 +8,4 @@ fn second()
 eight
 nine
+nine and a half
 ten
";

    #[test]
    fn parses_hunks() {
        let file = parse(PATCH);
        assert_eq!(file.header.len(), 4);
        assert_eq!(file.hunks.len(), 2);
        assert_eq!(file.hunks[0].context(), "fn first()");
        assert_eq!((file.hunks[0].additions(), file.hunks[0].deletions()), (1, 1));
        assert_eq!(file.hunks[1].new_start(), 8);
        assert_eq!(file.hunks[0].first_changed_line(), 1);
        assert_eq!(file.hunks[1].first_changed_line(), 10);
        assert_eq!((file.hunks[1].additions(), file.hunks[1].deletions()), (1, 0));
    }

    #[test]
    fn selects_a_subset() {
        let file = parse(PATCH);
        let second = file.hunks[1].header.clone();
        let patch = select(&file, |h| h.header == second).unwrap();
        assert!(patch.starts_with("diff --git a/f.txt b/f.txt\n"));
        assert!(patch.contains("+nine and a half\n"));
        assert!(!patch.contains("+ONE"));
        assert_eq!(select(&file, |_| false), None);
    }
}
