import assert from 'node:assert/strict';

// Use recognizable internal labels: a table with translated statuses would still leak work details.
const steps = ['PENDING', 'RUNNING', 'SKIPPED', 'FAILED', 'SUCCEEDED'].map((status) => ({
  status,
  key: 'internal_' + status.toLowerCase(),
  label: '模拟内部步骤 ' + status,
}));
const workerText =
  /模拟内部步骤|PENDD?ING|RUNNING|SKIPPED|FAILED|SUCCEEDED|QUEUED|COLLECTING|ANALYZING|ASSEMBLING|DELIVERING|CANCEL_REQUESTED|Choice|TuShare/i;
const activePhases = ['QUEUED', 'COLLECTING', 'ANALYZING', 'ASSEMBLING', 'DELIVERING'];
const outcomes = {
  COMPLETED: '已完成',
  ABSTAINED: '未生成报告',
  BUDGET_EXHAUSTED: '研究未能完成',
  CANCELLED: '已取消',
  BLOCKED: '暂时无法执行',
  FAILED: '研究失败',
};

export async function testWorkspaceTasks(t, { browser, origin, appHTML, task, stockSkill, plan }) {
  for (const viewport of [
    { width: 1280, height: 900 },
    { width: 360, height: 800 },
  ]) {
    await t.test(`research waiting and outcomes at ${viewport.width}px`, async () => {
      const context = await browser.newContext({ viewport });
      try {
        const page = await context.newPage();
        page.setDefaultTimeout(6000);
        const errors = [];
        page.on('pageerror', (err) => errors.push(err.message));
        let cancelRequested = false;
        let finishCancel = false;
        let cancelPosts = 0;
        let taskReads = 0;
        let readUnavailable = true;
        let logoutPosts = 0;
        const watchName = '模拟关注股';
        const details = (id, status) => ({
          ...task,
          id,
          subject: {
            ...task.subject,
            name:
              { active: '模拟证券甲', abstained: '模拟证券乙', ready: '模拟证券丙' }[id] ||
              task.subject.name,
          },
          status,
          steps,
          snapshot_as_of: status === 'QUEUED' ? null : task.snapshot_as_of,
          reason_code: status === 'ABSTAINED' ? 'REQUIRED_SECTION_UNAVAILABLE' : null,
          reason_message: status === 'ABSTAINED' ? '缺少最近年度财务数据' : null,
          report_ref: status === 'COMPLETED' && id !== 'no-report' ? task.report_ref : null,
        });
        await page.route(origin + '/app**', (route) =>
          route.fulfill({ contentType: 'text/html', body: appHTML })
        );
        await page.route(origin + '/', (route) =>
          route.fulfill({ contentType: 'text/html', body: '模拟公开页面' })
        );
        await page.route(origin + '/auth/logout', (route) => {
          assert.equal(route.request().method(), 'POST');
          assert.equal(route.request().headers()['x-csrf-token'], 'mock-csrf');
          logoutPosts++;
          return route.fulfill({ status: 204 });
        });
        await page.route(origin + '/api/v1/**', async (route) => {
          const request = route.request();
          const path = new URL(request.url()).pathname.slice('/api/v1/'.length);
          let body;
          if (path === 'me')
            body = {
              onboarded: true,
              csrf_token: 'mock-csrf',
              quota: {},
              synthetic: false,
              display_name: '模拟用户',
            };
          else if (path === 'skills') body = { items: [stockSkill] };
          else if (path === 'watchlist')
            body = {
              items: [
                {
                  id: 'mock-watch',
                  version: 1,
                  entity: { ...task.subject, name: watchName },
                  snapshot: {
                    trade_date: '2026-11-17',
                    close: '10',
                    change_pct: '1',
                    triggered: ['price_above'],
                  },
                  tags: [],
                  alerts: [],
                  note: '',
                  added_at: task.created_at,
                  last_report: null,
                },
              ],
            };
          else if (path === 'reports') body = { items: [] };
          else if (path === 'research-plans/mock-plan') body = plan;
          else if (path === 'research-plans') {
            const submitted = request.postDataJSON();
            if (submitted.text === '未知需求')
              body = { kind: 'unsupported', unsupported_reason: '模拟需求尚无可用研究类型' };
            else if (submitted.text === '候选研究')
              body = {
                kind: 'choose',
                candidates: [{ skill_id: stockSkill.id, subject: task.subject }],
              };
            else {
              assert.deepEqual(submitted, {
                skill_id: stockSkill.id,
                subject_id: task.subject.id,
                params: {},
              });
              body = { kind: 'plan', plan };
            }
          } else if (path === 'research-tasks')
            body = {
              items: [
                details('active', 'ANALYZING'),
                details('abstained', 'ABSTAINED'),
                details('ready', 'COMPLETED'),
              ],
            };
          else if (path === 'research-tasks/cancel-test/cancel') {
            assert.equal(request.method(), 'POST');
            assert.equal(request.postDataJSON().expected_revision, task.revision);
            assert.equal(request.headers()['x-csrf-token'], 'mock-csrf');
            cancelPosts++;
            cancelRequested = true;
            return route.fulfill({ status: 204 });
          } else if (path.startsWith('research-tasks/')) {
            taskReads++;
            const id = path.slice('research-tasks/'.length);
            if (id === 'read-error' && readUnavailable)
              return route.fulfill({
                status: 503,
                json: {
                  error: { code: 'UNAVAILABLE', message: '暂时无法读取研究任务', retryable: true },
                  request_id: 'mock-request-42',
                },
              });
            let status = id.toUpperCase();
            if (id === 'no-report' || id === 'read-error') status = 'COMPLETED';
            if (id === 'cancel-test')
              status = finishCancel ? 'CANCELLED' : cancelRequested ? 'CANCEL_REQUESTED' : 'QUEUED';
            body = details(id, status);
          } else throw new Error('Unexpected task request ' + path);
          await route.fulfill({ json: body });
        });

        const main = page.getByRole('main');
        const checkVisible = async () => {
          assert.doesNotMatch(await main.innerText(), workerText);
          assert.equal(await page.locator('html').evaluate((el) => el.scrollWidth), viewport.width);
        };
        const open = (id) => page.goto(origin + '/app/tasks/' + id);
        const cancel = page.getByRole('button', { name: '取消任务', exact: true });
        const viewReport = page.getByRole('link', { name: '查看报告', exact: true });
        await page.goto(origin + '/app');
        await main.getByText('模拟证券甲', { exact: true }).waitFor();
        await checkVisible();
        const assistant = main.getByRole('complementary', { name: '工作助手' });
        const summary = assistant.getByRole('region', { name: '自选股' });
        await summary.getByText(watchName).waitFor();
        await summary.getByText('有提醒').waitFor();
        const brand = page.getByRole('link', { name: 'Vera 工作台' });
        assert.equal(await brand.innerText(), 'Vera');
        const avatar = brand.locator('img');
        await avatar.evaluate((image) => image.decode());
        assert.equal(
          await page.locator('link[rel="icon"]').getAttribute('href'),
          '/vera-avatar.svg'
        );
        const nav = page.getByRole('navigation', { name: '工作区导航' });
        await nav.getByRole('link', { name: '自选股' }).click();
        await main.getByRole('heading', { name: '自选股', exact: true }).waitFor();
        await nav.getByRole('link', { name: '报告' }).click();
        await main.getByRole('heading', { name: '历史报告', exact: true }).waitFor();
        await nav.getByRole('link', { name: '工作台' }).click();
        const composer = main.getByLabel('描述你想要的研究');
        const start = main.getByRole('button', { name: '开始研究', exact: true });
        await composer.fill('未知需求');
        await start.click();
        await main.getByText(/模拟需求尚无可用研究类型/).waitFor();
        await composer.fill('候选研究');
        await start.click();
        await main.getByRole('button', { name: new RegExp(task.subject.name) }).click();
        await main.getByRole('heading', { name: '研究计划：' + task.subject.name }).waitFor();
        await page.goto(origin + '/app');
        await main.getByText('模拟证券甲', { exact: true }).waitFor();
        assert.equal(await main.locator('a[href="/app/reports/mock-report"]').count(), 1);
        assert.equal(await main.locator('a[href="/app/tasks/active"]').count(), 1);
        await main.locator('a[href="/app/tasks/abstained"]').click();
        await main.getByText('缺少最近年度财务数据').waitFor();

        for (const phase of activePhases) {
          await open(phase.toLowerCase());
          const wait = main.getByRole('status').filter({ hasText: '正在生成研究报告' });
          await wait.waitFor();
          assert.ok(
            await wait.evaluate((el) =>
              el
                .getAnimations({ subtree: true })
                .some((animation) => animation.playState === 'running')
            )
          );
          await cancel.waitFor();
          assert.equal(await viewReport.count(), 0);
          await main
            .getByText(phase === 'QUEUED' ? /数据快照尚未冻结/ : /快照截止.*14:00:05/)
            .waitFor();
          await checkVisible();
        }
        for (const [status, title] of Object.entries(outcomes)) {
          await open(status.toLowerCase());
          await main.getByRole('heading', { name: title, exact: true }).waitFor();
          assert.equal(await cancel.count(), 0);
          assert.equal(await viewReport.count(), status === 'COMPLETED' ? 1 : 0);
          if (status === 'ABSTAINED')
            await main.getByText('缺少最近年度财务数据', { exact: true }).waitFor();
          await checkVisible();
        }
        await open('no-report');
        await main.getByRole('heading', { name: '已完成', exact: true }).waitFor();
        assert.equal(await viewReport.count(), 0);
        await main.getByText(/报告.*(?:无法|暂|尚)/).waitFor();

        // Exercise the real polling path from a cancel request through the final response.
        await page.clock.install();
        await open('cancel-test');
        await cancel.waitFor();
        page.once('dialog', (dialog) => dialog.accept());
        await cancel.click();
        await main.getByRole('status').filter({ hasText: '正在取消' }).waitFor();
        assert.equal(await cancel.count(), 0);
        assert.equal(cancelPosts, 1);
        finishCancel = true;
        await main.getByRole('heading', { name: '已取消', exact: true }).waitFor();
        const terminalReads = taskReads;
        // Advance over a poll interval without delaying the test suite.
        await page.clock.fastForward(5000);
        assert.equal(taskReads, terminalReads);
        await checkVisible();
        await open('read-error');
        await page.getByRole('alert').filter({ hasText: 'mock-request-42' }).waitFor();
        readUnavailable = false;
        await page.getByRole('button', { name: '重新加载' }).click();
        await viewReport.waitFor();
        await page.goto(origin + '/app');
        const account = page.getByRole('button', { name: '账户菜单' });
        const logout = page.getByRole('button', { name: '退出登录' });
        await account.focus();
        await account.press('Enter');
        assert.equal(await account.getAttribute('aria-expanded'), 'true');
        await account.press('Tab');
        assert.ok(await logout.evaluate((el) => el === el.ownerDocument.activeElement));
        await logout.press('Escape');
        assert.equal(await account.getAttribute('aria-expanded'), 'false');
        assert.ok(await account.evaluate((el) => el === el.ownerDocument.activeElement));
        assert.equal(await page.locator('.desk-account-options').evaluate((el) => el.inert), true);
        await account.click();
        await logout.click();
        await page.waitForURL(origin + '/');
        assert.equal(logoutPosts, 1);
        assert.deepEqual(errors, []);
      } finally {
        await context.close();
      }
    });
  }
}
