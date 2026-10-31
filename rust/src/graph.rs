//! Lane layout for the commit graph.
//!
//! Each row is drawn independently: the top half connects the lanes coming
//! from the row above to this row's node (or straight through), the bottom
//! half connects the node to its parents' lanes and continues the others.
//!
//! A big history can have hundreds of lanes side by side, nearly all of
//! them just passing a row. So a row keeps the lanes around it as shared
//! snapshots of their colors, which neighbouring rows mostly have in
//! common, and only the edges of its own node.
//!
//! Like JetBrains, an edge that would run further than `LONG_EDGE` rows is
//! cut: it's drawn as a short arrow leaving the child's node, and another
//! arriving at the parent's, rather than a lane all the way. Otherwise
//! long-lived branches merged into a busy history would make the graph
//! hundreds of lanes wide. The edges of one commit, the one selected, can be kept
//! whole, to show where its arrows lead.

use std::sync::Arc;

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

/// Rows an edge may span before it's cut short.
const LONG_EDGE: usize = 50;
/// Cut edges arriving at one commit, or leaving one, that get an arrow; a
/// commit hundreds of branches start from shows only the first few.
const MOST_ARROWS: usize = 3;

/// A cut edge, as a short arrow at a node: leaving the child in the bottom
/// half of its row, arriving at the parent in the top half of its row.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Arrow {
    pub half: Half,
    /// Which of the node's arrows in that half it is, to fan them out.
    pub fan: usize,
    pub color: usize,
    /// The row of the child the edge leaves.
    pub child: usize,
    /// Which of the child's parents it goes to.
    pub parent: usize,
    /// The row of the commit at the other end, unless it isn't loaded.
    pub other_end: Option<usize>,
}

#[derive(Clone, Debug, Default)]
pub struct GraphRow {
    pub node: usize,
    pub color: usize,
    /// Number of lanes this row spans.
    pub width: usize,
    /// The colors of the lanes coming in from above, and going on below.
    above: Lanes,
    below: Lanes,
    /// The lanes above that end in the node.
    into_node: Vec<u32>,
    /// The bottom-half edges of the node: to its parents, and lanes bending
    /// into its lane.
    node_edges: Vec<Edge>,
    /// The lanes below that start in this row rather than pass it.
    starting: Vec<u32>,
    pub arrows: Vec<Arrow>,
}

type Lanes = Arc<[Option<u32>]>;

impl GraphRow {
    pub fn edges(&self) -> impl Iterator<Item = Edge> + '_ {
        let top = self.above.iter().enumerate().filter_map(|(ix, color)| {
            let to = if self.into_node.contains(&(ix as u32)) { self.node } else { ix };
            color.map(|color| Edge { from: ix, to, half: Half::Top, color: color as usize })
        });
        let through = self.below.iter().enumerate().filter_map(|(ix, color)| {
            let passes = !self.starting.contains(&(ix as u32));
            color.filter(|_| passes).map(|color| Edge { from: ix, to: ix, half: Half::Bottom, color: color as usize })
        });
        top.chain(self.node_edges.iter().copied()).chain(through)
    }
}

struct Lane<'a> {
    expect: &'a str,
    color: usize,
}

impl<'a> Lane<'a> {
    fn new(expect: &'a str, color: usize) -> Self {
        Self { expect, color }
    }

    /// Whether the edges to `id` end in this lane.
    fn leads_to(&self, id: &str) -> bool {
        self.expect == id
    }
}

