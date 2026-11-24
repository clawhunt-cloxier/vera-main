import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { chromium } from '@playwright/test';
import { createServer } from 'vite';
import { testReportLayout } from './report-layout.mjs';
import { testWorkspaceTasks } from './workspace-tasks.mjs';

const media = 'application/vnd.vera.v0.4+json';
const observationMedia = 'application/vnd.vera.observation.v1+json';
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
const stockSkill = {
  id: 'stock_analysis',
  title: '个股分析',
  version: '1.0.1',
  available: true,
  subject_kinds: ['stock'],
  params_schema: {},
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
  locator: '原始归档 SHA-256 mock-only；价格 $.data.Values[0]；时间 $.data.Values[1]',
  excerpt: null,
  content_hash: 'mock-only',
};

test('0.4 latest-available confirmation, frozen report and evidence render on desktop and mobile', async (t) => {
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
  await testWorkspaceTasks(t, { browser, origin, appHTML, task, stockSkill, plan });
  await testReportLayout(t, { browser, origin, appHTML, report, evidence });

  for (const viewport of [
    { width: 1280, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await t.test(`${viewport.width}px`, async () => {
      const selectedPlan = {
        ...plan,
        quote_requirement: 'latest_available',
      };
      const selectedSource = freshness;
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
            realtime_trade_available: false,
          };
        else if (path === '/api/v1/skills') body = { items: [] };
        else if (path === '/api/v1/research-plans' && request.method() === 'POST') {
          assert.equal(request.headers()['content-type'], media);
          assert.equal(request.headers().accept, media);
          assert.equal(request.postDataJSON().quote_requirement, undefined);
          body = {
            kind: 'plan',
            plan: selectedPlan,
            candidates: [],
            unsupported_reason: null,
            suggestions: [],
          };
        } else if (path === '/api/v1/research-plans/mock-plan') body = selectedPlan;
        else if (path === '/api/v1/research-tasks' && request.method() === 'POST') {
          assert.equal(request.headers()['content-type'], media);
          assert.equal(request.postDataJSON().plan_hash, plan.plan_hash);
          body = task;
        } else if (path === '/api/v1/research-tasks' || path === '/api/v1/watchlist')
          body = { items: [] };
        else if (path === '/api/v1/research-tasks/mock-task') body = task;
        else if (path === '/api/v1/reports/mock-report/versions/1')
          body = { ...report, source_freshness: [selectedSource] };
        else if (path === '/api/v1/evidence/mock-evidence')
          body = {
            ...evidence,
            latency_mode: selectedSource.latency_mode,
            basis: selectedSource.note,
          };
        else throw new Error(`Unexpected test request ${path}`);
        await route.fulfill({ contentType: media, body: JSON.stringify(body) });
      });
      await page.goto(`${origin}/app`);
      await page.getByLabel('描述你想要的研究').fill('分析模拟验收公司');
      assert.equal(await page.getByText('行情要求', { exact: true }).count(), 0);
      await page.getByRole('button', { name: '开始研究', exact: true }).click();
      await page.getByRole('heading', { name: '研究计划：模拟验收公司', exact: true }).waitFor();
      assert.match(await page.locator('main').innerText(), /计划预览时间/);
      assert.match(
        await page.locator('main').innerText(),
        /最近可用行情可能是延迟、盘后或休市数据/
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
      assert.doesNotMatch(
        await page.getByTestId('evidence-panel').innerText(),
        /证据位置|SHA-256|\$\.data/
      );
      assert.deepEqual(errors, []);
      await context.close();
    });
  }

  for (const advertisedProtocol of ['v0.4', 'observation.v1']) {
    await t.test(
      `${advertisedProtocol}: old observation plan, cancellation and history`,
      async () => {
        const context = await browser.newContext({
          viewport:
            advertisedProtocol === 'v0.4'
              ? { width: 1280, height: 900 }
              : { width: 390, height: 844 },
        });
        const page = await context.newPage();
        page.setDefaultTimeout(10000);
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        const oldPlan = {
          ...plan,
          id: 'mock-old-plan',
          skill_version: '1.1.0-demo.2',
          quote_requirement: null,
        };
        const oldTask = {
          ...task,
          id: 'mock-old-task',
          status: 'QUEUED',
          snapshot_as_of: null,
          report_ref: null,
        };
        const oldReport = {
          ...report,
          id: 'mock-old-report',
          title: '模拟旧版观察简报',
          claims: [{ ...report.claims[0], text: '模拟成交额为 11.07 亿元。' }],
          sections: [
            {
              ...report.sections[0],
              tables: [
                {
                  title: 'Choice 日成交观察（非实时）',
                  columns: ['厂商返回日期', '成交量（手）', '成交额（万元）', '换手率（%）'],
                  rows: [['2026-10-08', '1150268.50', '110698.22', '0.3454']],
                  evidence_refs: ['mock-evidence'],
                },
              ],
            },
          ],
          skill_version: oldPlan.skill_version,
          source_freshness: undefined,
        };
        const fixedReport = {
          ...oldReport,
          id: 'mock-fixed-report',
          title: '模拟固定时点报告',
          snapshot_mode: 'fixed_legacy',
          snapshot_as_of: undefined,
          data_as_of: '模拟历史截止',
        };
        const oldReports = [oldReport, fixedReport];
        let cancelled = false;
        await page.route(`${origin}/app**`, (route) =>
          route.fulfill({ contentType: 'text/html', body: appHTML })
        );
        await page.route(`${origin}/api/v1/**`, async (route) => {
          const request = route.request();
          const path = new URL(request.url()).pathname;
          const accept = request.headers().accept;
          const selected =
            advertisedProtocol === 'v0.4' && accept.includes(media) ? media : observationMedia;
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
              research_protocol: advertisedProtocol,
            };
          else if (path === '/api/v1/skills') body = { items: [stockSkill] };
          else if (path === '/api/v1/watchlist') body = { items: [] };
          else if (path === '/api/v1/entities') {
            assert.equal(accept, observationMedia);
            body = { items: [oldPlan.subject] };
          } else if (path === '/api/v1/research-plans') {
            assert.equal(accept, observationMedia);
            assert.equal(request.headers()['content-type'], observationMedia);
            assert.equal(request.postDataJSON().quote_requirement, undefined);
            body = { kind: 'plan', plan: oldPlan };
          } else if (path === '/api/v1/research-tasks' && request.method() === 'GET')
            body = { items: [] };
          else if (path === '/api/v1/research-plans/mock-old-plan') body = oldPlan;
          else if (path === '/api/v1/research-tasks' && request.method() === 'POST') {
            assert.equal(accept, observationMedia);
            assert.equal(request.headers()['content-type'], observationMedia);
            assert.equal(request.postDataJSON().plan_hash, oldPlan.plan_hash);
            body = oldTask;
          } else if (path === '/api/v1/research-tasks/mock-old-task')
            body = { ...oldTask, status: cancelled ? 'CANCELLED' : 'QUEUED' };
          else if (path === '/api/v1/research-tasks/mock-old-task/cancel') {
            assert.equal(request.postDataJSON().expected_revision, oldTask.revision);
            assert.equal(request.headers()['content-type'], 'application/json');
            cancelled = true;
            await route.fulfill({ status: 204 });
            return;
          } else if (path === '/api/v1/reports') body = { items: oldReports };
          else if (path === '/api/v1/reports/mock-old-report/versions/1') body = oldReport;
          else if (path === '/api/v1/reports/mock-fixed-report/versions/1') body = fixedReport;
          else if (path === '/api/v1/evidence/mock-evidence')
            body = {
              ...evidence,
              dataset_id: 'mkt.daily_bar',
              label: '日成交观察（非完整日线）',
              value: null,
              unit: null,
              locator:
                '原始归档 SHA-256 mock-only；$.data.Values（与 Codes/Indicators/Dates 轴对应）',
              excerpt: '2026-10-08 成交量 1150268.50 手，成交额 110698.22 万元，换手率 0.3454%',
            };
          else throw new Error(`Unexpected compatibility request ${path}`);
          await route.fulfill({ contentType: selected, body: JSON.stringify(body) });
        });
        await page.goto(`${origin}/app/plan/mock-old-plan`);
        await page.getByRole('heading', { name: '研究计划：模拟验收公司', exact: true }).waitFor();
        assert.match(await page.locator('main').innerText(), /这是非实时简报/);
        assert.equal(await page.getByRole('radio').count(), 0);
        await page.getByRole('button', { name: '提交任务', exact: true }).click();
        await page.getByRole('heading', { name: '任务：模拟验收公司', exact: true }).waitFor();
        assert.match(await page.locator('main').innerText(), /数据快照尚未冻结/);
        page.once('dialog', (dialog) => dialog.accept());
        await page.getByRole('button', { name: '取消任务', exact: true }).click();
        await page.getByText('已取消', { exact: true }).waitFor();
        assert.equal(cancelled, true);

        await page.goto(`${origin}/app/reports`);
        await page.locator('a[href="/app/reports/mock-old-report"]').click();
        await page.getByRole('heading', { name: oldReport.title, exact: true }).waitFor();
        assert.match(await page.locator('.report-head').innerText(), /快照截止/);
        assert.match(await page.locator('#limits').innerText(), /模拟厂商/);
        const daily = page.getByRole('table', { name: 'Choice 日成交观察（非实时）' });
        assert.deepEqual(await daily.locator('tbody td').allTextContents(), [
          '1150268.50',
          '110698.22',
          '0.3454',
        ]);
        await page.getByRole('button', { name: '查看证据 1', exact: true }).click();
        const excerpt = page.getByTestId('evidence-panel').locator('blockquote');
        await excerpt.waitFor();
        const [volume, amount, turnover] = await daily.locator('tbody td').allTextContents();
        assert.equal(
          await excerpt.innerText(),
          `2026-10-08 成交量 ${volume} 手，成交额 ${amount} 万元，换手率 ${turnover}%`
        );
        assert.doesNotMatch(
          await page.getByTestId('evidence-panel').innerText(),
          /证据位置|SHA-256|\$\.data|Codes\/Indicators\/Dates|厂商百分数|内部比例|亿元/
        );
        await page.getByRole('button', { name: '关闭证据' }).click();
        await page.getByRole('link', { name: '返回报告列表' }).click();
        await page.locator('a[href="/app/reports/mock-fixed-report"]').click();
        await page.getByRole('heading', { name: fixedReport.title, exact: true }).waitFor();
        assert.match(await page.locator('.report-head').innerText(), /数据截止 模拟历史截止/);
        assert.deepEqual(errors, []);
        await context.close();
      }
    );
  }

  await t.test('blocked annual research can preview capital flow for the same stock', async () => {
    const context = await browser.newContext();
    try {
      const page = await context.newPage();
      const button = page.getByRole('button', { name: '查看资金动向计划' });
      const body = page.locator('main');
      let entityCan = true,
        registryCan = true,
        previewCan = true,
        originSkill = 'stock_analysis';
      let previews = 0,
        tasks = 0;
      const me = { csrf_token: 'test', onboarded: true, synthetic: false, quota: {} };
      const fixed = (id, flow) => ({
        ...plan,
        id,
        skill_id: flow ? 'capital_flow' : originSkill,
        skill_version: flow ? '1.0.1' : '1.0.2',
        subject: {
          ...entity,
          skills: [{ skill_id: 'capital_flow', available: entityCan, reason: null }],
        },
        executable: flow && previewCan,
        reason_code: flow && previewCan ? null : 'REQUIRED_SECTION_UNAVAILABLE',
        snapshot_mode: 'fixed_legacy',
        quote_requirement: null,
        sections: [
          {
            id: 'S2',
            title: flow ? '行情与交易' : '公司概况',
            required: true,
            availability: flow && previewCan ? 'available' : 'unavailable',
            reason: flow
              ? previewCan
                ? null
                : '缺少近期融资余额'
              : '缺少最近年度财报可引用的主营业务原文',
          },
          { id: 'S5', title: '可选章节', required: false, availability: 'available', reason: null },
        ],
      });
      await page.route(origin + '/app**', (route) =>
        route.fulfill({ contentType: 'text/html', body: appHTML })
      );
      await page.route(origin + '/api/v1/**', async (route) => {
        const req = route.request(),
          path = new URL(req.url()).pathname.slice('/api/v1/'.length);
        let result;
        if (path === 'me') result = me;
        else if (path === 'skills')
          result = {
            items: [
              { ...stockSkill, id: 'capital_flow', title: '资金动向', available: registryCan },
            ],
          };
        else if (path === 'research-plans' && req.method() === 'POST') {
          previews++;
          assert.equal(req.headers()['content-type'], 'application/json');
          assert.deepEqual(req.postDataJSON(), {
            skill_id: 'capital_flow',
            subject_id: entity.id,
            params: {},
          });
          result = { kind: 'plan', plan: fixed('alternative', true) };
        } else if (path === 'research-plans/blocked') result = fixed('blocked', false);
        else if (path === 'research-plans/alternative') result = fixed('alternative', true);
        else if (path === 'research-tasks' && req.method() === 'POST') {
          tasks++;
          result = task;
        } else throw new Error('Unexpected alternative plan request ' + path);
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify(result) });
      });
      for (const originType of ['stock_analysis', 'deep_research']) {
        originSkill = originType;
        await page.goto(origin + '/app/plan/blocked');
        await button.waitFor();
        assert.equal(await page.getByRole('button', { name: '提交任务' }).isDisabled(), true);
        assert.match(await body.innerText(), /缺少最近年度财报可引用的主营业务原文/);
        await page.getByRole('checkbox', { name: /S5/ }).uncheck();
        assert.equal(previews, originType === 'stock_analysis' ? 0 : 1);
        previewCan = originType === 'stock_analysis';
        await button.click();
        await page.waitForURL('**/app/plan/alternative');
        await page.getByText('资金动向', { exact: true }).waitFor();
        assert.equal(await page.getByRole('checkbox', { name: /S5/ }).isChecked(), true);
        assert.equal(await page.getByRole('button', { name: '提交任务' }).isEnabled(), previewCan);
        if (!previewCan) assert.match(await body.innerText(), /缺少近期融资余额/);
        assert.equal(await button.count(), 0);
        assert.equal(tasks, 0);
      }
      for (const [subjectAllowed, skillAllowed] of [
        [false, true],
        [true, false],
      ]) {
        entityCan = subjectAllowed;
        registryCan = skillAllowed;
        const registryResponse = subjectAllowed
          ? page.waitForResponse((r) => r.url().endsWith('/api/v1/skills'))
          : null;
        await page.goto(origin + '/app/plan/blocked');
        await page.getByText('必需章节缺少可用数据，本次计划无法提交。').waitFor();
        if (registryResponse) await registryResponse;
        assert.equal(await button.count(), 0);
      }
      assert.equal(previews, 2);
    } finally {
      await context.close();
    }
  });

  for (const viewport of [
    { width: 1280, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await t.test(`vendor-neutral workspace uses ordinary JSON at ${viewport.width}px`, async () => {
      const context = await browser.newContext({ viewport });
      try {
        const page = await context.newPage();
        page.setDefaultTimeout(6000);
        const json = 'application/json';
        const closing = ['stock_analysis', 'capital_flow', 'deep_research'].map((id, index) => ({
          ...stockSkill,
          id,
          title: ['个股分析', '资金动向', '深度研究'][index],
        }));
        let expected = closing[0];
        let freeText = false;
        let requests = 0;
        const sources = ['Choice', 'TuShare'].map((provider) => ({
          dataset_id: 'mkt.daily_bar',
          provider,
          updated_at: plan.as_of,
        }));
        const currentPlan = () => ({
          ...plan,
          skill_id: expected.id,
          skill_version: '1.0.4',
          quote_requirement: undefined,
          snapshot_mode: undefined,
          sections: [
            ...plan.sections,
            {
              id: 'S2',
              title: '财务数据',
              required: false,
              availability: 'limited',
              reason: '只有经核验的财务指标。',
            },
            {
              id: 'S3',
              title: '其他披露',
              required: false,
              availability: 'unavailable',
              reason: '暂无数据。',
            },
          ],
        });
        await page.route(origin + '/app**', (route) =>
          route.fulfill({ contentType: 'text/html', body: appHTML })
        );
        await page.route(origin + '/api/v1/**', async (route) => {
          const req = route.request();
          const path = new URL(req.url()).pathname.slice('/api/v1/'.length);
          const accept = req.headers().accept;
          const responseType = accept.includes(',') ? media : accept;
          let body;
          if (path === 'me')
            body = {
              onboarded: true,
              csrf_token: 'test-csrf',
              research_protocol: 'v0.4',
              realtime_trade_available: true,
              closing_research_available: true,
            };
          else if (path === 'skills') {
            assert.equal(accept, json);
            body = { items: closing };
          } else if (path === 'watchlist')
            body = { items: [{ id: 'mock-watch', entity, snapshot: null }] };
          else if (path === 'entities') {
            assert.equal(accept, json);
            body = { items: [entity] };
          } else if (path === `research-subjects/${entity.id}/prepare`) {
            assert.equal(req.method(), 'POST');
            assert.equal(accept, json);
            body = { status: 'ready' };
          } else if (path === 'research-plans') {
            assert.equal(accept, json);
            assert.equal(req.headers()['content-type'], json);
            assert.deepEqual(req.postDataJSON(), {
              ...(freeText
                ? { text: '研究这只股票' }
                : { skill_id: expected.id, subject_id: entity.id, params: {} }),
            });
            requests++;
            body = { kind: 'plan', plan: currentPlan() };
          } else if (path === 'research-plans/mock-plan') {
            assert.equal(responseType, media);
            body = {
              ...currentPlan(),
              snapshot_mode: 'fixed_legacy',
              quote_requirement: 'latest_available',
            };
          } else if (path === 'research-tasks' && req.method() === 'POST') {
            assert.equal(accept, json);
            assert.equal(req.headers()['content-type'], json);
            assert.deepEqual(req.postDataJSON().disabled_sections, []);
            body = task;
          } else if (path === 'research-tasks') body = { items: [] };
          else if (path === 'research-tasks/mock-task') {
            assert.equal(accept, media);
            body = {
              ...task,
              skill_id: expected.id,
              snapshot_mode: 'fixed_legacy',
              snapshot_as_of: null,
            };
          } else if (path === 'reports/mock-report/versions/1')
            body = {
              ...report,
              sources,
              snapshot_mode: 'fixed_legacy',
              snapshot_as_of: null,
              skill_version: '1.0.4',
              source_freshness: undefined,
              claims: [{ ...report.claims[0], evidence_refs: ['choice', 'tushare'] }],
            };
          else if (path.startsWith('evidence/')) {
            const provider = path.endsWith('choice') ? 'Choice' : 'TuShare';
            body = { ...evidence, id: provider, provider, excerpt: provider + ' 模拟证据' };
          } else throw new Error('Unexpected combined request ' + path);
          await route.fulfill({ contentType: responseType, body: JSON.stringify(body) });
        });
        const assertNeutralAndFits = async () => {
          assert.equal(await page.getByLabel('研究数据').count(), 0);
          assert.equal(await page.getByRole('group', { name: /行情要求/ }).count(), 0);
          assert.doesNotMatch(await page.locator('main').innerText(), /choice|tushare/i);
          assert.equal(
            await page.locator('html').evaluate((element) => element.scrollWidth),
            viewport.width
          );
        };
        for (const entry of closing) {
          expected = entry;
          await page.goto(origin + '/app');
          await page.getByRole('region', { name: '发起研究', exact: true }).waitFor();
          for (const skill of closing)
            await page.getByRole('link', { name: skill.title, exact: true }).waitFor();
          await assertNeutralAndFits();
          await page.getByRole('link', { name: expected.title, exact: true }).click();
          await page.getByRole('heading', { name: expected.title, exact: true }).waitFor();
          await page.getByRole('combobox', { name: '搜索研究对象' }).fill(entity.symbol);
          await page.getByRole('option', { name: new RegExp(entity.name) }).click();
          await page.getByRole('button', { name: '发起研究' }).click();
          await page.getByText('1 个受限', { exact: true }).waitFor();
          if (entry.id === 'stock_analysis') {
            await page.reload();
            await page.getByText('1 个受限', { exact: true }).waitFor();
          }
          assert.equal(await page.getByRole('checkbox', { name: /财务数据/ }).isEnabled(), true);
          assert.equal(await page.getByRole('checkbox', { name: /其他披露/ }).isDisabled(), true);
          await assertNeutralAndFits();
          await page.getByRole('button', { name: '提交任务' }).click();
          await page.getByRole('link', { name: '查看报告' }).waitFor();
        }
        expected = closing[0];
        for (const oldRoute of ['daily-observation', 'choice-quote']) {
          await page.goto(origin + '/app/quick/' + oldRoute);
          await page.waitForURL(origin + '/app/quick/stock_analysis');
          await page.getByRole('heading', { name: expected.title, exact: true }).waitFor();
        }
        freeText = true;
        await page.goto(origin + '/app');
        await page.getByLabel('描述你想要的研究').fill('研究这只股票');
        await page.getByRole('button', { name: '开始研究' }).click();
        await page.getByRole('heading', { name: '研究计划：' + entity.name }).waitFor();
        freeText = false;
        await page.getByRole('button', { name: '提交任务' }).click();
        await page.getByRole('link', { name: '查看报告' }).click();
        await page.getByRole('heading', { name: report.title }).waitFor();
        assert.match(await page.locator('#limits').innerText(), /Choice · mkt.daily_bar/);
        assert.match(await page.locator('#limits').innerText(), /TuShare · mkt.daily_bar/);
        await page.getByRole('button', { name: '查看证据 1' }).click();
        for (const provider of ['Choice', 'TuShare'])
          await page.getByText(provider + ' 模拟证据', { exact: true }).waitFor();
        assert.equal(
          await page.getByRole('link', { name: '下载 PDF' }).getAttribute('href'),
          '/api/v1/reports/mock-report/versions/1/pdf'
        );
        await page.goto(origin + '/app/watchlist');
        await page.getByRole('button', { name: '分析' }).click();
        await page.getByRole('heading', { name: '研究计划：' + entity.name }).waitFor();
        assert.equal(requests, 5);
      } finally {
        await context.close();
      }
    });
  }

  await t.test(
    'stock picker defaults to full research and offers a separate short brief',
    async () => {
      const context = await browser.newContext();
      try {
        const page = await context.newPage();
        page.setDefaultTimeout(6000);
        const json = 'application/json';
        const stocks = [
          { ...entity, id: 'maotai', name: '贵州茅台', symbol: '600519.SH' },
          { ...entity, id: 'mobile', name: '中国移动', symbol: '600941.SH' },
          { ...entity, id: 'petro', name: '中国石油', symbol: '601857.SH' },
        ];
        let selected, selectedMedia;
        const preparedStocks = [];
        const searches = [];
        const confirmations = [];
        await page.route(origin + '/app**', (route) =>
          route.fulfill({ contentType: 'text/html', body: appHTML })
        );
        await page.route(origin + '/api/v1/**', async (route) => {
          const req = route.request();
          const { pathname, searchParams } = new URL(req.url());
          const path = pathname.slice('/api/v1/'.length);
          const accept = req.headers().accept;
          let body;
          if (path === 'me')
            body = {
              onboarded: true,
              csrf_token: 'mock-csrf',
              closing_research_available: true,
              research_protocol: 'v0.4',
            };
          else if (path === 'skills') body = { items: [stockSkill] };
          else if (path === 'entities') {
            const q = searchParams.get('q');
            const item = stocks.find((stock) => stock.name === q || stock.symbol === q);
            searches.push([q, accept]);
            assert.equal(searchParams.get('kind'), 'stock');
            // The registered result is a near hit for two names. It must not
            // hide the exact discovery hit or be duplicated in the list.
            body = { items: accept === json ? [stocks[0]] : [stocks[0], item] };
          } else if (/^research-subjects\/(petro|maotai)\/prepare$/.test(path)) {
            assert.equal(accept, json);
            preparedStocks.push(path.split('/')[1]);
            body = { status: 'ready' };
          } else if (path === 'research-plans') {
            selected = stocks.find((stock) => stock.id === req.postDataJSON().subject_id);
            assert.equal(accept, selectedMedia);
            assert.equal(req.headers()['content-type'], selectedMedia);
            body = { kind: 'plan', plan: { ...plan, subject: selected } };
          } else if (path === 'research-plans/mock-plan') {
            body = {
              ...plan,
              subject: selected,
              snapshot_mode: selectedMedia === json ? 'fixed_legacy' : 'collect_latest',
              quote_requirement: undefined,
            };
          } else if (path === 'research-tasks' && req.method() === 'POST') {
            assert.equal(accept, selectedMedia);
            assert.equal(req.headers()['content-type'], selectedMedia);
            confirmations.push(selected.id);
            body = task;
          } else if (path === 'research-tasks/mock-task') body = { ...task, subject: selected };
          else if (path === 'research-tasks') body = { items: [] };
          else throw new Error('Unexpected request ' + path);
          await route.fulfill({ contentType: accept.includes(',') ? media : accept, json: body });
        });
        for (const [index, stock] of stocks.entries()) {
          selectedMedia = index === 1 ? observationMedia : json;
          await page.goto(origin + '/app/quick/stock_analysis');
          const input = page.getByRole('combobox');
          const create = page.getByRole('button', { name: '发起研究' });
          if (stock.id === 'petro') {
            await input.fill(stocks[0].symbol);
            await page.getByRole('option', { name: /贵州茅台/ }).click();
            assert.ok(await create.isEnabled());
          }
          await input.fill(stock.name);
          assert.ok(await create.isDisabled());
          await page.getByRole('option', { name: new RegExp(stock.name) }).waitFor();
          const options = page.getByRole('option');
          assert.match(await options.first().innerText(), new RegExp(stock.name));
          assert.equal(await options.count(), stock.id === 'maotai' ? 1 : 2);
          await input.press('Enter');
          const brief = page.getByRole('button', { name: '查看成交简报', exact: true });
          if (stock.id === 'maotai') assert.equal(await brief.count(), 0);
          else await brief.waitFor();
          assert.doesNotMatch(await page.locator('main').innerText(), /choice|tushare/i);
          if (selectedMedia === observationMedia) await brief.click();
          else await create.click();
          await page.getByRole('heading', { name: '研究计划：' + stock.name }).waitFor();
          await page.getByRole('button', { name: '提交任务' }).click();
          await page.getByRole('link', { name: '查看报告' }).waitFor();
        }
        assert.deepEqual(preparedStocks, ['maotai', 'petro']);
        assert.deepEqual(searches, [
          ['贵州茅台', json],
          ['中国移动', json],
          ['中国移动', observationMedia],
          ['600519.SH', json],
          ['中国石油', json],
          ['中国石油', observationMedia],
        ]);
        assert.deepEqual(
          confirmations,
          stocks.map((s) => s.id)
        );
      } finally {
        await context.close();
      }
    }
  );

  await t.test('capital flow and deep research prepare new A-shares before planning', async () => {
    const context = await browser.newContext();
    try {
      const page = await context.newPage();
      const json = 'application/json';
      const stocks = [
        { ...entity, id: 'mobile', name: '中国移动', symbol: '600941.SH' },
        { ...entity, id: 'petro', name: '中国石油', symbol: '601857.SH' },
      ];
      const skills = [
        { ...stockSkill, id: 'capital_flow', title: '资金动向' },
        { ...stockSkill, id: 'deep_research', title: '深度研究' },
      ];
      let prepares = [];
      let searches = [];
      let plans = [];
      let status = 'pending';
      let selected;
      await page.route(origin + '/app**', (route) =>
        route.fulfill({ contentType: 'text/html', body: appHTML })
      );
      await page.route(origin + '/api/v1/**', async (route) => {
        const req = route.request();
        const { pathname, searchParams } = new URL(req.url());
        const path = pathname.slice('/api/v1/'.length);
        const headers = req.headers();
        let body;
        if (path === 'me') {
          body = {
            onboarded: true,
            csrf_token: 'mock',
            closing_research_available: true,
            research_protocol: 'v0.4',
          };
        } else if (path === 'skills') {
          body = { items: skills };
        } else if (path === 'entities') {
          const q = searchParams.get('q');
          searches.push([q, headers.accept]);
          body = {
            items:
              headers.accept === json
                ? []
                : stocks.filter((stock) => stock.name === q || stock.symbol.startsWith(q)),
          };
        } else if (path.endsWith('/prepare')) {
          assert.equal(headers.accept, json);
          assert.equal(headers['x-csrf-token'], 'mock');
          assert.equal(req.method(), 'POST');
          assert.deepEqual(req.postDataJSON(), {});
          prepares.push(path.split('/')[1]);
          if (status === 'limited') {
            await route.fulfill({
              status: 429,
              headers: { 'retry-after': '5' },
              contentType: json,
              json: { error: { code: 'RATE_LIMITED', message: '测试限频', retryable: true } },
            });
            return;
          }
          body = {
            status,
            message: {
              pending: '正在同步中国移动的 TuShare 研究数据，完成后将打开计划。',
              failed: 'TuShare 账号缺少数据权限',
            }[status],
          };
        } else if (path === 'research-plans') {
          assert.equal(headers.accept, json);
          assert.equal(headers['content-type'], json);
          const { skill_id, subject_id } = req.postDataJSON();
          plans.push([skill_id, subject_id]);
          selected = stocks.find((stock) => stock.id === subject_id);
          body = { kind: 'plan', plan: { ...plan, subject: selected } };
        } else if (path === 'research-plans/mock-plan') {
          body = {
            ...plan,
            subject: selected,
            snapshot_mode: 'fixed_legacy',
            quote_requirement: undefined,
          };
        } else throw new Error('Unexpected research preparation request ' + path);
        await route.fulfill({
          contentType: headers.accept.includes(',') ? media : headers.accept,
          json: body,
        });
      });
      for (const skill of skills) {
        prepares = [];
        searches = [];
        plans = [];
        status = 'pending';
        await page.goto(origin + '/app/quick/' + skill.id);
        const input = page.getByRole('combobox', { name: '搜索研究对象' });
        const start = page.getByRole('button', { name: '发起研究' });
        await input.fill(stocks[0].name);
        await page.getByRole('option', { name: /中国移动/ }).click();
        assert.deepEqual(prepares, []);
        await start.click();
        await page
          .getByRole('status')
          .filter({ hasText: /正在为中国移动准备研究计划/ })
          .waitFor();
        assert.doesNotMatch(await page.locator('main').innerText(), /choice|tushare/i);
        assert.deepEqual(plans, []);
        if (skill.id === 'deep_research') {
          await input.fill('601857');
          await page.getByRole('option', { name: /中国石油/ }).click();
          status = 'failed';
          await start.click();
          await page.getByRole('alert').filter({ hasText: /权限/ }).waitFor();
          assert.doesNotMatch(await page.locator('main').innerText(), /choice|tushare/i);
          assert.deepEqual(plans, []);
          status = 'ready';
          await start.click();
        } else {
          status = 'limited';
          await page.getByRole('status').filter({ hasText: '自动重试' }).waitFor();
          assert.deepEqual(plans, []);
          status = 'ready';
        }
        const stock = stocks[skill.id === 'capital_flow' ? 0 : 1];
        await page
          .getByRole('heading', { name: '研究计划：' + stock.name })
          .waitFor({ timeout: 9000 });
        assert.deepEqual(plans, [[skill.id, stock.id]]);
        assert.deepEqual(
          prepares,
          skill.id === 'capital_flow'
            ? ['mobile', 'mobile', 'mobile']
            : ['mobile', 'petro', 'petro']
        );
        assert.deepEqual(
          searches,
          [stocks[0].name, ...(skill.id === 'deep_research' ? ['601857'] : [])].flatMap((q) => [
            [q, json],
            [q, observationMedia],
          ])
        );
      }
    } finally {
      await context.close();
    }
  });

  await t.test('quick research distinguishes loading from an unavailable skill', async () => {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
    const page = await context.newPage();
    const { promise, resolve } = Promise.withResolvers();
    try {
      await page.route(origin + '/app**', (route) =>
        route.fulfill({ contentType: 'text/html', body: appHTML })
      );
      await page.route(origin + '/api/v1/**', async (route) => {
        const path = new URL(route.request().url()).pathname;
        const data =
          path === '/api/v1/me'
            ? { onboarded: true, csrf_token: 'test-csrf', synthetic: true }
            : { items: [{ ...stockSkill, available: false }] };
        assert.match(path, /\/api\/v1\/(me|skills)$/);
        if (path.endsWith('/skills')) await promise;
        await route.fulfill({ contentType: 'application/json', json: data });
      });
      await page.goto(origin + '/app/quick/stock_analysis');
      await page.getByText('加载研究能力…', { exact: true }).waitFor();
      assert.equal(await page.getByText('当前部署未开放这项研究。', { exact: true }).count(), 0);
      resolve();
      await page.getByText('当前部署未开放这项研究。', { exact: true }).waitFor();
      assert.equal(await page.getByRole('button', { name: '发起研究', exact: true }).count(), 0);
    } finally {
      resolve();
      await context.close();
    }
  });

  for (const synthetic of [false, true]) {
    await t.test(synthetic ? 'fixture industry comparison' : 'TuShare close research', async () => {
      const context = await browser.newContext();
      try {
        const page = await context.newPage();
        page.setDefaultTimeout(10000);
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        const ids = ['stock_analysis', 'capital_flow', 'deep_research'];
        const titles = ['个股分析', '资金动向', '深度研究'];
        const skills = ids.map((id, i) => ({
          ...stockSkill,
          id,
          title: titles[i],
          version: synthetic ? '1.0.0' : '1.0.1',
          subject_kinds: synthetic && id !== 'stock_analysis' ? ['stock', 'industry'] : ['stock'],
          params_schema:
            id === 'deep_research'
              ? {
                  properties: {
                    mode: {
                      enum: synthetic ? ['company', 'industry', 'company_industry'] : ['company'],
                    },
                  },
                }
              : {},
        }));
        const refs = Array.from({ length: 30 }, (_, i) => 'source-' + i);
        const requested = new Set();
        let skill, submitted;
        const fixedPlan = () => ({
          ...plan,
          skill_id: skill,
          skill_version: skills[0].version,
          snapshot_mode: 'fixed_legacy',
          quote_requirement: undefined,
        });
        await page.route(origin + '/app**', (route) =>
          route.fulfill({ contentType: 'text/html', body: appHTML })
        );
        await page.route(origin + '/api/v1/**', async (route) => {
          const req = route.request();
          const path = new URL(req.url()).pathname.replace('/api/v1/', '');
          let data;
          if (path === 'me')
            data = { csrf_token: 'mock-csrf', onboarded: true, synthetic, quota: {} };
          else if (path === 'skills') data = { items: skills };
          else if (path === 'entities') data = { items: [entity] };
          else if (
            path === 'research-plans' ||
            (path === 'research-tasks' && req.method() === 'POST')
          ) {
            assert.equal(req.headers().accept, 'application/json');
            assert.equal(req.headers()['content-type'], 'application/json');
            if (path === 'research-plans') {
              submitted = req.postDataJSON();
              skill = submitted.skill_id;
              data = { kind: 'plan', plan: fixedPlan() };
            } else {
              assert.equal(req.postDataJSON().plan_hash, plan.plan_hash);
              data = task;
            }
          } else if (path === 'research-tasks' || path === 'watchlist') data = { items: [] };
          else if (path === 'research-plans/mock-plan') data = fixedPlan();
          else if (path === 'research-tasks/mock-task')
            data = { ...task, skill_id: skill, snapshot_mode: 'fixed_legacy' };
          else if (path === 'reports/mock-report/versions/1')
            data = {
              ...report,
              synthetic,
              skill_id: skill,
              skill_version: skills[0].version,
              title: '模拟公司财务分析',
              snapshot_mode: 'fixed_legacy',
              source_freshness: undefined,
              sections: [
                {
                  ...report.sections[0],
                  title: '财务及估值',
                  tables: [
                    { title: '营业收入', columns: ['期间', '亿元'], rows: [['2025', '10.0']] },
                  ],
                  charts: [{ series_id: 'chart-a', title: '收入趋势' }],
                },
              ],
              claims: [{ ...report.claims[0], evidence_refs: refs, calculation_refs: ['calc-a'] }],
            };
          else if (path === 'reports/mock-report/versions/1/series/chart-a')
            data = {
              kind: 'line',
              unit: '亿元',
              series: [{ name: '收盘资料', points: [['2025', '10.0']] }],
            };
          else if (path.startsWith('evidence/')) {
            const id = path.slice('evidence/'.length);
            requested.add(id);
            data = {
              ...evidence,
              id,
              dataset_id: 'doc.periodic_text',
              provider: '模拟 TuShare 上游',
              title: id,
              locator: '模拟年报第 10 页',
              excerpt: '模拟原文 ' + id,
            };
          } else if (path === 'calculations/calc-a')
            data = {
              formula_id: 'F13.excess_return',
              formula_version: '1.1.1',
              result: '0.02',
              unit: 'pp',
              applicability: 'APPLICABLE',
              inputs: [
                { evidence_id: refs[0], value: '10', unit: 'CNY/share' },
                { evidence_id: refs[1], value: '10.4', unit: 'CNY/share' },
                { evidence_id: refs[2], value: '4000', unit: 'index_point' },
                { evidence_id: refs[3], value: '4080', unit: 'index_point' },
              ],
            };
          else throw new Error('Unexpected close research request ' + path);
          await route.fulfill({ contentType: 'application/json', body: JSON.stringify(data) });
        });
        for (const expected of synthetic ? ['deep_research'] : ids) {
          await page.goto(origin + '/app');
          for (const title of titles)
            await page.getByRole('link', { name: title, exact: true }).waitFor();
          assert.equal(await page.getByRole('link', { name: 'Choice 日成交简报' }).count(), 0);
          await page
            .getByRole('link', { name: titles[ids.indexOf(expected)], exact: true })
            .click();
          await page.getByRole('combobox', { name: '搜索研究对象' }).fill(entity.symbol);
          await page.getByRole('option', { name: new RegExp(entity.name) }).click();
          if (expected === 'deep_research') {
            const modes = page.getByLabel('模式');
            assert.deepEqual(
              await modes.locator('option').allTextContents(),
              synthetic ? ['公司深度研究', '公司 + 所在行业对比'] : ['公司深度研究']
            );
            if (synthetic) await modes.selectOption('company_industry');
          }
          await page.getByRole('button', { name: '发起研究' }).click();
          await page.getByRole('heading', { name: '研究计划：' + entity.name }).waitFor();
          assert.deepEqual(submitted, {
            skill_id: expected,
            subject_id: entity.id,
            params: synthetic ? { mode: 'company_industry' } : {},
          });
        }
        if (!synthetic) {
          await page.getByRole('button', { name: '提交任务' }).click();
          await page.getByRole('link', { name: '查看报告' }).click();
          await page.getByRole('heading', { name: '模拟公司财务分析' }).waitFor();
          assert.equal(
            await page.getByRole('table', { name: '营业收入' }).locator('td').innerText(),
            '10.0'
          );
          await page.locator('.chart-box svg').waitFor();
          await page.getByRole('button', { name: '查看证据 1', exact: true }).click();
          await page.getByText('F13.excess_return', { exact: true }).waitFor();
          await page.getByText(/结果：0\.02\s*（比例差，小数）/).waitFor();
          await page.getByText('输入 4 项：10 元/股，10.4 元/股，4000 点，4080 点').waitFor();
          await page.getByText('模拟原文 source-11', { exact: true }).waitFor();
          assert.deepEqual([...requested].sort(), refs.slice(0, 12).sort());
          await page.getByRole('button', { name: '下一页' }).click();
          await page.getByText('模拟原文 source-23', { exact: true }).waitFor();
          assert.deepEqual([...requested].sort(), refs.slice(0, 24).sort());
          assert.equal(
            await page.getByRole('link', { name: '下载 PDF' }).getAttribute('href'),
            '/api/v1/reports/mock-report/versions/1/pdf'
          );
          await page.goto(origin + '/app/quick/daily-observation');
          await page.waitForURL(origin + '/app/quick/stock_analysis');
          await page.getByRole('combobox', { name: '搜索研究对象' }).waitFor();
          assert.equal(await page.getByText('当前部署未开放这项研究。').count(), 0);
        }
        assert.deepEqual(errors, []);
      } finally {
        await context.close();
      }
    });
  }
});
