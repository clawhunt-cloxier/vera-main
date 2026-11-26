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
  throw new Error('Use the loopback URL of a local Vera demo with a configured conversation model');
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
        const greeting = '嗨，还在吗？';
        await send(greeting, '输入研究问题');
        const textFor = (question) => replyFor(question).locator('.chat-reply');
        const timeout = { timeout: 24000 };
        await expect(textFor(greeting)).toHaveText(
          /你好|在的|在呢|Vera|您好|在呀|我在|哈喽|嗨/,
          timeout
        );

        const education = '为什么封闭式基金的交易价格和净值可能不一样？';
        await send(education);
        await expect(textFor(education)).toHaveText(/净值/, timeout);
        await expect(textFor(education)).toHaveText(/供需|需求|预期|买方|卖方|市场/, timeout);

        const followUp = '那到期清算的时候呢？';
        await send(followUp);
        await expect(textFor(followUp)).toHaveText(/清算/, timeout);
        await expect(textFor(followUp)).toHaveText(/分配|处置|靠拢|收敛|缩小|兑现/, timeout);
        assert.notEqual(
          await textFor(education).textContent(),
          await textFor(followUp).textContent()
        );

        const offTopic = '下周去旅行，帮我查一下纽约天气';
        await send(offTopic);
        await expect(textFor(offTopic)).toHaveText(/金融|投研|天气/, timeout);

        const advice = '手头有笔钱，今天就投演示制造合适吗？';
        await send(advice);
        await expect(textFor(advice)).toHaveText(
          /不能|无法|不提供|不作|不替|不会|没法|没办法/,
          timeout
        );
        await expect(page.locator('.chat-result')).toHaveCount(0);
        const mixed = '查演示制造近 20 个交易日的成交额并给目标价';
        await send(mixed);
        await expect(replyFor(mixed).getByRole('table')).toBeVisible({ timeout: 30000 });
        await expect(replyFor(mixed).getByText('本轮仅交付可核验的历史研究。')).toBeVisible();
        await deleteThread();
        await page
          .getByRole('navigation', { name: '工作区导航' })
          .getByRole('link', { name: '工作台' })
          .click();

        const question = '演示制造近 20 个交易日的融资余额怎么变';
        await send(question, '输入研究问题');
        await page.waitForURL('**/app/chat/*');
        const threadURL = page.url();
        const threadPath = new URL(threadURL).pathname;
        const result = page.locator('.chat-result').last();
        await expect(result.getByRole('img', { name: '融资余额', exact: true })).toBeVisible({
          timeout: 30000,
        });
        await expect(page.getByRole('button', { name: '确认研究' })).toHaveCount(0);
        const title = page
          .getByRole('region', { name: '研究对话' })
          .getByRole('heading', { level: 1 });
        await expect(title).toHaveText(/融资/, timeout);
        const generatedTitle = (await title.textContent()).trim();
        assert.ok(generatedTitle !== question && [...generatedTitle].length <= 32);
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
        await expect(title).toHaveText(generatedTitle);
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
        const historyLink = page
          .getByRole('region', { name: '对话历史' })
          .locator(`a[href="${threadPath}"]`);
        await expect(historyLink.locator('span')).toHaveText(generatedTitle);
        await historyLink.click();
        assert.equal(page.url(), threadURL);
        await expect(page.getByText('这个数怎么算的', { exact: true })).toBeVisible();

        await deleteThread();
        await expect(historyLink).toHaveCount(0);

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