/// `whole`: the commit whose edges are never cut short, as long as both ends
/// are loaded.
pub fn layout(commits: &[Commit], whole: Option<&str>) -> Vec<GraphRow> {
    let mut lanes: Vec<Option<Lane>> = Vec::new();
    let mut next_color = 0;
    let mut new_color = || {
        next_color += 1;
        next_color - 1
    };
    let mut rows = Vec::with_capacity(commits.len());
    let mut above: Lanes = Arc::from([]);
    let row_of: std::collections::HashMap<&str, usize> =
        commits.iter().enumerate().map(|(row, c)| (c.id.as_str(), row)).collect();
    // The first parents from the top commit down.
    let mut mainline = std::collections::HashSet::new();
    let mut next = commits.first();
    while let Some(commit) = next.filter(|c| mainline.insert(c.id.as_str())) {
        next = commit.parents.first().and_then(|p| row_of.get(p.as_str())).map(|&row| &commits[row]);
    }
    // The arrows of cut edges arriving at the commit in a row below.
    let mut arriving: std::collections::HashMap<usize, Vec<Arrow>> = Default::default();

    for (row, commit) in commits.iter().enumerate() {
        let mut arrows: Vec<Arrow> = arriving.remove(&row).unwrap_or_default();
        arrows.truncate(MOST_ARROWS);
        for (fan, arrow) in arrows.iter_mut().enumerate() {
            arrow.fan = fan;
        }

        let matching: Vec<usize> = lanes
            .iter()
            .enumerate()
            .filter(|(_, lane)| lane.as_ref().is_some_and(|l| l.leads_to(&commit.id)))
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

        let mut cut = 0;
        for (parent_ix, parent) in commit.parents.iter().enumerate() {
            let existing = lanes
                .iter()
                .position(|lane| lane.as_ref().is_some_and(|l| l.leads_to(parent)));
            match existing {
                // Keep the first-parent line in the leftmost lane: the lane to
                // the right bends into this one instead. A merge only does so
                // when its other parents can go left of the node; otherwise
                // their new lane would cross the bend.
                Some(ix) if parent_ix == 0 && ix > node && can_bend(&lanes, node, &commit.parents) => {
                    let other = lanes[ix].take().unwrap();
                    ensure_len(&mut lanes, node + 1);
                    lanes[node] = Some(Lane::new(parent, color));
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
                    let at = row_of.get(parent.as_str()).copied();
                    // Far below, not loaded, or (with clocks off) above. The
                    // mainline, read top to bottom, is never cut.
                    let on_mainline = parent_ix == 0 && mainline.contains(commit.id.as_str());
                    let kept = on_mainline || whole.is_some_and(|id| id == commit.id || id == parent);
                    let long = match at {
                        None => true,
                        Some(at) => at <= row || (!kept && at - row > LONG_EDGE),
                    };
                    if long {
                        cut += 1;
                        if cut > MOST_ARROWS {
                            continue;
                        }
                        let color = if parent_ix == 0 { color } else { new_color() };
                        let arrow = Arrow { half: Half::Bottom, fan: cut - 1, color, child: row, parent: parent_ix, other_end: at };
                        arrows.push(arrow);
                        let up = Arrow { half: Half::Top, other_end: Some(row), ..arrow };
                        match at {
                            Some(at) if at > row => arriving.entry(at).or_default().push(up),
                            Some(at) => {
                                let above: &mut GraphRow = &mut rows[at];
                                let fan = above.arrows.iter().filter(|a| a.half == Half::Top).count();
                                if fan < MOST_ARROWS {
                                    above.arrows.push(Arrow { fan, ..up });
                                    above.width = above.width.max(above.node + 2);
                                }
                            }
                            None => {}
                        }
                        continue;
                    }
                    let (slot, color) = if parent_ix == 0 { (node, color) } else { (free_slot(&mut lanes), new_color()) };
                    ensure_len(&mut lanes, slot + 1);
                    lanes[slot] = Some(Lane::new(parent, color));
                    edges.push(Edge {
                        from: node,
                        to: slot,
                        half: Half::Bottom,
                        color,
                    });
                }
            }
        }


        let mut starting = Vec::new();
        for (ix, lane) in lanes.iter().enumerate() {
            let Some(lane) = lane else { continue };
            let continues = before
                .get(ix)
                .copied()
                .flatten()
                .is_some_and(|expect| expect == lane.expect && expect != commit.id);
            if !continues {
                starting.push(ix as u32);
            }
        }

        while matches!(lanes.last(), Some(None)) {
            lanes.pop();
        }

        let colors: Vec<Option<u32>> = lanes.iter().map(|lane| lane.as_ref().map(|l| l.color as u32)).collect();
        let below: Lanes = if *above == *colors { above.clone() } else { Arc::from(colors) };
        // Arrows reach into the lane right of the node.
        let width = [above.len(), below.len(), node + 1 + !arrows.is_empty() as usize].into_iter().max().unwrap_or(1);
        rows.push(GraphRow {
            node,
            color,
            width,
            above: std::mem::replace(&mut above, below.clone()),
            below,
            into_node: matching.iter().map(|&ix| ix as u32).collect(),
            node_edges: edges,
            starting,
            arrows,
        });
    }
    rows
}

