const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const template = fs.readFileSync('templates/dashboard.tmpl', 'utf8');
const source = template.slice(template.indexOf('let deployBusy ='), template.indexOf('function connectStats()'));
const elements = new Map();
const timers = new Map();
const requests = [];
let timerId = 0;
const responses = [];
function element(id) {
  if (!elements.has(id)) elements.set(id, {
    button: {disabled: false},
    classList: {add() {}},
    querySelector() { return this.button; },
    addEventListener(name, callback) { this[name] = callback; }
  });
  return elements.get(id);
}
const context = vm.createContext({
  document: {getElementById: element},
  AbortSignal,
  setTimeout(callback) { timers.set(++timerId, callback); return timerId; },
  clearTimeout(id) { timers.delete(id); },
  async fetch(url, options) {
    requests.push({url, options});
    assert.ok(responses.length, 'unexpected network request');
    const next = responses.shift();
    if (next instanceof Error) throw next;
    return {ok: true, status: 200, json: async () => next};
  }
});
async function settle() { for (let i = 0; i < 10; i++) await Promise.resolve(); }
async function poll() {
  assert.equal(timers.size, 1, 'expected a single status retry');
  const [id, callback] = timers.entries().next().value;
  timers.delete(id);
  await callback();
}
(async () => {
  responses.push({state: 'idle'});
  vm.runInContext(source, context);
  await settle();
  let prevented = false;
  responses.push(new Error('proxy dropped acknowledgement'));
  await element('traefik-redeploy-form').submit({preventDefault() { prevented = true; }});
  assert.ok(prevented, 'form must not navigate');
  assert.ok(element('traefik-stop-form').button.disabled);
  await element('traefik-update-form').submit({preventDefault() {}});
  assert.equal(requests.filter(r => r.options.method === 'POST').length, 1, 'duplicate click must not redeploy');
  responses.push(new Error('proxy still restarting'));
  await poll();
  assert.match(element('traefik-deploy-status').textContent, /reconnect/);
  responses.push({state: 'running', message: 'Updating Traefik'});
  await poll();
  responses.push({state: 'done', message: 'Traefik redeployed successfully.'});
  await poll();
  assert.equal(timers.size, 0, 'completion must stop polling');
  assert.equal(element('traefik-redeploy-form').button.disabled, false);
  assert.match(element('traefik-deploy-status').textContent, /successfully/);
  assert.equal(requests.filter(r => r.options.method === 'POST').length, 1);
})().catch(error => { console.error(error); process.exitCode = 1; });
