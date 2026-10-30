// Package graph lays out the lanes of the commit graph.
//
// Each row is drawn independently: the top half connects the lanes coming
// from the row above to this row's node (or straight through), the bottom
// half connects the node to its parents' lanes and continues the others.
//
// A big history can have hundreds of lanes side by side, nearly all of them
// just passing a row. So a row keeps the lanes around it as shared snapshots
// of their colors, which neighbouring rows mostly have in common, and only
// the edges of its own node.
//
// Like JetBrains, an edge that would run further than longEdge rows is cut:
// it's drawn as a short arrow leaving the child's node, and another arriving
// at the parent's, rather than a lane all the way. Otherwise long-lived
// branches merged into a busy history would make the graph hundreds of lanes
// wide. The edges of one commit, the one selected, can be kept whole, to
// show where its arrows lead.
package graph

import (
	"slices"

	"github.com/Zenyet/banyan/internal/git"
)

type Half int

const (
	Top Half = iota
	Bottom
)

type Edge struct {
	From, To int
	Half     Half
	Color    int
}

// longEdge is how many rows an edge may span before it's cut short.
const longEdge = 50

// mostArrows is how many cut edges arriving at one commit, or leaving one,
// get an arrow; a commit hundreds of branches start from shows only the
// first few.
const mostArrows = 3

// Arrow is a cut edge, as a short arrow at a node: leaving the child in the
// bottom half of its row, arriving at the parent in the top half of its row.
type Arrow struct {
	Half Half
	// Fan is which of the node's arrows in that half it is, to fan them
	// out.
	Fan   int
	Color int
	// Child is the row of the child the edge leaves.
	Child int
	// Parent is which of the child's parents it goes to.
	Parent int
	// OtherEnd is the row of the commit at the other end, -1 if it isn't
	// loaded.
	OtherEnd int
}

// lanes holds the color of each lane, -1 where there is none.
type lanes []int32

type Row struct {
	Node  int
	Color int
	// Width is the number of lanes this row spans.
	Width int
	// above and below are the colors of the lanes coming in from above,
	// and going on below.
	above, below lanes
	// intoNode are the lanes above that end in the node.
	intoNode []int32
	// nodeEdges are the bottom-half edges of the node: to its parents, and
	// lanes bending into its lane.
	nodeEdges []Edge
	// starting are the lanes below that start in this row rather than pass
	// it.
	starting []int32
	Arrows   []Arrow
}

// Edges returns the edges of the row.
func (r *Row) Edges() []Edge {
	edges := make([]Edge, 0, len(r.above)+len(r.below)+len(r.nodeEdges))
	for ix, color := range r.above {
		if color < 0 {
			continue
		}
		to := ix
		if slices.Contains(r.intoNode, int32(ix)) {
			to = r.Node
		}
		edges = append(edges, Edge{ix, to, Top, int(color)})
	}
	edges = append(edges, r.nodeEdges...)
	for ix, color := range r.below {
		if color >= 0 && !slices.Contains(r.starting, int32(ix)) {
			edges = append(edges, Edge{ix, ix, Bottom, int(color)})
		}
	}
	return edges
}

type lane struct {
	expect string
	color  int
}