/// Roughly the memory `rows` take, for benchmarks.
#[cfg(test)]
pub fn heap_size(rows: &[GraphRow]) -> usize {
    let mut snapshots = std::collections::HashSet::new();
    rows.iter()
        .map(|row| {
            let lanes: usize = [&row.above, &row.below]
                .into_iter()
                .filter(|lanes| snapshots.insert(Arc::as_ptr(lanes) as *const u8 as usize))
                .map(|lanes| lanes.len() * size_of::<Option<u32>>())
                .sum();
            size_of::<GraphRow>()
                + lanes
                + row.into_node.capacity() * 4
                + row.starting.capacity() * 4
                + row.node_edges.capacity() * size_of::<Edge>()
        })
        .sum()
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
        let rows = layout(&[commit("c", &["b"]), commit("b", &["a"]), commit("a", &[])], None);
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
        ], None);
        assert_eq!(rows[0].node, 0);
        assert!(rows[0].edges().any(|e| e == Edge { from: 0, to: 1, half: Half::Bottom, color: 1 }));
        assert_eq!(rows[1].node, 1);
        assert_eq!(rows[2].node, 0);
        // f's lane bends back into the first-parent lane at b.
        assert!(rows[2].edges().any(|e| e.half == Half::Bottom && e.from == 1 && e.to == 0));
        assert_eq!(rows[3].node, 0);
        assert_eq!(rows[3].width, 1);
    }

    #[test]
    fn long_edge_is_cut_into_arrows_at_both_ends() {
        // m merges f, which sits below a long run of mainline commits.
        let run = LONG_EDGE + 10;
        let ids: Vec<String> = (0..run).map(|i| format!("b{i}")).collect();
        let mut commits = vec![commit("m", &["b0", "f"])];
        for i in 0..run {
            let parent = if i + 1 < run { ids[i + 1].as_str() } else { "a" };
            commits.push(commit(&ids[i], &[parent]));
        }
        commits.push(commit("f", &["a"]));
        commits.push(commit("a", &[]));
        let rows = layout(&commits, None);
        let f = commits.len() - 2;

        // The edge leaves m as an arrow...
        let down = rows[0].arrows.iter().find(|a| a.half == Half::Bottom).expect("an arrow down");
        assert_eq!((down.child, down.parent, down.other_end), (0, 1, Some(f)));
        // ...takes no lane in between...
        assert!(rows[1..f].iter().all(|row| row.width == 1 && row.arrows.is_empty()));
        // ...and arrives at f as one.
        let up = rows[f].arrows.iter().find(|a| a.half == Half::Top).expect("an arrow up");
        assert_eq!((up.child, up.parent, up.other_end, up.color), (0, 1, Some(0), down.color));
        for (ix, row) in rows.iter().enumerate() {
            assert!(!has_crossing(row), "row {ix} crosses");
        }
    }

    #[test]
    fn edges_of_the_commit_kept_whole_are_not_cut() {
        let run = LONG_EDGE + 10;
        let ids: Vec<String> = (0..run).map(|i| format!("b{i}")).collect();
        let mut commits = vec![commit("m", &["b0", "f"])];
        for i in 0..run {
            let parent = if i + 1 < run { ids[i + 1].as_str() } else { "a" };
            commits.push(commit(&ids[i], &[parent]));
        }
        commits.push(commit("f", &["a"]));
        commits.push(commit("a", &[]));
        let f = commits.len() - 2;
        for whole in ["m", "f"] {
            let rows = layout(&commits, Some(whole));
            assert!(rows.iter().all(|row| row.arrows.is_empty()), "kept {whole}");
            assert!(rows[1..f].iter().all(|row| row.width == 2), "kept {whole}");
        }
    }

    /// Two edges in the same half of a row cross when they swap sides.
    fn has_crossing(row: &GraphRow) -> bool {
        let edges: Vec<Edge> = row.edges().collect();
        edges.iter().any(|a| {
            edges.iter().any(|b| {
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
        ], None);
        for (ix, row) in rows.iter().enumerate() {
            assert!(!has_crossing(row), "row {ix} crosses: {:?}", row.edges().collect::<Vec<_>>());
        }
        assert_eq!(rows[4].node, 0);
    }
}
