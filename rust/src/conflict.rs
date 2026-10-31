//! A conflicted file as git's three-way merge leaves it: text that merged
//! cleanly, and conflicts whose yours, base and theirs versions the merge
//! tool chooses between.

use crate::git::MARKER_SIZE;

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Segment {
    /// Lines that merged cleanly, with their line endings.
    Clean(Vec<String>),
    Conflict(Conflict),
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Conflict {
    pub ours: Vec<String>,
    pub base: Vec<String>,
    pub theirs: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Resolution {
    Ours,
    Theirs,
    OursThenTheirs,
    TheirsThenOurs,
    /// Text typed in the merge tool.
    Custom(String),
}

impl Conflict {
    /// The lines that replace the conflict.
    pub fn resolved(&self, resolution: &Resolution) -> Vec<String> {
        match resolution {
            Resolution::Ours => self.ours.clone(),
            Resolution::Theirs => self.theirs.clone(),
            Resolution::OursThenTheirs => concat(&self.ours, &self.theirs),
            Resolution::TheirsThenOurs => concat(&self.theirs, &self.ours),
            Resolution::Custom(text) => {
                let mut lines = split_lines(text);
                terminate(&mut lines);
                lines
            }
        }
    }
}

/// Lines with their endings; a last line without one is kept as it is.
pub fn split_lines(text: &str) -> Vec<String> {
    text.split_inclusive('\n').map(str::to_string).collect()
}

/// A line's text without its ending, for display.
pub fn display(line: &str) -> &str {
    line.trim_end_matches(['\n', '\r'])
}

fn terminate(lines: &mut [String]) {
    if let Some(last) = lines.last_mut()
        && !last.ends_with('\n')
    {
        last.push('\n');
    }
}

fn concat(first: &[String], second: &[String]) -> Vec<String> {
    let mut lines = first.to_vec();
    if !second.is_empty() {
        terminate(&mut lines);
    }
    lines.extend_from_slice(second);
    lines
}

/// Split `git merge-file --diff3` output (markers of [`MARKER_SIZE`]) into segments.
pub fn parse(text: &str) -> Vec<Segment> {
    enum Part {
        Clean,
        Ours,
        Base,
        Theirs,
    }
    let marker = |c: char, line: &str| {
        let line = display(line);
        let marker = c.to_string().repeat(MARKER_SIZE);
        line == marker || line.strip_prefix(&marker).is_some_and(|rest| rest.starts_with(' '))
    };
    let mut segments = Vec::new();
    let mut clean = Vec::new();
    let mut conflict = Conflict::default();
    let mut part = Part::Clean;
    for line in split_lines(text) {
        match part {
            Part::Clean if marker('<', &line) => {
                if !clean.is_empty() {
                    segments.push(Segment::Clean(std::mem::take(&mut clean)));
                }
                part = Part::Ours;
            }
            Part::Clean => clean.push(line),
            Part::Ours | Part::Base if marker('=', &line) => part = Part::Theirs,
            Part::Ours if marker('|', &line) => part = Part::Base,
            Part::Ours => conflict.ours.push(line),
            Part::Base => conflict.base.push(line),
            Part::Theirs if marker('>', &line) => {
                segments.push(Segment::Conflict(std::mem::take(&mut conflict)));
                part = Part::Clean;
            }
            Part::Theirs => conflict.theirs.push(line),
        }
    }
    if !clean.is_empty() {
        segments.push(Segment::Clean(clean));
    }
    segments
}

/// The merged file with `resolutions[i]` in place of the i-th conflict,
/// or `None` while a conflict is unresolved.
pub fn result_text(segments: &[Segment], resolutions: &[Option<Resolution>]) -> Option<String> {
    let mut text = String::new();
    let mut conflicts = 0;
    for segment in segments {
        let lines = match segment {
            Segment::Clean(lines) => lines.clone(),
            Segment::Conflict(conflict) => {
                let resolution = resolutions.get(conflicts)?.as_ref()?;
                conflicts += 1;
                conflict.resolved(resolution)
            }
        };
        for line in lines {
            text.push_str(&line);
        }
    }
    Some(text)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn markers(c: char, label: &str) -> String {
        let marker = c.to_string().repeat(MARKER_SIZE);
        if label.is_empty() { format!("{marker}\n") } else { format!("{marker} {label}\n") }
    }

    fn sample() -> String {
        format!(
            "fn main() {{\n{}    a();\n{}    x();\n{}    b();\n{}}}\n",
            markers('<', "yours"),
            markers('|', "base"),
            markers('=', ""),
            markers('>', "theirs"),
        )
    }

    #[test]
    fn parses_clean_text_and_conflicts() {
        let segments = parse(&sample());
        assert_eq!(
            segments,
            vec![
                Segment::Clean(vec!["fn main() {\n".into()]),
                Segment::Conflict(Conflict {
                    ours: vec!["    a();\n".into()],
                    base: vec!["    x();\n".into()],
                    theirs: vec!["    b();\n".into()],
                }),
                Segment::Clean(vec!["}\n".into()]),
            ]
        );
    }

    #[test]
    fn git_sized_markers_are_content() {
        let text = "<<<<<<< HEAD\na\n=======\nb\n>>>>>>> other\n";
        assert_eq!(parse(text), vec![Segment::Clean(split_lines(text))]);
    }

    #[test]
    fn result_needs_every_conflict_resolved() {
        let segments = parse(&sample());
        assert_eq!(result_text(&segments, &[None]), None);
        assert_eq!(
            result_text(&segments, &[Some(Resolution::Theirs)]).unwrap(),
            "fn main() {\n    b();\n}\n"
        );
        assert_eq!(
            result_text(&segments, &[Some(Resolution::OursThenTheirs)]).unwrap(),
            "fn main() {\n    a();\n    b();\n}\n"
        );
        assert_eq!(
            result_text(&segments, &[Some(Resolution::Custom("    c();".into()))]).unwrap(),
            "fn main() {\n    c();\n}\n"
        );
    }

    #[test]
    fn both_sides_keep_a_line_break_between_them() {
        let conflict = Conflict {
            ours: vec!["a".into()],
            base: vec![],
            theirs: vec!["b".into()],
        };
        assert_eq!(conflict.resolved(&Resolution::OursThenTheirs), vec!["a\n", "b"]);
    }
}
