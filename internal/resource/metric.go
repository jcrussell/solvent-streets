package resource

// A stored result row's metric key comes from the catalog (internal/metric);
// its subject says what it is measured over.

// Subject returns the result-row subject t's measures are stored under: t
// itself, scope included ("roads", "roads:city", "combined:city"), for the
// pavement resources and the combined pass, and "" for anything else.
// Producers that are not about one resource (fire response, census) write
// subject "".
func Subject(t Type) string {
	switch t.Bare() {
	case TypeRoads, TypeParking, TypeSidewalks, TypeCombined:
		return string(t)
	}
	return ""
}
