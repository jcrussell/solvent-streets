// The cohort table reads forecast.json's roads entry for the scope in view:
// city_baseline's final cohorts under the city scope, baseline's (every
// jurisdiction) under All. Without WASM the page renders it from the file.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, forecastJSON } from './fixtures.mjs';

const roads = forecastJSON.find((f) => f.resource_type === 'roads');

/** totalBacklog is the table's Total row deferred-backlog cell. */
const totalBacklog = (doc) => doc.querySelector('#cohort-table tbody tr.total-row td:last-child').textContent;

/** @param {any[]} cohorts */
const sum = (cohorts) => cohorts.reduce((s, c) => s + c.deferred_backlog, 0);

test('the fixture roads entry has distinct city and all-jurisdiction cohorts', () => {
  assert.ok(roads.city_baseline, 'forecast.json roads has no city_baseline; the Go json tag moved');
  assert.ok(roads.baseline.final_cohorts.length > 1 && roads.city_baseline.final_cohorts.length > 1);
  assert.notEqual(sum(roads.baseline.final_cohorts), sum(roads.city_baseline.final_cohorts));
});

test('the cohort table follows the scope toggle', async () => {
  const { win, doc, flush } = load({ data: fullData });
  await flush();
  const money = (v) => win.PvmtCommon.fmtMoney(v, { full: true });

  win.selectScope('city', { push: false });
  await flush();
  assert.equal(totalBacklog(doc), money(sum(roads.city_baseline.final_cohorts)));

  win.selectScope('all', { push: false });
  await flush();
  assert.equal(totalBacklog(doc), money(sum(roads.baseline.final_cohorts)));

  win.selectScope('city', { push: false });
  await flush();
  assert.equal(totalBacklog(doc), money(sum(roads.city_baseline.final_cohorts)));
});
