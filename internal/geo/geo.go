package geo

import "github.com/peterstace/simplefeatures/geom"

type Processor interface {
	Union(geometries []geom.Geometry) (geom.Geometry, error)
	Area(g geom.Geometry) float64
}

type DefaultProcessor struct{}

func (p *DefaultProcessor) Union(geometries []geom.Geometry) (geom.Geometry, error) {
	return UnionAll(geometries)
}

func (p *DefaultProcessor) Area(g geom.Geometry) float64 {
	return AreaSqFt(g)
}
