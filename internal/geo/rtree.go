package geo

import (
	"github.com/peterstace/simplefeatures/geom"
	"github.com/peterstace/simplefeatures/rtree"
)

// GeomIndex is a spatial index over the leaf sub-geometries of a Geometry.
// After construction it is read-only and safe for concurrent use.
type GeomIndex struct {
	tree  *rtree.RTree
	parts []geom.Geometry // indexed by BulkItem.RecordID
}

// NewGeomIndex decomposes g into leaf geometries via Dump() and indexes each
// by its bounding box in an R-tree built with BulkLoad.
func NewGeomIndex(g geom.Geometry) *GeomIndex {
	return NewGeomIndexFromGeoms(g.Dump())
}

// NewGeomIndexFromGeoms builds an R-tree over the supplied geometries directly,
// without an intermediate Dump(). Use this when the caller already has the
// individual parts — e.g. buffered features before any union pass.
func NewGeomIndexFromGeoms(parts []geom.Geometry) *GeomIndex {
	items := make([]rtree.BulkItem, 0, len(parts))
	for i, p := range parts {
		env := p.Envelope()
		lo, hi, ok := env.MinMaxXYs()
		if !ok {
			continue
		}
		items = append(items, rtree.BulkItem{
			Box:      rtree.Box{MinX: lo.X, MinY: lo.Y, MaxX: hi.X, MaxY: hi.Y},
			RecordID: i,
		})
	}
	return &GeomIndex{
		tree:  rtree.BulkLoad(items),
		parts: parts,
	}
}

// Search returns all sub-geometries whose bounding boxes intersect env.
func (idx *GeomIndex) Search(env geom.Envelope) []geom.Geometry {
	lo, hi, ok := env.MinMaxXYs()
	if !ok {
		return nil
	}
	box := rtree.Box{MinX: lo.X, MinY: lo.Y, MaxX: hi.X, MaxY: hi.Y}
	var results []geom.Geometry
	_ = idx.tree.RangeSearch(box, func(id int) error {
		results = append(results, idx.parts[id])
		return nil
	})
	return results
}

// Len returns the number of indexed sub-geometries.
func (idx *GeomIndex) Len() int {
	return len(idx.parts)
}
