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

const chart = {
  series_id: 'mock-financials',
  kind: 'bar',
  title: '模拟年度数据',
  x: 'report_period',
  unit: '亿元',
  note: '模拟数据图注',
  series: [
    {
      name: '样例收入',
      points: [
        ['2024-12-31', '182.40'],
        ['2025-12-31', '195.00'],
      ],
    },
    {
      name: '样例利润',
      points: [
        ['2023-12-31', '-2.80'],
        ['2025-12-31', '5.30'],
      ],
    },
  ],
};

async function assertNoPageOverflow(page) {
  const overflow = await page
    .locator('html')
    .evaluate((html) => html.scrollWidth - html.clientWidth);
  assert.ok(overflow <= 1, `The report overflows the viewport by ${overflow}px`);
}

async function finishTransitions(element) {
  return element.evaluate(async (el) => {
    const animations = el.getAnimations();
    await Promise.allSettled(animations.map((animation) => animation.finished));
    return animations.map((animation) => animation.transitionProperty);
  });
}

export async function testReportLayout(t, { browser, origin, appHTML, report, evidence }) {
  const details = {
    ...report,
    sections: [
      { ...report.sections[0], tables: [table], charts: [chart] },
      { ...report.sections[0], id: 'S2', title: '样例摘要', claim_refs: ['C2'] },
    ],
    claims: [...report.claims, { ...report.claims[0], ref: 'C2' }],
  };
  for (const [width, reducedMotion] of [
    [1440, 'no-preference'],
    [1024, 'no-preference'],
    [320, 'no-preference'],
    [390, 'no-preference'],
    [390, 'reduce'],
  ]) {
    await t.test(`report reader at ${width}px with motion ${reducedMotion}`, async () => {
      const context = await browser.newContext({ viewport: { width, height: 844 }, reducedMotion });
      try {
        const page = await context.newPage();
        page.setDefaultTimeout(6000);
        const path = '/app/reports/' + report.id;
        const extraSections =
          width === 1440
            ? Array.from({ length: 7 }, (_, i) => ({
                ...details.sections[1],
                id: 'S' + (i + 3),
                title: '图表附录 ' + (i + 1),
                charts: [{ ...chart, series_id: 'appendix-' + i, title: '附录图 ' + (i + 1) }],
              }))
            : [];
        await page.route(origin + path, (route) =>
          route.fulfill({ contentType: 'text/html', body: appHTML })
        );
        const responses = {
          me: { onboarded: true, csrf_token: 'mock-csrf', synthetic: true, quota: {} },
          [`reports/${report.id}/versions/1`]: {
            ...details,
            sections: [...details.sections, ...extraSections],
          },
          [`reports/${report.id}/versions/1/series/${chart.series_id}`]: chart,
          ...Object.fromEntries(
            extraSections.map(({ charts: [item] }) => [
              `reports/${report.id}/versions/1/series/${item.series_id}`,
              item,
            ])
          ),
          [`evidence/${evidence.id}`]: evidence,
        };
        await page.route(origin + '/api/v1/**', (route) => {
          const resource = new URL(route.request().url()).pathname.slice('/api/v1/'.length);
          assert.ok(Object.hasOwn(responses, resource), `Unexpected layout request: ${resource}`);
          return route.fulfill({ json: responses[resource] });
        });
        await page.goto(origin + path);
        await page.getByRole('heading', { name: report.title, exact: true }).waitFor();
        const figure = page.locator('figure').filter({ hasText: chart.title });
        await figure.scrollIntoViewIfNeeded();
        await figure.getByText('单位 亿元', { exact: true }).waitFor();
        await figure.getByText(chart.note, { exact: true }).waitFor();
        await figure.locator('svg text').first().waitFor();
        const labels = await figure.locator('svg text').allTextContents();
        for (const label of ['2023', '2025', '0']) {
          assert.ok(labels.includes(label), `chart label is missing: ${label}`);
        }
        const revenue = figure.getByRole('button', { name: '样例收入', exact: true });
        const hundreds = figure
          .locator('svg text')
          .filter({ hasText: /^\d{3}$/ })
          .first();
        await hundreds.waitFor();
        await revenue.click();
        assert.equal(await revenue.getAttribute('aria-pressed'), 'false');
        await hundreds.waitFor({ state: 'detached' });
        await revenue.press('Space');
        assert.equal(await revenue.getAttribute('aria-pressed'), 'true');
        await hundreds.waitFor();
        const article = page.getByRole('article');
        const toc = page.getByRole('navigation', { name: '章节目录' });
        const chapter = toc.getByRole('link', { name: /样例摘要/ });
        const body = await article.boundingBox();
        if (width === 1440) {
          const rail = await toc.boundingBox();
          assert.ok(rail.width <= 192 && rail.x + rail.width <= body.x, 'compact chapter rail');
          assert.ok(body.x <= 300, `reading column starts at ${body.x}px`);
        }
        await assertNoPageOverflow(page);
        if (width <= 390) {
          assert.ok(body.width >= width * 0.85, 'text uses the width of the phone');
          const scrolled = await page.locator('.table-wrap').evaluate((wrapper) => {
            wrapper.scrollLeft = wrapper.scrollWidth;
            return wrapper.scrollLeft;
          });
          assert.ok(scrolled > 0, 'the wide table can scroll independently');
          const menu = page.getByRole('button', { name: /章节目录/ });
          const fold = toc.locator('.toc-fold');
          assert.equal(await menu.getAttribute('aria-expanded'), 'false');
          assert.equal(await fold.evaluate((el) => el.inert), true);
          await menu.focus();
          await menu.press('Enter');
          assert.equal(await menu.getAttribute('aria-expanded'), 'true');
          const opening = await finishTransitions(fold);
          if (reducedMotion === 'reduce') assert.equal(opening.length, 0);
          else assert.ok(opening.includes('grid-template-rows'), 'directory expands smoothly');
          assert.ok(
            Math.abs((await article.boundingBox()).y - body.y) <= 1,
            'directory overlays the text'
          );
          await menu.press('Tab');
          assert.match(await page.locator(':focus').innerText(), /S1/);
          await page.keyboard.press('Escape');
          assert.equal(await menu.getAttribute('aria-expanded'), 'false');
          assert.ok(await menu.evaluate((el) => el === el.ownerDocument.activeElement));
          assert.equal(await fold.evaluate((el) => el.inert), true);
          await menu.press('Enter');
          await finishTransitions(fold);
        }
        await chapter.click();
        await page.waitForURL((url) => url.hash === '#S2');
        const heading = await page.getByRole('heading', { name: /样例摘要/ }).boundingBox();
        assert.ok(heading.y >= 0 && heading.y + heading.height <= 844, 'chapter is in view');

        const citation = article
          .locator('#S2')
          .getByRole('button', { name: '查看证据 2', exact: true });
        const download = page.locator('.reader-back a[download]');
        assert.equal(await download.innerText(), 'PDF');
        await citation.scrollIntoViewIfNeeded();
        const toolbarPosition = await download.boundingBox();
        await citation.click();
        const panel = page.getByTestId('evidence-panel');
        const entering = await finishTransitions(panel);
        if (reducedMotion === 'reduce') assert.equal(entering.length, 0);
        else assert.ok(entering.includes('opacity'), 'evidence fades in');
        await panel.getByText(evidence.title, { exact: true }).waitFor();
        await assertNoPageOverflow(page);
        const openedBody = await article.boundingBox();
        const openedPanel = await panel.boundingBox();
        const openedDownload = await download.boundingBox();
        assert.deepEqual(openedDownload, toolbarPosition, 'download stays in the toolbar');
        assert.ok(openedPanel.x >= 0 && openedPanel.x + openedPanel.width <= width + 1);
        if (width === 1440) {
          assert.ok(openedBody.width >= 560, 'evidence leaves a readable article column');
          assert.ok(openedBody.x + openedBody.width <= openedPanel.x + 1, 'panels do not overlap');
        } else {
          assert.ok(Math.abs(openedBody.width - body.width) <= 1, 'drawer preserves text width');
          assert.ok(Math.abs(openedPanel.y + openedPanel.height - 844) <= 1, 'bottom drawer');
        }
        await page.keyboard.press('Escape');
        assert.equal(await citation.getAttribute('aria-expanded'), 'false');
        assert.ok(await citation.evaluate((el) => el === el.ownerDocument.activeElement));
        // Reopen before the first close completes and verify the same citation stays selected.
        await citation.press('Enter');
        await panel.getByText(evidence.title, { exact: true }).waitFor();
        assert.equal(await citation.getAttribute('aria-expanded'), 'true');
        await finishTransitions(panel);
        await panel.getByRole('button', { name: '关闭证据' }).click();
        await panel.waitFor({ state: 'detached' });
        assert.deepEqual(await download.boundingBox(), toolbarPosition);
        assert.ok(await citation.evaluate((el) => el === el.ownerDocument.activeElement));
        if (extraSections.length) {
          const last = page.locator('figure').last();
          await last.getByRole('button', { name: '样例收入' }).waitFor();
          assert.equal(await last.locator('svg').count(), 0);
          await last.scrollIntoViewIfNeeded();
          const svg = last.locator('.chart-box svg');
          await svg.waitFor();
          const fullWidth = await svg.getAttribute('width');
          const legend = last.getByRole('button', { name: '样例收入' });
          await legend.press('Space');
          await citation.click();
          await panel.getByText(evidence.title, { exact: true }).waitFor();
          await finishTransitions(page.locator('.report'));
          assert.equal(await svg.getAttribute('width'), fullWidth, 'offscreen plot skips reflow');
          await last.scrollIntoViewIfNeeded();
          await page.waitForFunction(() => {
            const box = [...document.querySelectorAll('.chart-box')].at(-1);
            return Number(box.querySelector('svg')?.getAttribute('width')) === box.clientWidth;
          });
          assert.ok(Number(await svg.getAttribute('width')) < Number(fullWidth));
          assert.equal(await legend.getAttribute('aria-pressed'), 'false');
          await page.emulateMedia({ media: 'print' });
          await page.waitForFunction(() =>
            [...document.querySelectorAll('.chart-box')].every(
              (box) => Number(box.querySelector('svg')?.getAttribute('width')) === box.clientWidth
            )
          );
          assert.equal(await page.locator('.chart-box svg').count(), 8);
        }
      } finally {
        await context.close();
      }
    });
  }
}
