import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';

const app = readFileSync(new URL('../cmd/vless-manager/web/app.js', import.meta.url), 'utf8');
const section = app.slice(app.indexOf('const TRAFFIC_HISTORY ='), app.indexOf('// TUNNEL SPEED TEST'));
function fixture(api = async () => ({})) {
  const elements = new Map();
  const document = {
    hidden: false,
    querySelectorAll: () => [],
    getElementById: (id) => {
      if (!elements.has(id)) elements.set(id, {});
      return elements.get(id);
    },
  };
  const context = {document, api, isStatusTabActive: () => true,
    fmtRate: String, fmtBytes: String};
  runInNewContext(section + '\ndrawTrafficChart = () => {};\nglobalThis.chart = {fetchTraffic, updateTrafficSeries, renderTrafficSummary, selectTrafficMode, trafficSeries};', context);
  return {chart: context.chart, context, elements, document};
}
const counters = (download, upload = 0, source = 'wan:eth0', available = true) =>
  ({download_bytes: download, upload_bytes: upload, source, available});
const snapshot = (timestamp, bytes, source = 'wan:eth0') => ({
  timestamp, interface: 'eth0', vpn_running: true,
  modes: {all: counters(bytes, 0, source), vpn: counters(bytes, 0, 'core'), bypass: counters(0, 0, 'core')},
});

test('rates use elapsed time and correct download/upload directions', () => {
  const {chart} = fixture();
  chart.updateTrafficSeries('all', counters(5000, 900), counters(1000, 100), 2000, 2);
  assert.deepEqual(Array.from(chart.trafficSeries.all.download), [2000]);
  assert.deepEqual(Array.from(chart.trafficSeries.all.upload), [400]);
});

test('long hidden-tab gaps are not averaged into a fake history point', async () => {
  const values = [snapshot(1000, 0), snapshot(2000, 1000), snapshot(3600000, 100000000), snapshot(3601000, 100002000)];
  const {chart} = fixture(async () => values.shift());
  await chart.fetchTraffic(); await chart.fetchTraffic();
  assert.equal(chart.trafficSeries.all.download.length, 1);
  await chart.fetchTraffic();
  for (const mode of ['all', 'vpn', 'bypass']) assert.equal(chart.trafficSeries[mode].download.length, 0);
  await chart.fetchTraffic();
  assert.deepEqual(Array.from(chart.trafficSeries.all.download), [2000]);
});

test('uplink changes, counter resets, outage and clock jumps reset the baseline', () => {
  for (const [current, previous, dt] of [
    [counters(1000000, 0, 'wan:usb0'), counters(1000), 1],
    [counters(10), counters(1000), 1],
    [counters(1000, 0, 'wan:eth0', false), counters(100), 1],
    [counters(1000), counters(100), 0],
    [counters(1000), counters(100), -1],
  ]) {
    const {chart} = fixture();
    chart.updateTrafficSeries('all', counters(100), counters(0), 1000, 1);
    chart.updateTrafficSeries('all', current, previous, 2000, dt);
    assert.equal(chart.trafficSeries.all.download.length, 0);
  }
});

test('history is bounded by both time and sample count', () => {
  const {chart} = fixture();
  for (let i = 1; i <= 80; i++) chart.updateTrafficSeries('all', counters(i * 100), counters((i - 1) * 100), i * 2000, 2);
  const series = chart.trafficSeries.all;
  assert.equal(series.times.length, 30);
  assert.ok(series.times.at(-1) - series.times[0] < 60000);
});

test('only one traffic request may be in flight', async () => {
  let resolve, calls = 0;
  const response = new Promise(done => { resolve = done; });
  const {chart} = fixture(() => { calls++; return response; });
  const first = chart.fetchTraffic(), second = chart.fetchTraffic();
  assert.equal(calls, 1);
  resolve(snapshot(1000, 100));
  await first; await second;
  await chart.fetchTraffic();
  assert.equal(calls, 2);
});

test('hidden tabs do not poll or accept in-flight samples', async () => {
  let resolve, calls = 0;
  const {chart, document} = fixture(() => { calls++; return new Promise(done => { resolve = done; }); });
  document.hidden = true; await chart.fetchTraffic(); assert.equal(calls, 0);
  document.hidden = false; const request = chart.fetchTraffic(); document.hidden = true;
  resolve(snapshot(1000, 100)); await request;
  assert.equal(chart.trafficSeries.all.download.length, 0);
});

test('WAN label and missing-uplink state are truthful', () => {
  const {chart, elements} = fixture();
  const t = snapshot(1000, 100);
  chart.renderTrafficSummary(t);
  assert.equal(elements.get('traffic-subtitle').textContent, 'Внешний канал · eth0');
  t.modes.all.available = false; chart.renderTrafficSummary(t);
  assert.equal(elements.get('traffic-empty').hidden, false);
  assert.equal(elements.get('traffic-empty').textContent, 'Нет данных внешнего подключения');
});

