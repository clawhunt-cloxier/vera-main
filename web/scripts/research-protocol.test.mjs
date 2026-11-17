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
    [{ snapshot_mode: 'collect_latest', quote_requirement: null }, observation, 'application/json'],
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
