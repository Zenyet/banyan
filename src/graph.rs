//! Lane layout for the commit graph.
//!
//! Each row is drawn independently: the top half connects the lanes coming
//! from the row above to this row's node (or straight through), the bottom
//! half connects the node to its parents' lanes and continues the others.

use crate::git::Commit;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Half {
    Top,
    Bottom,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Edge {
    pub from: usize,
    pub to: usize,
    pub half: Half,
    pub color: usize,
}

#[derive(Clone, Debug, Default)]
pub struct GraphRow {
    pub node: usize,
    pub color: usize,
    pub edges: Vec<Edge>,
    /// Number of lanes this row spans.
    pub width: usize,
}

struct Lane<'a> {
    expect: &'a str,
    color: usize,
}

pub fn layout(commits: &[Commit]) -> Vec<GraphRow> {
    let mut lanes: Vec<Option<Lane>> = Vec::new();
    let mut next_color = 0;
    let mut new_color = || {
        next_color += 1;
        next_color - 1
    };
    let mut rows = Vec::with_capacity(commits.len());

    for commit in commits {
        let matching: Vec<usize> = lanes
            .iter()
            .enumerate()
            .filter(|(_, lane)| lane.as_ref().is_some_and(|l| l.expect == commit.id))
            .map(|(ix, _)| ix)
            .collect();

        let (node, color) = match matching.first() {
            Some(&ix) => (ix, lanes[ix].as_ref().unwrap().color),
            None => {
                let slot = free_slot(&mut lanes);
                (slot, new_color())
            }
        };

        let mut edges = Vec::new();
        for (ix, lane) in lanes.iter().enumerate() {
            if let Some(lane) = lane {
                let to = if lane.expect == commit.id { node } else { ix };
                edges.push(Edge {
                    from: ix,
                    to,
                    half: Half::Top,
                    color: lane.color,
                });
            }
        }

        let before: Vec<Option<&str>> = lanes
            .iter()
            .map(|lane| lane.as_ref().map(|l| l.expect))
            .collect();
        for &ix in &matching {
            lanes[ix] = None;
        }
        if matching.is_empty() && node < lanes.len() {
            lanes[node] = None;
        }

        for (parent_ix, parent) in commit.parents.iter().enumerate() {
            let existing = lanes
                .iter()
                .position(|lane| lane.as_ref().is_some_and(|l| l.expect == parent));
            match existing {
                // Keep the first-parent line in the leftmost lane: the lane to
                // the right bends into this one instead. A merge only does so
                // when its other parents can go left of the node; otherwise
                // their new lane would cross the bend.
                Some(ix) if parent_ix == 0 && ix > node && can_bend(&lanes, node, &commit.parents) => {
                    let other = lanes[ix].take().unwrap();
                    ensure_len(&mut lanes, node + 1);
                    lanes[node] = Some(Lane {
                        expect: parent,
                        color,
                    });
                    edges.push(Edge {
                        from: node,
                        to: node,
                        half: Half::Bottom,
                        color,
                    });
                    edges.push(Edge {
                        from: ix,
                        to: node,
                        half: Half::Bottom,
                        color: other.color,
                    });
                }
                Some(ix) => edges.push(Edge {
                    from: node,
                    to: ix,
                    half: Half::Bottom,
                    color: lanes[ix].as_ref().unwrap().color,
                }),
                None => {
                    let (slot, color) = if parent_ix == 0 {
                        (node, color)
                    } else {
                        (free_slot(&mut lanes), new_color())
                    };
                    ensure_len(&mut lanes, slot + 1);
                    lanes[slot] = Some(Lane {
                        expect: parent,
                        color,
                    });
                    edges.push(Edge {
                        from: node,
                        to: slot,
                        half: Half::Bottom,
                        color,
                    });
                }
            }
        }

        for (ix, lane) in lanes.iter().enumerate() {
            let Some(lane) = lane else { continue };
            let continues = before
                .get(ix)
                .copied()
                .flatten()
                .is_some_and(|expect| expect == lane.expect && expect != commit.id);
            if continues {
                edges.push(Edge {
                    from: ix,
                    to: ix,
                    half: Half::Bottom,
                    color: lane.color,
                });
            }
        }

        while matches!(lanes.last(), Some(None)) {
            lanes.pop();
        }

        let width = edges
            .iter()
            .map(|edge| edge.from.max(edge.to))
            .chain([node])
            .max()
            .unwrap_or(0)
            + 1;
        rows.push(GraphRow {
            node,
            color,
            edges,
            width,
        });
    }
    rows
}

fn ensure_len(lanes: &mut Vec<Option<Lane>>, len: usize) {
    while lanes.len() < len {
        lanes.push(None);
    }
}

/// Whether the other parents of a merge at `node` fit without crossing a
/// lane that bends into the node from the right.
fn can_bend(lanes: &[Option<Lane>], node: usize, parents: &[String]) -> bool {
    let needs_new_lane = parents[1..]
        .iter()
        .any(|p| !lanes.iter().any(|lane| lane.as_ref().is_some_and(|l| l.expect == p)));
    !needs_new_lane || lanes[..node].iter().any(Option::is_none)
}

fn free_slot(lanes: &mut Vec<Option<Lane>>) -> usize {
    match lanes.iter().position(Option::is_none) {
        Some(ix) => ix,
        None => {
            lanes.push(None);
            lanes.len() - 1
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn commit(id: &str, parents: &[&str]) -> Commit {
        Commit {
            id: id.into(),
            parents: parents.iter().map(|p| p.to_string()).collect(),
            author: String::new(),
            email: String::new(),
            time: 0,
            subject: String::new(),
            refs: Vec::new(),
        }
    }

    #[test]
    fn linear_history_stays_in_one_lane() {
        let rows = layout(&[commit("c", &["b"]), commit("b", &["a"]), commit("a", &[])]);
        assert!(rows.iter().all(|row| row.node == 0 && row.width == 1));
    }

    #[test]
    fn merge_opens_and_closes_a_lane() {
        // m merges f into b; both come from a.
        let rows = layout(&[
            commit("m", &["b", "f"]),
            commit("f", &["a"]),
            commit("b", &["a"]),
            commit("a", &[]),
        ]);
        assert_eq!(rows[0].node, 0);
        assert!(rows[0].edges.contains(&Edge { from: 0, to: 1, half: Half::Bottom, color: 1 }));
        assert_eq!(rows[1].node, 1);
        assert_eq!(rows[2].node, 0);
        // f's lane bends back into the first-parent lane at b.
        assert!(rows[2].edges.iter().any(|e| e.half == Half::Bottom && e.from == 1 && e.to == 0));
        assert_eq!(rows[3].node, 0);
        assert_eq!(rows[3].width, 1);
    }

    /// Two edges in the same half of a row cross when they swap sides.
    fn has_crossing(row: &GraphRow) -> bool {
        row.edges.iter().any(|a| {
            row.edges.iter().any(|b| {
                a.half == b.half
                    && (a.from as isize - b.from as isize) * (a.to as isize - b.to as isize) < 0
            })
        })
    }

    #[test]
    fn merge_does_not_cross_a_bending_lane() {
        // M's first parent A is already expected right of M's lane, and its
        // second parent B needs a new lane.
        let rows = layout(&[
            commit("y", &["m"]),
            commit("x", &["a"]),
            commit("m", &["a", "b"]),
            commit("b", &["a"]),
            commit("a", &[]),
        ]);
        for (ix, row) in rows.iter().enumerate() {
            assert!(!has_crossing(row), "row {ix} crosses: {:?}", row.edges);
        }
        assert_eq!(rows[4].node, 0);
    }
}
