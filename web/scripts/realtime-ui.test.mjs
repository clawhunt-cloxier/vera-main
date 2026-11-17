import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { chromium } from '@playwright/test';
import { createServer } from 'vite';

const media = 'application/vnd.vera.v0.4+json';
const entity = {
  id: 'mock-entity',
  name: '模拟验收公司',
  kind: 'stock',
  symbol: 'MOCK',
  venue: 'SSE',
  status: 'listed',
  classification: null,
  skills: [],
};
const plan = {
  id: 'mock-plan',
  plan_hash: 'mock-hash',
  skill_id: 'stock_analysis',
  skill_version: '1.1.0-demo.3',
  subject: entity,
  params: {},
  as_of: '2026-11-17T06:00:00Z',
  snapshot_mode: 'collect_latest',
  quote_requirement: 'realtime_trade',
  executable: true,
  reason_code: null,
  estimate: { p95_seconds: 120, quota_cost: 1 },
  expires_at: '2026-11-17T06:05:00Z',
  sections: [
    { id: 'S1', title: '行情事实', required: true, availability: 'available', reason: null },
  ],
};
const task = {
  id: 'mock-task',
  skill_id: 'stock_analysis',
  subject: entity,
  status: 'COMPLETED',
  revision: 2,
  created_at: plan.as_of,
  deadline: plan.expires_at,
  as_of: plan.as_of,
  snapshot_mode: 'collect_latest',
  snapshot_as_of: '2026-11-17T06:00:05Z',
  steps: [],
  report_ref: { id: 'mock-report', version: 1 },
  reason_code: null,
  reason_message: null,
};
const freshness = {
  dataset_id: 'intraday_quote',
  provider: '模拟厂商',
  event_at: '2026-11-17T06:00:01Z',
  received_at: '2026-11-17T06:00:02Z',
  available_at: '2026-11-17T06:00:03Z',
  published_at: null,
  period: '2026-11-17',
  latency_mode: 'real_time',
  status: 'fresh',
  note: '模拟验收数据；交易状态：连续交易。',
};
const report = {
  id: 'mock-report',
  version: 1,
  task_id: task.id,
  title: '模拟行情简报',
  skill_id: 'stock_analysis',
  skill_version: plan.skill_version,
  subject: entity,
  as_of: plan.as_of,
  snapshot_mode: 'collect_latest',
  snapshot_as_of: task.snapshot_as_of,
  data_as_of: '模拟期间',
  result_kind: 'limited',
  synthetic: true,
  ai_generated: true,
  summary_claim_refs: ['C1'],
  sections: [
    {
      id: 'S1',
      title: '行情事实',
      status: 'ok',
      claim_refs: ['C1'],
      tables: [],
      charts: [],
      note: null,
    },
  ],
  claims: [
    {
      ref: 'C1',
      kind: 'fact',
      text: '模拟行情为 10.00 元。',
      evidence_refs: ['mock-evidence'],
      calculation_refs: [],
      scenario: null,
    },
  ],
  limitations: ['仅用于网页验收的模拟数据'],
  sources: [
    {
      dataset_id: freshness.dataset_id,
      provider: freshness.provider,
      updated_at: freshness.available_at,
    },
  ],
  source_freshness: [freshness],
  disclaimer: '模拟验收，不构成投资建议。',
  delivered_at: '2026-11-17T06:00:10Z',
};
const evidence = {
  id: 'mock-evidence',
  dataset_id: freshness.dataset_id,
  provider: freshness.provider,
  origin: 'exchange',
  source_tier: 'L1',
  title: '模拟行情证据',
  label: '最新价',
  value: '10.00',
  unit: '元',
  basis: '模拟连续交易行情',
  period: freshness.period,
  published_at: null,
  available_at: freshness.available_at,
  received_at: freshness.received_at,
  event_at: freshness.event_at,
  latency_mode: freshness.latency_mode,
  locator: null,
  excerpt: null,
  content_hash: 'mock-only',
};

