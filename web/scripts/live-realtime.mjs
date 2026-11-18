import { readFileSync, realpathSync, statSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, relative, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const media = 'application/vnd.vera.v0.4+json';
const skillVersion = '1.1.0-demo.3';
const dataset = 'mkt.intraday_quote';
// Separate acceptance targets: latest checks the initial TIME,NOW-only policy;
// strict realtime still requires a verified continuous-trading snapshot.
const modes = {
  realtime_trade: {
    latency: 'real_time',
    status: 'fresh',
    state: '连续交易',
    freshnessLabel: '符合新鲜度策略',
    latencyLabel: '实时传输',
  },
  latest_available: {
    latency: 'unknown',
    status: 'unknown',
    state: '未确认',
    freshnessLabel: '新鲜度未知',
    latencyLabel: '延迟性质未知',
  },
};
const terminals = new Set([
  'COMPLETED',
  'ABSTAINED',
  'BUDGET_EXHAUSTED',
  'CANCELLED',
  'BLOCKED',
  'FAILED',
]);
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

class AcceptanceError extends Error {}
function requireValue(condition, code) {
  if (!condition) throw new AcceptanceError(code);
}
function integer(value, code, max) {
  requireValue(/^\d+$/.test(value ?? ''), code);
  const result = Number(value);
  requireValue(Number.isSafeInteger(result) && result > 0 && result <= max, code);
  return result;
}

// This preflight is offline. It never prints configuration or session contents.
export function readConfig(env) {
  requireValue(env.VERA_LIVE_BROWSER === '1', 'EXPLICIT_LIVE_OPT_IN_REQUIRED');
  const deployment =
    env.GITHUB_ACTIONS === 'true' &&
    env.GITHUB_EVENT_NAME === 'workflow_dispatch' &&
    env.VERA_LIVE_DEPLOY_ACCEPT === '1';
  requireValue(!env.CI || deployment, 'LIVE_RUN_NOT_ALLOWED_IN_CI');
  requireValue(!env.DEBUG && !env.PWDEBUG && !env.NODE_DEBUG, 'DEBUG_LOGGING_NOT_ALLOWED');
  let base;
  try {
    base = new URL(env.VERA_LIVE_BASE_URL);
  } catch {
    throw new AcceptanceError('HTTPS_ORIGIN_REQUIRED');
  }
  requireValue(
    base.protocol === 'https:' &&
      !base.username &&
      !base.password &&
      base.pathname === '/' &&
      !base.search &&
      !base.hash,
    'HTTPS_ORIGIN_REQUIRED'
  );
  requireValue(
    /^[0-9]{6}\.(SH|SZ|BJ)$/.test(env.VERA_LIVE_STOCK_CODE ?? ''),
    'A_SHARE_CODE_REQUIRED'
  );
  requireValue(
    /^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$/.test(env.VERA_LIVE_PROVIDER_ID ?? ''),
    'EXPECTED_PROVIDER_ID_REQUIRED'
  );
  requireValue(
    typeof env.VERA_LIVE_PROVIDER_NAME === 'string' &&
      env.VERA_LIVE_PROVIDER_NAME.trim().length > 0 &&
      env.VERA_LIVE_PROVIDER_NAME.length <= 200,
    'EXPECTED_PROVIDER_NAME_REQUIRED'
  );
  const maxAge = integer(env.VERA_LIVE_MAX_AGE_MS, 'REGISTERED_MAX_AGE_MS_REQUIRED', 86400000);
  const taskTimeout = integer(
    env.VERA_LIVE_TASK_TIMEOUT_MS ?? '180000',
    'INVALID_TASK_TIMEOUT_MS',
    900000
  );
  requireValue(
    isAbsolute(env.VERA_LIVE_STORAGE_STATE ?? ''),
    'ABSOLUTE_STORAGE_STATE_PATH_REQUIRED'
  );
  let state;
  try {
    const path = realpathSync(env.VERA_LIVE_STORAGE_STATE);
    const rel = relative(root, path);
    requireValue(
      rel.startsWith('..' + '/') || isAbsolute(rel),
      'STORAGE_STATE_MUST_BE_OUTSIDE_REPOSITORY'
    );
    const file = statSync(path);
    requireValue(
      file.isFile() && file.size > 0 && file.size <= 1048576,
      'INVALID_STORAGE_STATE_FILE'
    );
    requireValue(
      (file.mode & 0o077) === 0 &&
        (typeof process.getuid !== 'function' || file.uid === process.getuid()),
      'STORAGE_STATE_MUST_BE_OWNER_ONLY'
    );
    state = JSON.parse(readFileSync(path, 'utf8'));
    requireValue(
      Array.isArray(state.cookies) && Array.isArray(state.origins),
      'INVALID_STORAGE_STATE'
    );
    requireValue(
      state.cookies.some((cookie) => {
        const domain = String(cookie.domain ?? '');
        const host = domain.replace(/^\./, '');
        return (
          (base.hostname === host ||
            (domain.startsWith('.') && base.hostname.endsWith('.' + host))) &&
          (cookie.expires === -1 || cookie.expires > Date.now() / 1000)
        );
      }),
      'NO_CURRENT_COOKIE_FOR_TARGET'
    );
  } catch (error) {
    if (error instanceof AcceptanceError) throw error;
    throw new AcceptanceError('STORAGE_STATE_UNREADABLE');
  }
  return {
    origin: base.origin,
    state,
    code: env.VERA_LIVE_STOCK_CODE,
    provider: env.VERA_LIVE_PROVIDER_ID,
    providerName: env.VERA_LIVE_PROVIDER_NAME,
    maxAge,
    taskTimeout,
  };
}

function timestamp(value) {
  const match =
    typeof value === 'string' &&
    value.match(/^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/);
  requireValue(match, 'SOURCE_TIME_MISSING');
  const parsed = Date.parse(match[1] + match[3]);
  requireValue(Number.isFinite(parsed), 'SOURCE_TIME_INVALID');
  // Go emits RFC3339Nano and PostgreSQL retains microseconds. Date alone
  // would accept inconsistent source times inside the same millisecond.
  return BigInt(parsed) * 1000000n + BigInt((match[2] ?? '').padEnd(9, '0'));
}
function sameTime(a, b) {
  return timestamp(a) === timestamp(b);
}
const displayTime = (value) =>
  new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).format(new Date(value));

