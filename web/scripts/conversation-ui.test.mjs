import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { chromium, expect } from '@playwright/test';

const origin = new URL(process.env.VERA_CHAT_URL ?? 'http://127.0.0.1:18095');
if (
  origin.protocol !== 'http:' ||
  origin.hostname !== '127.0.0.1' ||
  origin.username ||
  origin.password
) {
  throw new Error('Use the loopback URL of a local simulated Vera demo');
}
const invite = readFileSync(process.env.VERA_CHAT_INVITE_FILE ?? '', 'utf8')
  .trim()
  .split(/\r?\n/)
  .at(-1);
if (!invite) throw new Error('Set VERA_CHAT_INVITE_FILE to a local demo invite file');

test('private conversational research on desktop and mobile', async (t) => {
  const browser = await chromium.launch();
  t.after(() => browser.close());
  for (const viewport of [
    { width: 1440, height: 960 },
    { width: 390, height: 844 },
  ]) {
    await t.test(`${viewport.width}px`, async () => {
      const context = await browser.newContext({ baseURL: origin.origin, viewport });
      try {
        const page = await context.newPage();
        page.setDefaultTimeout(15000);
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        await page.goto('/auth/login?return_to=/app');
        await page.getByLabel('用户名').fill('chat-' + randomUUID().slice(0, 12));
        await page.getByRole('button', { name: '登录', exact: true }).click();
        await page.waitForURL('**/onboarding');
        await page.getByLabel('邀请码').fill(invite);
        await page.getByRole('button', { name: '开通工作区' }).click();
        await expect(page.getByRole('heading', { name: '今天想研究什么？' })).toBeVisible();

        const question = '演示制造近 20 个交易日的融资余额怎么变';
        await page.getByLabel('输入研究问题').fill(question);
        await page.getByRole('button', { name: '发送研究问题' }).click();
        await page.waitForURL('**/app/chat/*');
        const threadURL = page.url();
        await expect(
          page.getByRole('region', { name: '待确认的研究计划' }).getByText('20 个交易日')
        ).toBeVisible();
        await page.getByRole('button', { name: '确认研究' }).click();
        const result = page.locator('.chat-result');
        await expect(result.getByRole('img', { name: '融资余额', exact: true })).toBeVisible({
          timeout: 30000,
        });
        await expect(result.getByRole('table')).toBeVisible();
        await result.getByRole('button', { name: '查看全部 20 个交易日' }).click();
        await expect(result.locator('tbody tr')).toHaveCount(20);
        await result.getByRole('button', { name: '图表依据' }).click();
        const panel = page.getByRole('dialog', { name: '证据' });
        await expect(panel).toBeVisible();
        const box = await panel.boundingBox();
        assert.ok(box && box.x >= 0 && box.x + box.width <= viewport.width + 1);
        if (process.env.VERA_CHAT_SCREENSHOTS) {
          await page.screenshot({
            animations: 'disabled',
            path: process.env.VERA_CHAT_SCREENSHOTS + '/evidence-' + viewport.width + '.png',
          });
        }
        await page.keyboard.press('Escape');
        await expect(panel).toBeHidden();
        await page.reload();
        await expect(result.getByRole('table')).toBeVisible({ timeout: 15000 });
        if (process.env.VERA_CHAT_SCREENSHOTS) {
          await page.screenshot({
            animations: 'disabled',
            path: process.env.VERA_CHAT_SCREENSHOTS + '/result-' + viewport.width + '.png',
            fullPage: true,
          });
        }

        await result.getByRole('button', { name: '追问' }).click();
        const followup = page.getByLabel('继续提问');
        const send = async (text) => {
          await followup.fill(text);
          await page.getByRole('button', { name: '发送研究问题' }).click();
        };
        await send('这个数怎么算的');
        await page.getByRole('button', { name: '查看原计算' }).click();
        await expect(panel.getByText('计算过程', { exact: true })).toBeVisible();
        await expect(panel.getByText('F18.balance_change', { exact: false })).toBeVisible();
        await panel.getByRole('button', { name: '关闭证据' }).click();

        await send('改成 60 个交易日');
        await expect(
          page.getByRole('region', { name: '待确认的研究计划' }).getByText('60 个交易日')
        ).toBeVisible();
        await page.getByRole('button', { name: '取消本轮' }).click();
        await expect(page.getByText('已取消本轮研究。', { exact: true })).toBeVisible();
        const dimensions = await page
          .locator('html')
          .evaluate((html) => [html.scrollWidth, html.clientWidth]);
        assert.equal(dimensions[0], dimensions[1], 'horizontal overflow');
        await page
          .getByRole('navigation', { name: '工作区导航' })
          .getByRole('link', { name: '工作台' })
          .click();
        await expect(page.getByRole('heading', { name: '今天想研究什么？' })).toBeVisible();
        await expect(page.getByRole('link', { name: new RegExp(question) })).toBeVisible();
        await page.getByRole('link', { name: new RegExp(question) }).click();
        assert.equal(page.url(), threadURL);
        await expect(page.getByText('这个数怎么算的', { exact: true })).toBeVisible();

        await page.getByRole('button', { name: '删除会话' }).click();
        await page
          .getByRole('group', { name: '确认删除会话' })
          .getByRole('button', { name: '删除', exact: true })
          .click();
        await expect(page.getByRole('heading', { name: '对话研究' })).toBeVisible();
        await expect(page.getByRole('link', { name: new RegExp(question) })).toHaveCount(0);
        assert.deepEqual(errors, []);
      } finally {
        await context.close();
      }
    });
  }
});
