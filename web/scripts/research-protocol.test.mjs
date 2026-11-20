import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import ts from 'typescript';

// Exercise the actual browser client with HTTP responses, without a server or live vendor.
const source = readFileSync(new URL('../src/api/client.ts', import.meta.url), 'utf8');
const javascript = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText;
const v04 = 'application/vnd.vera.v0.4+json';
const observation = 'application/vnd.vera.observation.v1+json';
const loadClient = () =>
  import(`data:text/javascript,${encodeURIComponent(javascript)}#${crypto.randomUUID()}`);

test('0.4 research keeps explicit intent and rejects a downgraded response', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => {
    globalThis.fetch = originalFetch;
  });
  const client = await loadClient();
  globalThis.fetch = async (_path, request) => {
    assert.match(request.headers.Accept, /application\/vnd\.vera\.v0\.4\+json/);
    return new Response(JSON.stringify({ research_protocol: 'v0.4' }), {
      headers: { 'Content-Type': v04 },
    });
  };
  await client.get('/api/v1/me');
  client.setCsrf('test-csrf');

  globalThis.fetch = async (_path, request) => {
    assert.equal(request.headers.Accept, v04);
    assert.equal(request.headers['Content-Type'], v04);
    assert.equal(request.headers['X-CSRF-Token'], 'test-csrf');
    assert.equal(JSON.parse(request.body).quote_requirement, 'realtime_trade');
    return new Response(JSON.stringify({ kind: 'plan' }), {
      headers: { 'Content-Type': 'application/json' },
    });
  };
  await assert.rejects(
    client.post('/api/v1/research-plans', {
      text: '分析样例公司',
      quote_requirement: 'realtime_trade',
    }),
    /服务端数据版本不匹配/
  );

  for (const [plan, media, contentType] of [
    [{ snapshot_mode: 'collect_latest', quote_requirement: 'latest_available' }, v04, v04],
    [{ snapshot_mode: 'collect_latest', quote_requirement: 'realtime_trade' }, v04, v04],
    [{ snapshot_mode: 'collect_latest', quote_requirement: null }, observation, observation],
    [
      { snapshot_mode: 'fixed_legacy', quote_requirement: 'latest_available' },
      'application/json',
      'application/json',
    ],
    [{}, 'application/json', 'application/json'],
    [
      { snapshot_mode: 'fixed_legacy', quote_requirement: null },
      'application/json',
      'application/json',
    ],
  ]) {
    globalThis.fetch = async (_path, request) => {
      assert.equal(request.headers.Accept, media);
      assert.equal(request.headers['Content-Type'], contentType);
      assert.equal(request.headers['Idempotency-Key'], 'test-confirmation');
      return new Response(JSON.stringify({ id: 'test-task' }), {
        headers: { 'Content-Type': media },
      });
    };
    await client.post(
      '/api/v1/research-tasks',
      { plan_id: 'test-plan', plan_hash: 'test-hash' },
      {
        protocol: client.planProtocol(plan),
        idem: 'test-confirmation',
      }
    );
  }
});

test('a legacy server cannot advertise 0.4 while returning JSON', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => {
    globalThis.fetch = originalFetch;
  });
  const client = await loadClient();
  globalThis.fetch = async () =>
    new Response(JSON.stringify({ research_protocol: 'v0.4' }), {
      headers: { 'Content-Type': 'application/json' },
    });
  await assert.rejects(client.get('/api/v1/me'), /服务端数据版本不匹配/);
});

test('first request negotiates observation or legacy without changing old plan intent', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => {
    globalThis.fetch = originalFetch;
  });
  for (const [advertised, selected] of [
    ['observation.v1', observation],
    [undefined, 'application/json'],
  ]) {
    const client = await loadClient();
    globalThis.fetch = async (_path, request) => {
      assert.equal(request.headers.Accept, `${v04}, ${observation};q=0.8, application/json;q=0.5`);
      return new Response(JSON.stringify({ research_protocol: advertised }), {
        headers: { 'Content-Type': selected },
      });
    };
    await client.get('/api/v1/me');
    const plan = {
      snapshot_mode: advertised ? 'collect_latest' : 'fixed_legacy',
      quote_requirement: null,
    };
    globalThis.fetch = async (_path, request) => {
      assert.equal(request.headers.Accept, selected);
      assert.equal(request.headers['Content-Type'], selected);
      assert.equal('quote_requirement' in JSON.parse(request.body), false);
      return new Response(JSON.stringify(plan), { headers: { 'Content-Type': selected } });
    };
    await client.post('/api/v1/research-plans', { text: '分析模拟公司' });
    await client.post(
      '/api/v1/research-tasks',
      { plan_id: 'mock-old-plan', plan_hash: 'mock-hash' },
      { protocol: client.planProtocol(plan) }
    );
  }
});

test('0.4 session reads historical reports and accepts empty cancellation acknowledgement', async (t) => {
  const originalFetch = globalThis.fetch;
  t.after(() => {
    globalThis.fetch = originalFetch;
  });
  const client = await loadClient();
  globalThis.fetch = async () =>
    new Response(JSON.stringify({ research_protocol: 'v0.4' }), {
      headers: { 'Content-Type': v04 },
    });
  await client.get('/api/v1/me');
  for (const path of [
    '/api/v1/reports',
    '/api/v1/reports/mock-report/versions/1',
    '/api/v1/reports/mock-report/versions/1/series/mock-series',
  ]) {
    globalThis.fetch = async (_path, request) => {
      assert.equal(request.headers.Accept, v04);
      return new Response(JSON.stringify({ snapshot_mode: 'fixed_legacy' }), {
        headers: { 'Content-Type': v04 },
      });
    };
    assert.equal((await client.get(path)).snapshot_mode, 'fixed_legacy');
  }
  globalThis.fetch = async (_path, request) => {
    assert.equal(request.headers.Accept, v04);
    assert.equal(request.headers['Content-Type'], 'application/json');
    assert.equal(JSON.parse(request.body).expected_revision, 2);
    return new Response(null, { status: 204 });
  };
  assert.equal(
    await client.post('/api/v1/research-tasks/mock-old-task/cancel', { expected_revision: 2 }),
    undefined
  );
});
