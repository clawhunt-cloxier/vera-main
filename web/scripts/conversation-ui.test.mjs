import assert from 'node:assert/strict';
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
const desktopUser = process.env.VERA_CHAT_USER;
const mobileUser = process.env.VERA_CHAT_MOBILE_USER;
const passwordFile = process.env.VERA_CHAT_PASSWORD_FILE;
if (!desktopUser || !mobileUser || desktopUser === mobileUser || !passwordFile) {
  throw new Error('Set two distinct local test accounts and VERA_CHAT_PASSWORD_FILE');
}
const password = readFileSync(passwordFile, 'utf8').trim();
if (!password) throw new Error('The local test password file is empty');

test('private conversational research on desktop and mobile', async (t) => {
  const browser = await chromium.launch({ executablePath: process.env.VERA_CHAT_CHROMIUM_PATH });
  t.after(() => browser.close());
  for (const { viewport, username } of [
    { viewport: { width: 1440, height: 960 }, username: desktopUser },
    { viewport: { width: 390, height: 844 }, username: mobileUser },
  ]) {
    await t.test(`${viewport.width}px`, async () => {
      const context = await browser.newContext({ baseURL: origin.origin, viewport });
      try {
        const page = await context.newPage();
        const replyFor = (question) =>
          page
            .locator('.chat-turns > li')
            .filter({ has: page.getByText(question, { exact: true }) })
            .locator('.chat-answer');
        page.setDefaultTimeout(15000);
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        await page.goto('/');
        await page.getByRole('link', { name: '进入工作台' }).first().click();
        await page.getByLabel('账号').fill(username);
        await expect(page.getByRole('button', { name: '登录', exact: true })).toBeDisabled();
        await page.getByLabel('密码').fill(password);
        await page.getByRole('button', { name: '登录', exact: true }).click();
        await expect(page.getByRole('heading', { name: '今天想研究什么？' })).toBeVisible();
        await expect(page.getByLabel('邀请码')).toHaveCount(0);
        await expect(page.getByText('今日剩余', { exact: false })).toHaveCount(0);
        const send = async (text, label = '继续提问') => {
          await page.getByLabel(label).fill(text);
          await page.getByRole('button', { name: '发送消息' }).click();
        };
        const deleteThread = async () => {
          await page.getByRole('button', { name: '删除会话' }).click();
          await page
            .getByRole('group', { name: '确认删除会话' })
            .getByRole('button', { name: '删除', exact: true })
            .click();
          await expect(page.getByRole('heading', { name: '对话研究' })).toBeVisible();
        };
        await send('你好', '输入研究问题');
        await expect(
          page.locator('.chat-answer').getByText('我是 Vera', { exact: false })
        ).toBeVisible();
        await send('什么是市盈率');
        await expect(
          replyFor('什么是市盈率').getByText('公司市值相对于盈利', { exact: false })
        ).toBeVisible();
        await send('那它有什么局限');
        await expect(
          replyFor('那它有什么局限').getByText('亏损时市盈率', { exact: false })
        ).toBeVisible();
        await send('今天纽约天气怎么样');
        await expect(
          replyFor('今天纽约天气怎么样').getByText('你可以问我金融概念', { exact: false })
        ).toBeVisible();
        await send('现在该不该买演示制造');
        await expect(
          page.getByText('目标价及未来收益判断超出服务范围', { exact: false })
        ).toBeVisible();
        await expect(page.locator('.chat-result')).toHaveCount(0);
        await deleteThread();
        await page
          .getByRole('navigation', { name: '工作区导航' })
          .getByRole('link', { name: '工作台' })
          .click();

        const question = '演示制造近 20 个交易日的融资余额怎么变';
        await send(question, '输入研究问题');
        await page.waitForURL('**/app/chat/*');
        const threadURL = page.url();
        const result = page.locator('.chat-result').last();
        await expect(result.getByRole('img', { name: '融资余额', exact: true })).toBeVisible({
          timeout: 30000,
        });
        await expect(page.getByRole('button', { name: '确认研究' })).toHaveCount(0);
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
        await send('这个数怎么算的');
        await page.getByRole('button', { name: '查看原计算' }).click();
        await expect(panel.getByText('计算过程', { exact: true })).toBeVisible();
        await expect(panel.getByText('F18.balance_change', { exact: false })).toBeVisible();
        await panel.getByRole('button', { name: '关闭证据' }).click();

        await send('改成 60 个交易日');
        await result.getByRole('button', { name: '查看全部 60 个交易日' }).click();
        await expect(result.locator('tbody tr')).toHaveCount(60);
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

        await deleteThread();
        await expect(page.getByRole('link', { name: new RegExp(question) })).toHaveCount(0);

        const meResponse = await context.request.get('/api/v1/me');
        assert.equal(meResponse.status(), 200);
        const me = await meResponse.json();
        const accepted = await context.request.post('/api/v1/conversations', {
          headers: { 'X-CSRF-Token': me.csrf_token },
          data: { client_message_id: crypto.randomUUID(), text: '演示制造的日成交' },
        });
        assert.equal(accepted.status(), 202);
        const { conversation_id } = await accepted.json();
        await page.goto('/app/chat/' + conversation_id);
        const plan = page.getByRole('region', { name: '待确认的研究计划' });
        await expect(plan).toBeVisible();
        await plan.getByRole('button', { name: '确认研究' }).click();
        await expect(page.locator('.chat-result').getByRole('table')).toBeVisible({
          timeout: 30000,
        });
        await deleteThread();
        assert.deepEqual(errors, []);
      } finally {
        await context.close();
      }
    });
  }
});