test('0.4 selection, confirmation, frozen report and evidence render on desktop and mobile', async (t) => {
  const server = await createServer({ server: { host: '127.0.0.1', port: 0 }, logLevel: 'error' });
  await server.listen();
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  const browser = await chromium.launch({ headless: true });
  t.after(async () => {
    await browser.close();
    await server.close();
  });
  const appHTML = await server.transformIndexHtml(
    '/app',
    readFileSync(new URL('../app.html', import.meta.url), 'utf8')
  );

  for (const viewport of [
    { width: 1280, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await t.test(`${viewport.width}px`, async () => {
      const context = await browser.newContext({ viewport });
      const page = await context.newPage();
      page.setDefaultTimeout(10000);
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.route(`${origin}/app`, (route) =>
        route.fulfill({ contentType: 'text/html', body: appHTML })
      );
      await page.route(`${origin}/api/v1/**`, async (route) => {
        const request = route.request();
        const path = new URL(request.url()).pathname;
        let body;
        if (path === '/api/v1/me')
          body = {
            user_id: 'mock-user',
            workspace_id: 'mock-workspace',
            csrf_token: 'mock-csrf',
            persona: 'analyst',
            quota: {},
            onboarded: true,
            synthetic: true,
            display_name: '模拟验收',
            research_protocol: 'v0.4',
          };
        else if (path === '/api/v1/skills') body = { items: [] };
        else if (path === '/api/v1/research-plans' && request.method() === 'POST') {
          assert.equal(request.headers()['content-type'], media);
          assert.equal(request.headers().accept, media);
          assert.equal(request.postDataJSON().quote_requirement, 'realtime_trade');
          body = { kind: 'plan', plan, candidates: [], unsupported_reason: null, suggestions: [] };
        } else if (path === '/api/v1/research-plans/mock-plan') body = plan;
        else if (path === '/api/v1/research-tasks' && request.method() === 'POST') {
          assert.equal(request.headers()['content-type'], media);
          assert.equal(request.postDataJSON().plan_hash, plan.plan_hash);
          body = task;
        } else if (path === '/api/v1/research-tasks') body = { items: [] };
        else if (path === '/api/v1/research-tasks/mock-task') body = task;
        else if (path === '/api/v1/reports/mock-report/versions/1') body = report;
        else if (path === '/api/v1/evidence/mock-evidence') body = evidence;
        else throw new Error(`Unexpected test request ${path}`);
        await route.fulfill({ contentType: media, body: JSON.stringify(body) });
      });
      await page.goto(`${origin}/app`);
      await page.getByLabel('描述你想要的研究').fill('分析模拟验收公司');
      assert.equal(
        await page.getByRole('button', { name: '开始研究', exact: true }).isDisabled(),
        true
      );
      await page.getByRole('radio', { name: '交易中实时成交行情', exact: true }).check();
      await page.getByRole('button', { name: '开始研究', exact: true }).click();
      await page.getByRole('heading', { name: '研究计划：模拟验收公司', exact: true }).waitFor();
      assert.match(await page.locator('main').innerText(), /计划预览时间/);
      assert.match(
        await page.locator('main').innerText(),
        /若无法核实交易中实时成交条件，任务将弃权/
      );
      await page.getByRole('button', { name: '提交任务', exact: true }).click();
      await page.getByRole('link', { name: '查看报告', exact: true }).click();
      await page.getByRole('heading', { name: report.title, exact: true }).waitFor();
      assert.match(
        await page.locator('.report-head').innerText(),
        /快照截止 2026\/11\/17 14:00:05/
      );
      assert.match(
        await page.locator('#limits').innerText(),
        /14:00:01[\s\S]*14:00:02[\s\S]*14:00:03/
      );
      await page.getByRole('button', { name: '查看证据 1', exact: true }).click();
      await page.getByText('模拟行情证据', { exact: true }).waitFor();
      assert.match(
        await page.getByTestId('evidence-panel').innerText(),
        /14:00:01[\s\S]*14:00:02[\s\S]*14:00:03/
      );
      assert.deepEqual(errors, []);
      await context.close();
    });
  }
});
