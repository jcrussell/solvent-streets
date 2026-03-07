package server

import (
	"encoding/json"
	"net/http"

	"pvmt/internal/geo"
	"pvmt/internal/resource"
)

type statsResponse struct {
	Type           string  `json:"type"`
	TotalAreaSqFt  float64 `json:"total_area_sqft"`
	TotalAreaAcres float64 `json:"total_area_acres"`
	FeatureCount   int     `json:"feature_count"`
	GeoJSON        string  `json:"geojson,omitempty"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	typeParam := r.URL.Query().Get("type")
	if typeParam == "" {
		typeParam = "all"
	}

	var results []statsResponse

	if typeParam == "all" {
		for _, rt := range resource.All {
			sr, err := s.statsForType(rt.Name())
			if err != nil {
				continue
			}
			results = append(results, *sr)
		}
	} else {
		sr, err := s.statsForType(typeParam)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		results = append(results, *sr)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(results); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) statsForType(resourceType string) (*statsResponse, error) {
	result, err := s.store.LatestComputeResult(resourceType)
	if err != nil {
		return nil, err
	}

	return &statsResponse{
		Type:           result.ResourceType,
		TotalAreaSqFt:  result.TotalAreaSqFt,
		TotalAreaAcres: result.TotalAreaAcres,
		FeatureCount:   result.FeatureCount,
		GeoJSON:        result.GeometryJSON,
	}, nil
}

type configResponse struct {
	ProjectName string  `json:"project_name"`
	CenterLon   float64 `json:"center_lon"`
	CenterLat   float64 `json:"center_lat"`
	BBox        [4]float64 `json:"bbox"`
}

func (s *Server) handleHexStats(w http.ResponseWriter, r *http.Request) {
	typeParam := r.URL.Query().Get("type")
	if typeParam == "" {
		typeParam = "pavements"
	}

	stats, err := s.store.ListHexStats(typeParam)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build GeoJSON FeatureCollection with hex center points and properties
	// We use the hex ID to reconstruct center coordinates
	lon0, lat0 := s.cfg.Center()
	proj := geo.NewUTMProjector(lon0, lat0)
	hexEdge := s.cfg.HexEdge()

	// Project bbox to get hex grid
	bbox := s.cfg.Area.BBox
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0])
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2])
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)

	// Index hexes by ID
	hexMap := make(map[string]*geo.Hex, len(hexes))
	for i := range hexes {
		hexMap[hexes[i].ID] = &hexes[i]
	}

	type hexFeature struct {
		Type       string         `json:"type"`
		Geometry   json.RawMessage `json:"geometry"`
		Properties map[string]any `json:"properties"`
	}

	var features []hexFeature
	for _, st := range stats {
		h, ok := hexMap[st.HexID]
		if !ok {
			continue
		}
		gjson, err := geo.GeometryToGeoJSON(h.Geom, proj)
		if err != nil {
			continue
		}
		features = append(features, hexFeature{
			Type:     "Feature",
			Geometry: json.RawMessage(gjson),
			Properties: map[string]any{
				"hex_id":      st.HexID,
				"area_sqft":   st.AreaSqFt,
				"pct_covered": st.PctCovered,
			},
		})
	}

	fc := map[string]any{
		"type":     "FeatureCollection",
		"features": features,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	resp := configResponse{
		ProjectName: s.cfg.Project.Name,
		BBox:        s.cfg.Area.BBox,
	}
	resp.CenterLon, resp.CenterLat = s.cfg.Center()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
