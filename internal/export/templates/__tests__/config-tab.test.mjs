// The Config tab of a multi-city page (solvent-streets-ern4.36): it shows the
// settings of the city in view, from that city's meta.json settings_toml, and
// follows the city select, instead of dumping the whole combined config
// (hundreds of kilobytes) for every city.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { load } from './harness.mjs';
import { fullData, metaJSON } from './fixtures.mjs';

test('the fixture meta.json carries the city settings the exporter resolved', () => {
  // REAL BuildMeta output (js_harness_test.go): the city's own [[cities]]
  // entry, not the whole config.
  assert.match(metaJSON.settings_toml, /\[\[cities\]\]/);
  assert.match(metaJSON.settings_toml, /name = "Alpha, CA"/);
  assert.equal((metaJSON.settings_toml.match(/\[\[cities\]\]/g) || []).length, 1, 'one city, not the whole file');
});

test('the Config tab shows the city in view and switches with the city select', async () => {
  const beta = { ...metaJSON, project_name: 'Beta, CA', settings_toml: '[[cities]]\n  name = "Beta, CA"\n' };
  // Key order matters: the fetch stub takes the first key the path ends with.
  const data = { 'beta-ca/data/meta.json': beta, ...fullData };
  const { win, $, flush } = load({ data, url: 'https://example.test/index.html?city=alpha-ca&tab=config-tab' });
  await flush();
  await win.loadCityData();
  await flush();

  assert.equal($('#config-city-name').textContent, 'Alpha, CA');
  assert.equal($('#config-city-toml').textContent, metaJSON.settings_toml);
  assert.doesNotMatch($('#config-tab').textContent, /Input Configuration|Effective configuration/,
    'a multi-city page does not render the whole combined file');

  win.selectCity('beta-ca');
  await flush();
  assert.equal($('#config-city-name').textContent, 'Beta, CA');
  assert.match($('#config-city-toml').textContent, /name = "Beta, CA"/);
});

test('a city whose meta.json fails to load keeps nothing of the previous city (solvent-streets-06q4)', async () => {
  const data = { ...fullData };
  const { win, $, flush, fireMap } = load({ data, url: 'https://example.test/index.html?city=alpha-ca&tab=config-tab' });
  fireMap('style.load'); await flush(10);
  const before = $('#config-city-toml').textContent;
  assert.notEqual($('#stats').innerHTML, '');
  delete data['meta.json'];
  win.selectCity('beta-ca'); await flush(10);
  assert.notEqual($('#config-city-toml').textContent, before);
  assert.equal($('#config-city-toml').textContent, 'No settings were published for this city.');
  assert.equal($('#stats').innerHTML, '');
});
