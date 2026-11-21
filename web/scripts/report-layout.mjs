import assert from 'node:assert/strict';

// Use enough columns to exercise a genuinely scrollable table at phone widths.
const columns = [
  '数据期间',
  '样例成交额（元）',
  '样例成交量（股）',
  '样例换手率（%）',
  '样例披露日',
];
const table = {
  title: '模拟成交明细',
  columns,
  rows: Array.from({ length: 16 }, (_, index) => [
    `模拟期间 ${index + 1}`,
    '123,456,789.00',
    '12,345,678',
    '12.34%',
    '2026-11-20',
  ]),
};

async function assertNoPageOverflow(page) {
  const overflow = await page
    .locator('html')
    .evaluate((html) => html.scrollWidth - html.clientWidth);
  assert.ok(overflow <= 1, `The report overflows the viewport by ${overflow}px`);
}

export async function testReportLayout(t, { browser, origin, appHTML, report, evidence }) {
  const details = {
    ...report,
    sections: [
      { ...report.sections[0], tables: [table] },
      { ...report.sections[0], id: 'S2', title: '样例摘要', claim_refs: ['C2'] },
    ],
    claims: [...report.claims, { ...report.claims[0], ref: 'C2' }],
  };
  for (const width of [1440, 1024, 390]) {
    await t.test(`report reading, chapter navigation and evidence at ${width}px`, async () => {
      const context = await browser.newContext({ viewport: { width, height: 844 } });
      try {
        const page = await context.newPage();
        page.setDefaultTimeout(6000);
        const path = '/app/reports/' + report.id;
        await page.route(origin + path, (route) =>
          route.fulfill({ contentType: 'text/html', body: appHTML })
        );
        const responses = {
          me: { onboarded: true, csrf_token: 'mock-csrf', synthetic: true, quota: {} },
          [`reports/${report.id}/versions/1`]: details,
          [`evidence/${evidence.id}`]: evidence,
        };
        await page.route(origin + '/api/v1/**', (route) => {
          const resource = new URL(route.request().url()).pathname.slice('/api/v1/'.length);
          assert.ok(Object.hasOwn(responses, resource), `Unexpected layout request: ${resource}`);
          return route.fulfill({ json: responses[resource] });
        });
        await page.goto(origin + path);
        await page.getByRole('heading', { name: report.title, exact: true }).waitFor();
        const article = page.getByRole('article');
        const toc = page.getByRole('navigation', { name: '章节目录' });
        const body = await article.boundingBox();
        if (width === 1440) {
          const rail = await toc.boundingBox();
          assert.ok(rail.width <= 192 && rail.x + rail.width <= body.x, 'compact chapter rail');
          assert.ok(body.x <= 300, `reading column starts at ${body.x}px`);
        }
        await assertNoPageOverflow(page);
        if (width === 390) {
          assert.ok(body.width >= width * 0.85, 'text uses the width of the phone');
          const scrolled = await page.locator('.table-wrap').evaluate((wrapper) => {
            wrapper.scrollLeft = wrapper.scrollWidth;
            return wrapper.scrollLeft;
          });
          assert.ok(scrolled > 0, 'the wide table can scroll independently');
          const menu = page.getByRole('button', { name: /章节目录/ });
          assert.equal(await menu.getAttribute('aria-expanded'), 'false');
          await menu.click();
          assert.equal(await menu.getAttribute('aria-expanded'), 'true');
        }
        const chapter = toc.getByRole('link', { name: /样例摘要/ });
        await chapter.click();
        await page.waitForURL((url) => url.hash === '#S2');
        const heading = await page.getByRole('heading', { name: /样例摘要/ }).boundingBox();
        assert.ok(heading.y >= 0 && heading.y + heading.height <= 844, 'chapter is in view');

        await page.getByRole('button', { name: '查看证据 2', exact: true }).click();
        const panel = page.getByTestId('evidence-panel');
        await panel.getByText(evidence.title, { exact: true }).waitFor();
        await assertNoPageOverflow(page);
        const openedBody = await article.boundingBox();
        const openedPanel = await panel.boundingBox();
        assert.ok(openedPanel.x >= 0 && openedPanel.x + openedPanel.width <= width + 1);
        if (width === 1440) {
          assert.ok(openedBody.width >= 560, 'evidence leaves a readable article column');
          assert.ok(openedBody.x + openedBody.width <= openedPanel.x + 1, 'panels do not overlap');
        } else {
          assert.ok(Math.abs(openedBody.width - body.width) <= 1, 'drawer preserves text width');
          assert.ok(Math.abs(openedPanel.y + openedPanel.height - 844) <= 1, 'bottom drawer');
        }
        await panel.getByRole('button', { name: '关闭证据' }).click();
        await panel.waitFor({ state: 'hidden' });
      } finally {
        await context.close();
      }
    });
  }
}
