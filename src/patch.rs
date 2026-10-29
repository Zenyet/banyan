//! Splitting a single-file unified diff into hunks and putting a subset of
//! its changed lines back together, for committing only some of a file's
//! changes.

/// A changed line: its hunk's header and its index into the hunk's `lines`.
pub type ChangedLine = (String, usize);

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

    /// Indices into `lines` of the added and removed lines.
    pub fn changes(&self) -> impl Iterator<Item = usize> + '_ {
        self.lines
            .iter()
            .enumerate()
            .filter(|(_, line)| is_change(line))
            .map(|(ix, _)| ix)
    }

    /// Line in the new file at `lines[ix]`; for a removed line, the line
    /// that follows it.
    pub fn new_line_at(&self, ix: usize) -> usize {
        let before = self.lines[..ix]
            .iter()
            .filter(|line| line.starts_with(' ') || line.starts_with('+'))
            .count();
        self.new_start() + before
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

fn is_change(line: &str) -> bool {
    line.starts_with('+') || line.starts_with('-')
}

/// A patch with only the changed lines `keep` accepts, given a hunk and an
/// index into its `lines`, or `None` if none remain. A left-out removal
/// stays as context; a left-out addition is dropped.
///
/// Line counts in hunk headers are not adjusted; apply the result with
/// `git apply --recount`, which recomputes them.
pub fn select(file: &FilePatch, keep: impl Fn(&Hunk, usize) -> bool) -> Option<String> {
    let mut body = String::new();
    for hunk in &file.hunks {
        let mut lines = Vec::new();
        let mut kept_any = false;
        // Whether the previous line made it into the patch, for the
        // "\ No newline at end of file" marker that follows it.
        let mut previous_kept = true;
        for (ix, line) in hunk.lines.iter().enumerate() {
            if line.starts_with('\\') {
                if previous_kept {
                    lines.push(line.clone());
                }
                continue;
            }
            if !is_change(line) {
                lines.push(line.clone());
                previous_kept = true;
            } else if keep(hunk, ix) {
                lines.push(line.clone());
                kept_any = true;
                previous_kept = true;
            } else if let Some(text) = line.strip_prefix('-') {
                lines.push(format!(" {text}"));
                previous_kept = true;
            } else {
                previous_kept = false;
            }
        }
        if !kept_any {
            continue;
        }
        body.push_str(&hunk.header);
        body.push('\n');
        for line in lines {
            body.push_str(&line);
            body.push('\n');
        }
    }
    if body.is_empty() {
        return None;
    }
    let mut out = String::new();
    for line in &file.header {
        out.push_str(line);
        out.push('\n');
    }
    out.push_str(&body);
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
        let patch = select(&file, |h, _| h.header == second).unwrap();
        assert!(patch.starts_with("diff --git a/f.txt b/f.txt\n"));
        assert!(patch.contains("+nine and a half\n"));
        assert!(!patch.contains("+ONE"));
        assert!(!patch.contains("fn first()"));
        assert_eq!(select(&file, |_, _| false), None);
    }

    #[test]
    fn selects_single_lines() {
        let file = parse(PATCH);
        let first = &file.hunks[0];
        assert_eq!(first.changes().collect::<Vec<_>>(), vec![0, 1]);
        assert_eq!(first.new_line_at(1), 1);
        assert_eq!(file.hunks[1].new_line_at(2), 10);
        // Keep only the addition: the removal becomes context.
        let patch = select(&file, |h, ix| h.header == first.header && ix == 1).unwrap();
        assert!(patch.contains("@@ -1,3 +1,3 @@ fn first()\n one\n+ONE\n two\n"));
        assert!(!patch.contains("nine and a half"));
        // Keep only the removal: the addition is dropped.
        let patch = select(&file, |h, ix| h.header == first.header && ix == 0).unwrap();
        assert!(patch.contains("fn first()\n-one\n two\n"));
    }

    #[test]
    fn no_newline_marker_follows_its_line() {
        let file = parse("--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n");
        let patch = select(&file, |_, ix| ix == 0).unwrap();
        assert!(patch.ends_with("@@ -1 +1 @@\n-old\n\\ No newline at end of file\n"), "{patch}");
    }
}