export function validateReport(report, task, plan, config, confirmedAt, observedAt) {
  requireValue(report.synthetic === false && report.ai_generated === true, 'REPORT_NOT_REAL');
  requireValue(
    report.skill_id === 'stock_analysis' &&
      report.skill_version === skillVersion &&
      report.snapshot_mode === 'collect_latest' &&
      report.result_kind === 'limited',
    'WRONG_REPORT_KIND'
  );
  requireValue(
    report.task_id === task.id &&
      report.id === task.report_ref.id &&
      report.version === task.report_ref.version &&
      report.subject.id === plan.subject.id &&
      report.subject.symbol === config.code,
    'REPORT_BINDING_MISMATCH'
  );
  requireValue(
    sameTime(report.as_of, plan.as_of) &&
      sameTime(task.as_of, plan.as_of) &&
      sameTime(report.snapshot_as_of, task.snapshot_as_of),
    'PREVIEW_OR_SNAPSHOT_MISMATCH'
  );
  requireValue(
    Array.isArray(report.source_freshness) && report.source_freshness.length === 1,
    'UNEXPECTED_SOURCE_COUNT'
  );
  const source = report.source_freshness[0];
  const expected = modes[config.requirement];
  requireValue(
    source.dataset_id === dataset &&
      source.provider === config.provider &&
      source.latency_mode === expected.latency &&
      source.status === expected.status,
    'SOURCE_MODE_MISMATCH'
  );
  requireValue(
    typeof source.note === 'string' && source.note.includes('交易状态：' + expected.state),
    'SOURCE_TRADING_STATE_MISMATCH'
  );
  requireValue(
    source.published_at === null && source.period === null,
    'QUOTE_TIME_SEMANTICS_CHANGED'
  );
  const [event, received, available, cutoff, preview, delivered] = [
    source.event_at,
    source.received_at,
    source.available_at,
    report.snapshot_as_of,
    plan.as_of,
    report.delivered_at,
  ].map(timestamp);
  requireValue(
    event <= received && received <= available && available <= cutoff && preview <= cutoff,
    'SOURCE_TIME_ORDER_INVALID'
  );
  requireValue(
    cutoff - event <= BigInt(config.maxAge) * 1000000n,
    'QUOTE_EXCEEDS_ACCEPTANCE_MAX_AGE'
  );
  // Host clocks may differ by five seconds. Delivery is persisted at second precision.
  requireValue(
    cutoff >= BigInt(confirmedAt - 5000) * 1000000n &&
      cutoff <= BigInt(observedAt + 5000) * 1000000n &&
      delivered + 1000000000n >= cutoff &&
      delivered <= BigInt(observedAt + 5000) * 1000000n,
    'SNAPSHOT_NOT_FROM_THIS_RUN'
  );
  requireValue(
    Array.isArray(report.sources) &&
      report.sources.length === 1 &&
      report.sources[0].dataset_id === dataset &&
      report.sources[0].provider === config.providerName &&
      sameTime(report.sources[0].updated_at, source.available_at),
    'REPORT_SOURCE_MISMATCH'
  );
  requireValue(
    Array.isArray(report.claims) &&
      report.claims.length > 0 &&
      report.claims.every(
        (claim) => Array.isArray(claim.evidence_refs) && claim.evidence_refs.length === 1
      ),
    'CLAIM_EVIDENCE_MISSING'
  );
  const evidenceIDs = [...new Set(report.claims.flatMap((claim) => claim.evidence_refs))];
  requireValue(
    evidenceIDs.length === 1 && /^[0-9a-f-]{36}$/i.test(evidenceIDs[0]),
    'UNEXPECTED_QUOTE_EVIDENCE'
  );
  return { source, evidenceID: evidenceIDs[0], age: Number(cutoff - event) / 1000000 };
}