// Layout lays out the rows of commits. whole is the commit whose edges are
// never cut short, as long as both ends are loaded.
func Layout(commits []git.Commit, whole string) []Row {
	var ls []*lane
	nextColor := 0
	newColor := func() int {
		nextColor++
		return nextColor - 1
	}
	rows := make([]Row, 0, len(commits))
	var above lanes
	rowOf := make(map[string]int, len(commits))
	for row, c := range commits {
		rowOf[c.ID] = row
	}
	// The first parents from the top commit down.
	mainline := map[string]bool{}
	for i := 0; len(commits) > 0; {
		c := &commits[i]
		if mainline[c.ID] {
			break
		}
		mainline[c.ID] = true
		if len(c.Parents) == 0 {
			break
		}
		next, ok := rowOf[c.Parents[0]]
		if !ok {
			break
		}
		i = next
	}
	// The arrows of cut edges arriving at the commit in a row below.
	arriving := map[int][]Arrow{}

	for row := range commits {
		commit := &commits[row]
		arrows := arriving[row]
		delete(arriving, row)
		if len(arrows) > mostArrows {
			arrows = arrows[:mostArrows]
		}
		for fan := range arrows {
			arrows[fan].Fan = fan
		}

		var matching []int
		for ix, l := range ls {
			if l != nil && l.expect == commit.ID {
				matching = append(matching, ix)
			}
		}

		var node, color int
		if len(matching) > 0 {
			node, color = matching[0], ls[matching[0]].color
		} else {
			node, color = freeSlot(&ls), newColor()
		}

		var edges []Edge
		before := make([]string, len(ls))
		for ix, l := range ls {
			if l != nil {
				before[ix] = l.expect
			}
		}
		for _, ix := range matching {
			ls[ix] = nil
		}
		if len(matching) == 0 && node < len(ls) {
			ls[node] = nil
		}

		cut := 0
		for parentIx, parent := range commit.Parents {
			existing := -1
			for ix, l := range ls {
				if l != nil && l.expect == parent {
					existing = ix
					break
				}
			}
			switch {
			// Keep the first-parent line in the leftmost lane: the lane to
			// the right bends into this one instead. A merge only does so
			// when its other parents can go left of the node; otherwise
			// their new lane would cross the bend.
			case existing >= 0 && parentIx == 0 && existing > node && canBend(ls, node, commit.Parents):
				other := ls[existing]
				ls[existing] = nil
				ensureLen(&ls, node+1)
				ls[node] = &lane{parent, color}
				edges = append(edges, Edge{node, node, Bottom, color}, Edge{existing, node, Bottom, other.color})
			case existing >= 0:
				edges = append(edges, Edge{node, existing, Bottom, ls[existing].color})
			default:
				at, loaded := rowOf[parent]
				// Far below, not loaded, or (with clocks off) above. The
				// mainline, read top to bottom, is never cut.
				onMainline := parentIx == 0 && mainline[commit.ID]
				kept := onMainline || (whole != "" && (whole == commit.ID || whole == parent))
				long := !loaded || at <= row || (!kept && at-row > longEdge)
				if long {
					cut++
					if cut > mostArrows {
						continue
					}
					arrowColor := color
					if parentIx != 0 {
						arrowColor = newColor()
					}
					otherEnd := -1
					if loaded {
						otherEnd = at
					}
					arrow := Arrow{Half: Bottom, Fan: cut - 1, Color: arrowColor, Child: row, Parent: parentIx, OtherEnd: otherEnd}
					arrows = append(arrows, arrow)
					up := arrow
					up.Half, up.OtherEnd = Top, row
					switch {
					case loaded && at > row:
						arriving[at] = append(arriving[at], up)
					case loaded:
						r := &rows[at]
						fan := 0
						for _, a := range r.Arrows {
							if a.Half == Top {
								fan++
							}
						}
						if fan < mostArrows {
							up.Fan = fan
							r.Arrows = append(r.Arrows, up)
							r.Width = max(r.Width, r.Node+2)
						}
					}
					continue
				}
				slot, c := node, color
				if parentIx != 0 {
					slot, c = freeSlot(&ls), newColor()
				}
				ensureLen(&ls, slot+1)
				ls[slot] = &lane{parent, c}
				edges = append(edges, Edge{node, slot, Bottom, c})
			}
		}

		var starting []int32
		for ix, l := range ls {
			if l == nil {
				continue
			}
			continues := ix < len(before) && before[ix] != "" && before[ix] == l.expect && before[ix] != commit.ID
			if !continues {
				starting = append(starting, int32(ix))
			}
		}

		for len(ls) > 0 && ls[len(ls)-1] == nil {
			ls = ls[:len(ls)-1]
		}

		colors := make(lanes, len(ls))
		for ix, l := range ls {
			colors[ix] = -1
			if l != nil {
				colors[ix] = int32(l.color)
			}
		}
		below := colors
		if slices.Equal(above, colors) {
			below = above
		}
		// Arrows reach into the lane right of the node.
		width := max(len(above), len(below), node+1)
		if len(arrows) > 0 {
			width = max(width, node+2)
		}
		into := make([]int32, len(matching))
		for i, ix := range matching {
			into[i] = int32(ix)
		}
		rows = append(rows, Row{
			Node:      node,
			Color:     color,
			Width:     width,
			above:     above,
			below:     below,
			intoNode:  into,
			nodeEdges: edges,
			starting:  starting,
			Arrows:    arrows,
		})
		above = below
	}
	return rows
}

func ensureLen(ls *[]*lane, n int) {
	for len(*ls) < n {
		*ls = append(*ls, nil)
	}
}

// canBend reports whether the other parents of a merge at node fit without
// crossing a lane that bends into the node from the right.
func canBend(ls []*lane, node int, parents []string) bool {
	needsNewLane := false
	for _, p := range parents[1:] {
		if !slices.ContainsFunc(ls, func(l *lane) bool { return l != nil && l.expect == p }) {
			needsNewLane = true
		}
	}
	return !needsNewLane || slices.Contains(ls[:node], nil)
}

func freeSlot(ls *[]*lane) int {
	if ix := slices.Index(*ls, nil); ix >= 0 {
		return ix
	}
	*ls = append(*ls, nil)
	return len(*ls) - 1
}
