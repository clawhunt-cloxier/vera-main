import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { chromium } from '@playwright/test';
import { createServer } from 'vite';

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
      const allowRealtime = viewport.width === 1280;
      const selectedPlan = {
        ...plan,
        quote_requirement: allowRealtime ? 'realtime_trade' : 'latest_available',
      };
      const selectedSource = allowRealtime
        ? freshness
        : { ...freshness, latency_mode: 'unknown', status: 'unknown', note: '交易状态：未确认' };
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
            realtime_trade_available: allowRealtime,
          };
        else if (path === '/api/v1/skills') body = { items: [] };
        else if (path === '/api/v1/research-plans' && request.method() === 'POST') {
          assert.equal(request.headers()['content-type'], media);
          assert.equal(request.headers().accept, media);
          assert.equal(request.postDataJSON().quote_requirement, selectedPlan.quote_requirement);
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
        } else if (path === '/api/v1/research-tasks') body = { items: [] };
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
      assert.equal(
        await page.getByRole('radio', { name: '最近可用行情', exact: true }).isChecked(),
        true
      );
      const strict = page.getByRole('radio', { name: '交易中实时成交行情', exact: true });
      if (allowRealtime) await strict.check();
      else assert.equal(await strict.isDisabled(), true);
      await page.getByRole('button', { name: '开始研究', exact: true }).click();
      await page.getByRole('heading', { name: '研究计划：模拟验收公司', exact: true }).waitFor();
      assert.match(await page.locator('main').innerText(), /计划预览时间/);
      assert.match(
        await page.locator('main').innerText(),
        allowRealtime
          ? /若无法核实交易中实时成交条件，任务将弃权/
          : /最近可用行情可能是延迟、盘后或休市数据/
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
      if (!allowRealtime) assert.match(await page.locator('#limits').innerText(), /延迟性质未知/);
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
        if (advertisedProtocol === 'v0.4') {
          await page.goto(`${origin}/app`);
          await page.getByRole('link', { name: 'Choice 日成交简报' }).click();
          await page.getByRole('combobox').fill(oldPlan.subject.symbol);
          await page.getByRole('option').first().click();
          await page.getByRole('button', { name: '发起研究' }).click();
          await page.waitForURL('**/app/plan/mock-old-plan');
          await page.reload();
        } else await page.goto(`${origin}/app/plan/mock-old-plan`);
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
        await page.getByRole('link', { name: '← 返回报告列表' }).click();
        await page.locator('a[href="/app/reports/mock-fixed-report"]').click();
        await page.getByRole('heading', { name: fixedReport.title, exact: true }).waitFor();
        assert.match(await page.locator('.report-head').innerText(), /数据截止 模拟历史截止/);
        assert.deepEqual(errors, []);
        await context.close();
      }
    );
  }

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
          } else if (path === 'research-tasks') data = { items: [] };
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
              formula_id: 'F01',
              formula_version: '1.0',
              result: '10.0',
              unit: '亿元',
              applicability: 'APPLICABLE',
              inputs: [],
            };
          else throw new Error('Unexpected close research request ' + path);
          await route.fulfill({ contentType: 'application/json', body: JSON.stringify(data) });
        });
        for (const expected of synthetic ? ['deep_research'] : ids) {
          await page.goto(origin + '/app');
          for (const title of titles)
            await page.getByRole('link', { name: title, exact: true }).waitFor();
          assert.equal(await page.getByRole('link', { name: 'Choice 日成交简报' }).count(), 0);
          if (!synthetic) await page.getByText(/行情采用收盘数据/).waitFor();
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
          await page.getByText('F01', { exact: true }).waitFor();
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
          await page.getByText('当前部署未开放这项研究。').waitFor();
          assert.equal(await page.getByRole('button', { name: '发起研究' }).isDisabled(), true);
        }
        assert.deepEqual(errors, []);
      } finally {
        await context.close();
      }
    });
  }
});