export function validateEvidence(evidence, source, evidenceID, config) {
  requireValue(
    evidence.id === evidenceID &&
      evidence.dataset_id === dataset &&
      evidence.provider === config.provider &&
      evidence.latency_mode === modes[config.requirement].latency &&
      evidence.latency_mode === source.latency_mode,
    'EVIDENCE_SOURCE_MISMATCH'
  );
  requireValue(
    ['event_at', 'received_at', 'available_at'].every((field) =>
      sameTime(evidence[field], source[field])
    ),
    'EVIDENCE_TIME_MISMATCH'
  );
  requireValue(
    evidence.origin === 'vendor_derived' &&
      evidence.source_tier === 'L2' &&
      evidence.unit === 'CNY/share' &&
      typeof evidence.value === 'string' &&
      /^\d+(?:\.\d+)?$/.test(evidence.value) &&
      /[1-9]/.test(evidence.value),
    'EVIDENCE_VALUE_INVALID'
  );
  requireValue(
    /^[0-9a-f]{64}$/i.test(evidence.content_hash ?? '') &&
      typeof evidence.locator === 'string' &&
      evidence.locator.includes(evidence.content_hash) &&
      /；价格 \S+；时间 \S+/.test(evidence.locator),
    'ARCHIVE_LOCATOR_MISSING'
  );
  requireValue(
    typeof evidence.excerpt === 'string' &&
      evidence.excerpt.includes('交易状态 ' + modes[config.requirement].state),
    'EVIDENCE_TRADING_STATE_MISSING'
  );
}

