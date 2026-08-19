// Data fixtures shaped like the real exported files. Field names mirror the Go
// structs (internal/export/forecast.go, seeds.go) — if those change, these
// tests should break, which is the point.

function years(n, { pci = 68, need = 1_650_000, spend = 0 } = {}) {
  const out = [];
  let backlog = 0;
  for (let y = 1; y <= n; y++) {
    pci = Math.max(0, pci - 2);
    backlog += Math.max(0, need - spend);
    out.push({
      year: y, pci, area: 3_300_000,
      annual_need: need, annual_spend: spend, deferred_backlog: backlog,
      cost_tier: pci < 40 ? 'reconstruction' : 'rehab',
    });
  }
  return out;
}

const cohorts = [
  { classification: 'residential', end_pci: 28, area: 2_000_000, decay_rate: 0.045, total_spend: 12_000_000, total_deficit: 3_000_000 },
  { classification: 'primary', end_pci: 34, area: 1_300_000, decay_rate: 0.03, total_spend: 8_000_000, total_deficit: 2_000_000 },
];

function scenario(name, strategy, opts) {
  return { scenario: { name, strategy }, years: years(20, opts), final_cohorts: cohorts };
}

export const scenariosJSON = {
  summary: { city_count: 900, all_count: 1200, city_pct: 0.75 },
  city: [scenario('do-nothing', 0), scenario('maintain', 1, { spend: 1_000_000 })],
  bbox: [scenario('do-nothing', 0), scenario('maintain', 1, { spend: 1_200_000 })],
};

export const forecastJSON = [{
  resource_type: 'roads',
  baseline: scenario('baseline', 0),
  bbox_baseline: scenario('baseline', 0),
  scenarios: [scenario('maintain', 1, { spend: 1_000_000 })],
  insolvency_year: 7,
  break_even_budget: 18_000_000,
  current_budget: 15_000_000,
  funding_gap: 0.2,
}];

export const seedJSON = {
  initial_pci: 68, decay_rate: 0.04, growth_rate: 0, years: 20,
  treatment_cycle_years: 12, cost_overhead: 1, area: 3_300_000, city_area: 3_300_000,
  cost_tiers: [
    { min_pci: 70, max_pci: 101, cost_per_sqm: 5, label: 'preventive' },
    { min_pci: 40, max_pci: 70, cost_per_sqm: 50, label: 'rehab' },
    { min_pci: 0, max_pci: 40, cost_per_sqm: 150, label: 'reconstruction' },
  ],
  cohorts: [
    { classification: 'residential', area: 2_000_000, decay_rate: 0.045, initial_pci: 68 },
    { classification: 'primary', area: 1_300_000, decay_rate: 0.03, initial_pci: 68 },
  ],
};

export const metaJSON = {
  project_name: 'Alpha', city_area: 40_000_000, total_area: 3_300_000,
  pct_paved: 8.25, feature_count: 1234,
};

// fullData is everything a city needs for the Financials tab to render.
export const fullData = {
  'meta.json': metaJSON,
  'scenarios.json': scenariosJSON,
  'forecast.json': forecastJSON,
  'forecast_seed.json': seedJSON,
};
