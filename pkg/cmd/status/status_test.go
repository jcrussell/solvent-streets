package status

import (
	"strings"
	"testing"
	"time"

	"pvmt/internal/db"
	"pvmt/internal/db/dbtest"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"
)

func TestNewCmdStatus_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdStatus(f, rt, func(opts *Options) error {
		called = true
		return nil
	})

	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("runF was not called")
	}
}

func TestRunStatus_SingleResource(t *testing.T) {
	now := time.Now()
	store := &dbtest.MockStore{
		StatsFunc: func(rt string) (*db.StatusInfo, error) {
			if rt == "roads" {
				return &db.StatusInfo{
					ResourceType: "roads",
					FeatureCount: 42,
					LastIngestAt: &now,
				}, nil
			}
			return &db.StatusInfo{ResourceType: rt}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		CityDB: func() (db.Store, error) {
			return store, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdStatus(f, rt, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "roads") {
		t.Errorf("expected roads in output, got: %s", output)
	}
	if !strings.Contains(output, "42") {
		t.Errorf("expected feature count 42 in output, got: %s", output)
	}
}

func TestRunStatus_AllResources(t *testing.T) {
	store := &dbtest.MockStore{
		StatsFunc: func(rt string) (*db.StatusInfo, error) {
			switch rt {
			case "roads":
				return &db.StatusInfo{ResourceType: "roads", FeatureCount: 10}, nil
			case "parking":
				return &db.StatusInfo{ResourceType: "parking", FeatureCount: 5}, nil
			default:
				return &db.StatusInfo{ResourceType: rt}, nil
			}
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		CityDB: func() (db.Store, error) {
			return store, nil
		},
	}

	// nil ResourceType means show all
	cmd := NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "roads") || !strings.Contains(output, "parking") {
		t.Errorf("expected both resource types in output, got: %s", output)
	}
}

func TestRunStatus_CitySummary(t *testing.T) {
	// ~1km x 1km boundary polygon
	boundaryGJSON := `{"type":"Polygon","coordinates":[[[-97.745,30.265],[-97.7346,30.265],[-97.7346,30.274],[-97.745,30.274],[-97.745,30.265]]]}`

	store := &dbtest.MockStore{
		StatsFunc: func(rt string) (*db.StatusInfo, error) {
			if rt == "roads" {
				return &db.StatusInfo{
					ResourceType:  "roads",
					FeatureCount:  100,
					TotalAreaSqFt: 500000,
					TotalAreaAcres: 11.48,
				}, nil
			}
			return &db.StatusInfo{ResourceType: rt}, nil
		},
		GetBoundaryFunc: func() (string, error) {
			return boundaryGJSON, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	ios.SetTTY(true)
	f := &cmdutil.Factory{
		IOStreams: ios,
		CityDB: func() (db.Store, error) {
			return store, nil
		},
	}

	cmd := NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "City Summary") {
		t.Errorf("expected City Summary in output, got: %s", output)
	}
	if !strings.Contains(output, "City Area:") {
		t.Errorf("expected City Area in output, got: %s", output)
	}
	if !strings.Contains(output, "Paved Area:") {
		t.Errorf("expected Paved Area in output, got: %s", output)
	}
	if !strings.Contains(output, "% Paved:") {
		t.Errorf("expected %% Paved in output, got: %s", output)
	}
}

func TestRunStatus_NonTTY_TabSeparated(t *testing.T) {
	store := &dbtest.MockStore{
		StatsFunc: func(rt string) (*db.StatusInfo, error) {
			if rt == "roads" {
				return &db.StatusInfo{ResourceType: "roads", FeatureCount: 7}, nil
			}
			return &db.StatusInfo{ResourceType: rt}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	// Test() returns isTTY=false by default
	f := &cmdutil.Factory{
		IOStreams: ios,
		CityDB: func() (db.Store, error) {
			return store, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdStatus(f, rt, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "\t") {
		t.Errorf("expected tab-separated output for non-TTY, got: %s", output)
	}
	if !strings.Contains(output, "roads") {
		t.Errorf("expected 'roads' in output, got: %s", output)
	}
}
