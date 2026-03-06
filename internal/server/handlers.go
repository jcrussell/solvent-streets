package server

import (
	"encoding/json"
	"net/http"

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