async function run(config, progress) {
  progress.phase = 'browser';
  const { chromium } = await import('@playwright/test');
  let browser;
  try {
    const proxy = process.env.VERA_LIVE_HTTP_PROXY;
    browser = await chromium.launch({
      headless: true,
      ...(proxy ? { proxy: { server: proxy } } : {}),
    });
    const context = await browser.newContext({
      storageState: config.state,
      serviceWorkers: 'block',
      acceptDownloads: false,
      viewport: { width: 1440, height: 1000 },
    });
    const page = await context.newPage();
    page.setDefaultTimeout(15000);
    const responses = [];
    let pageErrors = 0;
    let taskCreates = 0;
    page.on('pageerror', () => {
      pageErrors++;
    });
    page.on('response', (response) => {
      responses.push(response);
    });
    page.on('request', (request) => {
      const url = new URL(request.url());
      if (
        url.origin === config.origin &&
        url.pathname === '/api/v1/research-tasks' &&
        request.method() === 'POST'
      )
        taskCreates++;
    });
    const matches = (path, method) => (response) => {
      const url = new URL(response.url());
      return (
        url.origin === config.origin &&
        url.pathname === path &&
        response.request().method() === method
      );
    };
    async function responseAfter(path, method, start, timeout = 15000) {
      const predicate = matches(path, method);
      const found = responses.slice(start).find(predicate);
      return found ?? (await page.waitForResponse(predicate, { timeout }));
    }
    async function read(response, status = 200) {
      requireValue(response.status() === status, 'HTTP_STATUS_REJECTED');
      requireValue(
        response.headers()['content-type']?.split(';')[0].trim() === media,
        'RESEARCH_PROTOCOL_DOWNGRADE'
      );
      requireValue(
        response.request().headers().accept?.includes(media),
        'BROWSER_PROTOCOL_MISMATCH'
      );
      if (response.request().method() === 'POST')
        requireValue(
          response.request().headers()['content-type'] === media,
          'BROWSER_WRITE_PROTOCOL_MISMATCH'
        );
      return response.json();
    }

    progress.phase = 'session';
    await page.goto(config.origin + '/app', { waitUntil: 'domcontentloaded' });
    const me = await read(await responseAfter('/api/v1/me', 'GET', 0));
    requireValue(
      me.onboarded === true && me.synthetic === false && me.research_protocol === 'v0.4',
      'REALTIME_SESSION_REQUIRED'
    );
    requireValue(new URL(page.url()).origin === config.origin, 'UNEXPECTED_NAVIGATION');
    progress.phase = 'selection';
    await page.locator('a[href="/app/quick/stock_analysis"]').click();
    await page.getByRole('heading', { name: '行情数据简报', exact: true }).waitFor();
    await page.getByRole('combobox', { name: '搜索研究对象' }).fill(config.code);
    await page.getByRole('option').filter({ hasText: config.code }).click();
    requireValue(
      await page.getByRole('radio', { name: '最近可用行情', exact: true }).isChecked(),
      'DEFAULT_QUOTE_REQUIREMENT_MISMATCH'
    );
    const strict = config.requirement === 'realtime_trade';
    const radio = page.getByRole('radio', { name: '交易中实时成交行情', exact: true });
    if (strict) await radio.check();
    else requireValue(await radio.isDisabled(), 'UNVERIFIED_REALTIME_OPTION_ENABLED');

    progress.phase = 'plan';
    let start = responses.length;
    await page.getByRole('button', { name: '发起研究', exact: true }).click();
    const planResponse = await responseAfter('/api/v1/research-plans', 'POST', start);
    const input = planResponse.request().postDataJSON();
    requireValue(
      input.skill_id === 'stock_analysis' && input.quote_requirement === config.requirement,
      'REQUEST_INTENT_MISMATCH'
    );
    const result = await read(planResponse);
    requireValue(result.kind === 'plan' && result.plan, 'PLAN_NOT_AVAILABLE');
    const plan = result.plan;
    requireValue(
      plan.subject.id === input.subject_id &&
        plan.subject.symbol === config.code &&
        plan.subject.kind === 'stock',
      'PLAN_SUBJECT_MISMATCH'
    );
    requireValue(
      plan.skill_version === skillVersion &&
        plan.snapshot_mode === 'collect_latest' &&
        plan.quote_requirement === config.requirement &&
        plan.executable === true,
      'REALTIME_PLAN_NOT_EXECUTABLE'
    );
    const review = await read(
      await responseAfter('/api/v1/research-plans/' + plan.id, 'GET', start)
    );
    requireValue(
      review.id === plan.id &&
        review.plan_hash === plan.plan_hash &&
        review.quote_requirement === plan.quote_requirement &&
        review.executable === true,
      'PLAN_REVIEW_MISMATCH'
    );
    await page
      .getByRole('heading', { name: '研究计划：' + plan.subject.name, exact: true })
      .waitFor();
    requireValue(
      (await page.locator('main').innerText()).includes(
        strict
          ? '若无法核实交易中实时成交条件，任务将弃权'
          : '最近可用行情可能是延迟、盘后或休市数据'
      ),
      'PLAN_REALTIME_NOTICE_MISSING'
    );

    progress.phase = 'task';
    start = responses.length;
    const confirmedAt = Date.now();
    await page.getByRole('button', { name: '提交任务', exact: true }).click();
    const taskResponse = await responseAfter('/api/v1/research-tasks', 'POST', start);
    const confirmation = taskResponse.request().postDataJSON();
    requireValue(
      confirmation.plan_id === plan.id && confirmation.plan_hash === plan.plan_hash,
      'CONFIRMATION_BINDING_MISMATCH'
    );
    let task = await read(taskResponse, 202);
    const taskID = task.id;
    const deadline = confirmedAt + config.taskTimeout;
    while (!terminals.has(task.status)) {
      requireValue(Date.now() < deadline, 'TASK_TIMEOUT');
      const response = await responseAfter(
        '/api/v1/research-tasks/' + taskID,
        'GET',
        start,
        Math.max(1, deadline - Date.now())
      );
      start = responses.indexOf(response) + 1;
      task = await read(response);
    }
    requireValue(task.status === 'COMPLETED', 'TASK_DID_NOT_COMPLETE');
    requireValue(
      task.id === taskID &&
        task.subject.id === plan.subject.id &&
        task.quote_requirement === config.requirement &&
        task.snapshot_mode === 'collect_latest' &&
        task.report_ref?.version === 1,
      'TASK_INTENT_MISMATCH'
    );

    progress.phase = 'report';
    start = responses.length;
    await page.getByRole('link', { name: '查看报告', exact: true }).click();
    const report = await read(
      await responseAfter('/api/v1/reports/' + task.report_ref.id + '/versions/1', 'GET', start)
    );
    const { source, evidenceID, age } = validateReport(
      report,
      task,
      plan,
      config,
      confirmedAt,
      Date.now()
    );
    await page.getByRole('heading', { name: report.title, exact: true }).waitFor();
    requireValue(
      (await page.locator('.report-head').innerText()).includes(displayTime(report.snapshot_as_of)),
      'SNAPSHOT_TIME_NOT_DISPLAYED'
    );
    const limits = await page.locator('#limits').innerText();
    requireValue(
      ['event_at', 'received_at', 'available_at'].every((field) =>
        limits.includes(displayTime(source[field]))
      ) &&
        limits.includes(config.provider) &&
        limits.includes(modes[config.requirement].freshnessLabel) &&
        limits.includes(modes[config.requirement].latencyLabel) &&
        limits.includes('交易状态：' + modes[config.requirement].state),
      'SOURCE_METADATA_NOT_DISPLAYED'
    );

    progress.phase = 'evidence';
    start = responses.length;
    await page.getByRole('button', { name: '查看证据 1', exact: true }).click();
    const evidence = await read(
      await responseAfter('/api/v1/evidence/' + evidenceID, 'GET', start)
    );
    validateEvidence(evidence, source, evidenceID, config);
    const panel = page.getByTestId('evidence-panel');
    await panel.getByText(evidence.title, { exact: true }).waitFor();
    const panelText = await panel.innerText();
    requireValue(
      ['event_at', 'received_at', 'available_at'].every((field) =>
        panelText.includes(displayTime(source[field]))
      ) &&
        panelText.includes(config.provider) &&
        panelText.includes(modes[config.requirement].latencyLabel) &&
        panelText.includes('交易状态 ' + modes[config.requirement].state),
      'EVIDENCE_METADATA_NOT_DISPLAYED'
    );
    requireValue(
      pageErrors === 0 && taskCreates === 1 && new URL(page.url()).origin === config.origin,
      'BROWSER_FLOW_INVALID'
    );
    progress.phase = 'complete';
    if (process.env.VERA_LIVE_PRIVATE_RECEIPT) {
      writeFileSync(
        process.env.VERA_LIVE_PRIVATE_RECEIPT,
        JSON.stringify({ report_id: report.id }) + '\n',
        { flag: 'wx', mode: 0o600 }
      );
    }
    return {
      result: 'passed',
      quote_requirement: config.requirement,
      skill_version: skillVersion,
      dataset,
      synthetic: false,
      latency_mode: source.latency_mode,
      status: source.status,
      as_of: plan.as_of,
      snapshot_as_of: report.snapshot_as_of,
      event_at: source.event_at,
      received_at: source.received_at,
      available_at: source.available_at,
      delivered_at: report.delivered_at,
      quote_age_at_freeze_ms: age,
      acceptance_max_age_ms: config.maxAge,
      claims: report.claims.length,
      evidence: 1,
      tasks_created: taskCreates,
      source_verified: true,
    };
  } finally {
    await browser?.close();
  }
}

async function main() {
  const started = Date.now();
  const progress = { phase: 'configuration' };
  try {
    requireValue(
      process.argv
        .slice(2)
        .every((arg) => arg === '--check-config' || arg === '--latest-available'),
      'UNKNOWN_ARGUMENT'
    );
    const config = readConfig(process.env);
    config.requirement = process.argv.includes('--latest-available')
      ? 'latest_available'
      : 'realtime_trade';
    const result = process.argv.includes('--check-config')
      ? { result: 'configuration_valid', live_run: false }
      : await run(config, progress);
    process.stdout.write(JSON.stringify({ ...result, elapsed_ms: Date.now() - started }) + '\n');
  } catch (error) {
    // Playwright exceptions can contain URLs, DOM text or response details.
    // Emit only codes we own, never exception messages/stacks from dependencies.
    const code = error instanceof AcceptanceError ? error.message : 'BROWSER_OR_VALIDATION_FAILED';
    process.stderr.write(
      JSON.stringify({
        result: 'failed',
        phase: progress.phase,
        code,
        elapsed_ms: Date.now() - started,
      }) + '\n'
    );
    process.exitCode = 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href)
  await main();
