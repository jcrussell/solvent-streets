// Package coherence judges whether a city's latest compute may be frozen as
// a snapshot. A snapshot must be coherent: every part computed from the
// inputs (features and boundary) the city has stored now, pavement present,
// and nothing ingested that its producer has not read. The judgement is
// digests, not clocks: each run records the digest of every input its
// producer reads (db.ProducerInputs), and Check compares them with the
// current ones.
//
// Check is pure, so `pvmt snapshots create` can run it inside the
// transaction that freezes the snapshot, and `pvmt status` can show the same
// verdict.
package coherence

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/services"
)

// Report is the verdict on one city's latest compute. Refusals block a
// snapshot; Warnings do not. Hint is the one command that clears every
// refusal ("" when there is none).
type Report struct {
	Refusals []string `json:"refusals,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Hint     string   `json:"hint,omitempty"`
}

// Refused reports whether the latest compute may not be snapshotted.
func (r Report) Refused() bool { return len(r.Refusals) > 0 }

// Err returns a *RefusedError carrying r when r refuses, else nil.
func (r Report) Err() error {
	if !r.Refused() {
		return nil
	}
	return &RefusedError{Report: r}
}

// RefusedError is the error a refused Report becomes (Report.Err), so it can
// pass through db.Store.CreateSnapshot's check and be recovered with
// errors.As.
type RefusedError struct{ Report Report }

func (e *RefusedError) Error() string {
	return "not coherent: " + strings.Join(e.Report.Refusals, "; ")
}

// timeLayout is how a refusal names when an input changed.
const timeLayout = "2006-01-02 15:04 UTC"

// Check judges city's latest compute: parts as db.Store.LatestCompute returns
// them, inputs as db.Store.InputStates does (an input with no row has digest
// "").
//
// It refuses when
//   - a part recorded a digest for an input that differs from the current
//     one (including an input that was absent then and is present now);
//   - there is no pavement part (roads, parking or sidewalks);
//   - there is pavement but no combined part;
//   - a pavement resource is ingested but has no part.
//
// It warns, without refusing, when an optional part is absent though its
// inputs are there: a service's coverage when roads and its facilities
// (ingested, or [[cities.facilities]]) are, and census when the blocks are
// ingested and the city takes part in census.
func Check(city *config.CityConfig, parts []db.LatestPart, inputs []db.InputState) Report {
	current := make(map[string]db.InputState, len(inputs))
	for _, st := range inputs {
		current[st.Input] = st
	}
	have := make(map[string]db.LatestPart, len(parts))
	for _, p := range parts {
		have[p.Part] = p
	}

	var (
		r     Report
		stale = map[string]bool{} // parts to recompute
	)
	boundaryStale := false

	// Stale parts, grouped by the input that moved on.
	staleBy := map[string][]string{}
	for _, p := range parts {
		for _, in := range db.ProducerInputs(p.Part) {
			if p.Inputs[in] == current[in].Digest {
				continue
			}
			staleBy[in] = append(staleBy[in], p.Part)
			stale[p.Part] = true
			if in == db.InputBoundary {
				boundaryStale = true
			}
		}
	}
	for _, in := range sortedKeys(staleBy) {
		r.Refusals = append(r.Refusals, fmt.Sprintf("%s %s after the %s", inputLabel(in),
			changed(current[in], have, staleBy[in][0]), computes(staleBy[in])))
	}

	// Pavement: present, combined, and every ingested resource computed.
	pavement := false
	for _, rt := range resource.All {
		part := string(rt.Type())
		if _, ok := have[part]; ok {
			pavement = true
			continue
		}
		if st := current[part]; st.Digest != "" {
			r.Refusals = append(r.Refusals, fmt.Sprintf("%s ingested %s but never computed", part, st.IngestedAt.UTC().Format(timeLayout)))
			stale[part] = true
		}
	}
	_, combined := have[string(resource.CombinedAll)]
	switch {
	case !pavement:
		r.Refusals = append(r.Refusals, "no pavement compute (roads, parking or sidewalks)")
	case !combined:
		r.Refusals = append(r.Refusals, "no combined compute (the cross-resource total)")
	}

	if r.Refused() {
		r.Hint = commandHint(city, stale, boundaryStale || !pavement || !combined || stale[string(resource.CombinedAll)])
	}
	r.Warnings = optionalWarnings(city, have, current)
	return r
}

// changed says how input's current state differs from what the first stale
// part recorded: "re-ingested <when>", "ingested <when>" (absent then) or
// "removed <when>" (absent now).
func changed(st db.InputState, have map[string]db.LatestPart, part string) string {
	verb := "re-ingested"
	switch {
	case st.Digest == "":
		verb = "removed"
	case have[part].Inputs[st.Input] == "":
		verb = "ingested"
	}
	if st.IngestedAt.IsZero() {
		return verb
	}
	return verb + " " + st.IngestedAt.UTC().Format(timeLayout)
}

// commandHint is the command that clears the refusals: `all compute` when all is
// set or two or more parts are stale, else the one part's own command.
func commandHint(city *config.CityConfig, stale map[string]bool, all bool) string {
	prefix := "pvmt --city " + city.Slug() + " "
	if all || len(stale) != 1 {
		return prefix + "all compute"
	}
	var part string
	for p := range stale {
		part = p
	}
	return prefix + partCommand(part)
}

// partCommand is the command that recomputes one part.
func partCommand(part string) string {
	switch {
	case part == db.ProducerCensus:
		return "census compute"
	case strings.HasPrefix(part, db.ServiceProducer("")):
		svc := strings.TrimPrefix(part, db.ServiceProducer(""))
		if svc == defaultService {
			return "services compute"
		}
		return "services --service " + svc + " compute"
	}
	return part + " compute"
}

// defaultService is the service `pvmt services` acts on without --service
// (pkg/cmd/services.DefaultService, which this package cannot import).
const defaultService = "fire"

// optionalWarnings names the optional parts that are absent though their
// inputs are stored, with the command that computes each.
func optionalWarnings(city *config.CityConfig, have map[string]db.LatestPart, current map[string]db.InputState) []string {
	var out []string
	prefix := "pvmt --city " + city.Slug() + " "
	roads := current[string(resource.TypeRoads)].Digest != ""
	for _, svc := range services.Names() {
		part := db.ServiceProducer(svc)
		if _, ok := have[part]; ok || !roads {
			continue
		}
		var why string
		switch {
		case current[string(resource.FacilityType(svc))].Digest != "":
			why = "ingested"
		case slices.ContainsFunc(city.Facilities, func(f config.FacilityConfig) bool { return f.Service == svc }):
			why = "configured"
		default:
			continue
		}
		out = append(out, fmt.Sprintf("no %s coverage: %s facilities are %s but never computed (%s)",
			svc, svc, why, prefix+partCommand(part)))
	}
	if _, ok := have[db.ProducerCensus]; !ok && city.CensusEnabled() &&
		current[string(resource.TypeCensusBlocks)].Digest != "" {
		out = append(out, fmt.Sprintf("no census population: census blocks are ingested but never computed (%s)",
			prefix+partCommand(db.ProducerCensus)))
	}
	return out
}

// inputLabel names an input for a person.
func inputLabel(in string) string {
	t := resource.Type(in)
	switch {
	case in == string(resource.TypeCensusBlocks):
		return "census blocks"
	case in == string(resource.TypeRoutingRoads):
		return "routing roads"
	case t.IsFacility():
		return t.FacilityService() + " facilities"
	}
	return in
}

// partLabel names a part for a person.
func partLabel(part string) string {
	if svc, ok := strings.CutPrefix(part, db.ServiceProducer("")); ok {
		return svc + " coverage"
	}
	return part
}

// computes is "the roads compute" / "the roads and combined computes" /
// "the roads, parking and combined computes", without the article.
func computes(parts []string) string {
	labels := make([]string, len(parts))
	for i, p := range parts {
		labels[i] = partLabel(p)
	}
	switch len(labels) {
	case 1:
		return labels[0] + " compute"
	case 2:
		return labels[0] + " and " + labels[1] + " computes"
	}
	return strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1] + " computes"
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Part states, as `pvmt status` shows them.
const (
	StateCurrent     = "current"
	StateStale       = "stale"
	StateNotComputed = "not computed"
)

// PartState is one part's standing in a city's latest compute: not
// computed, current, or stale because inputs it read were ingested again
// since (the same digest comparison Check refuses on).
type PartState struct {
	Part  string
	State string // StateCurrent, StateStale or StateNotComputed
	// Stale lists the inputs that changed since the part read them, as
	// "<input> re-ingested <when>" (or "ingested" / "removed"), ordered by
	// input; StaleInputs has the bare input names in the same order.
	Stale       []string
	StaleInputs []string
}

// String is the state as one table cell: "current", "not computed", or
// "stale: roads re-ingested 2026-11-14 10:02 UTC".
func (s PartState) String() string {
	if s.State != StateStale {
		return s.State
	}
	return "stale: " + strings.Join(s.Stale, ", ")
}

// PartStates returns the state of each named part of the latest compute
// parts, given the city's current inputs (as for Check).
func PartStates(names []string, parts []db.LatestPart, inputs []db.InputState) []PartState {
	current := make(map[string]db.InputState, len(inputs))
	for _, st := range inputs {
		current[st.Input] = st
	}
	have := make(map[string]db.LatestPart, len(parts))
	for _, p := range parts {
		have[p.Part] = p
	}
	out := make([]PartState, len(names))
	for i, name := range names {
		st := PartState{Part: name, State: StateNotComputed}
		if p, ok := have[name]; ok {
			st.State = StateCurrent
			for _, in := range db.ProducerInputs(name) {
				if p.Inputs[in] == current[in].Digest {
					continue
				}
				st.State = StateStale
				st.StaleInputs = append(st.StaleInputs, in)
				st.Stale = append(st.Stale, inputLabel(in)+" "+changed(current[in], have, name))
			}
		}
		out[i] = st
	}
	return out
}
