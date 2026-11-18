import type { components } from './schema';

export type S<K extends keyof components['schemas']> = components['schemas'][K];
export type Me = S<'Me'>;
export type Entity = S<'Entity'>;
export type Skill = S<'Skill'>;
export type PlanResult = S<'PlanResult'>;
export type ResearchPlan = S<'ResearchPlan'>;
export type Task = S<'Task'>;
export type Report = S<'Report'>;
export type Claim = S<'Claim'>;
export type Section = S<'Section'>;
export type Evidence = S<'Evidence'>;
export type Calculation = S<'Calculation'>;
export type ChartSeries = S<'ChartSeries'>;
export type ReportSummary = S<'ReportSummary'>;
export type WatchItem = S<'WatchItem'>;
export type WatchAlert = S<'WatchAlert'>;
export type SkillId = S<'SkillId'>;
export type QuoteRequirement = NonNullable<ResearchPlan['quote_requirement']>;
export type ResearchProtocol = 'v0.4' | 'observation.v1' | 'legacy';

const mediaTypes: Record<ResearchProtocol, string> = {
  'v0.4': 'application/vnd.vera.v0.4+json',
  'observation.v1': 'application/vnd.vera.observation.v1+json',
  legacy: 'application/json',
};

export const planProtocol = (plan: ResearchPlan): ResearchProtocol =>
  plan.quote_requirement
    ? 'v0.4'
    : plan.snapshot_mode === 'collect_latest'
      ? 'observation.v1'
      : 'legacy';

export const quoteRequirementLabel: Record<QuoteRequirement, string> = {
  realtime_trade: '交易中实时成交行情',
  latest_available: '最近可用行情',
};

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public requestId: string,
    public retryable = false,
    public details?: Record<string, unknown>
  ) {
    super(message);
  }
}

let csrf = '';
let researchProtocol: ResearchProtocol | undefined;
export const setCsrf = (t: string) => {
  csrf = t;
};

export const newKey = () => crypto.randomUUID();

type Opts = { idem?: string; noRedirect?: boolean; protocol?: ResearchProtocol };

export async function api<T>(
  method: string,
  path: string,
  body?: unknown,
  opts: Opts = {}
): Promise<T> {
  const protocol = opts.protocol ?? researchProtocol;
  const apiPath = path.startsWith('/api/v1/');
  const headers: Record<string, string> = {
    Accept: apiPath
      ? protocol
        ? mediaTypes[protocol]
        : `${mediaTypes['v0.4']}, ${mediaTypes['observation.v1']};q=0.8, application/json;q=0.5`
      : 'application/json',
  };
  if (body !== undefined) {
    headers['Content-Type'] =
      protocol &&
      (path === '/api/v1/research-plans' || path === '/api/v1/research-tasks')
        ? mediaTypes[protocol]
        : 'application/json';
  }
  if (method !== 'GET') headers['X-CSRF-Token'] = csrf;
  if (opts.idem) headers['Idempotency-Key'] = opts.idem;
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const e = data?.error ?? {};
    const err = new ApiError(
      res.status,
      e.code ?? 'HTTP_' + res.status,
      e.message ?? '请求失败',
      data?.request_id ?? '',
      e.retryable,
      e.details
    );
    if (!opts.noRedirect) {
      if (res.status === 401) {
        // 未登录：跳到密码登录页（/onboarding），而不是 OIDC 流（GET /auth/login）。
        const from = encodeURIComponent(location.pathname + location.search);
        window.location.assign('/onboarding?return_to=' + from);
      } else if (err.code === 'ONBOARDING_REQUIRED' && location.pathname !== '/onboarding') {
        window.location.assign('/onboarding');
      }
    }
    throw err;
  }
  const advertisedProtocol = data?.research_protocol;
  const expectedProtocol: ResearchProtocol | undefined =
    protocol ??
    (path === '/api/v1/me' &&
    (advertisedProtocol === 'v0.4' || advertisedProtocol === 'observation.v1')
      ? advertisedProtocol
      : undefined);
  if (
    apiPath &&
    expectedProtocol &&
    res.headers.get('Content-Type')?.split(';')[0].trim() !== mediaTypes[expectedProtocol]
  ) {
    throw new Error('服务端数据版本不匹配，请刷新后重试');
  }
  if (path === '/api/v1/me') {
    researchProtocol =
      advertisedProtocol === 'v0.4' || advertisedProtocol === 'observation.v1'
        ? advertisedProtocol
        : 'legacy';
  }
  return data as T;
}

export const get = <T>(p: string, o?: Opts) => api<T>('GET', p, undefined, o);
export const post = <T>(p: string, b: unknown, o?: Opts) => api<T>('POST', p, b, o);

export const skillTitle: Record<string, string> = {
  stock_analysis: '个股分析',
  capital_flow: '资金动向',
  deep_research: '深度研究',
};
export const kindLabel: Record<string, string> = {
  fact: '事实',
  computed: '计算',
  inference: '推断',
  attributed: '引述',
  scenario: '情景',
};
export const statusLabel: Record<string, string> = {
  QUEUED: '排队中',
  COLLECTING: '取数中',
  ANALYZING: '分析中',
  ASSEMBLING: '组装中',
  DELIVERING: '交付中',
  CANCEL_REQUESTED: '取消处理中',
  COMPLETED: '已完成',
  ABSTAINED: '已弃权',
  BUDGET_EXHAUSTED: '预算耗尽',
  CANCELLED: '已取消',
  BLOCKED: '已阻止',
  FAILED: '失败',
};
export const terminal = [
  'COMPLETED',
  'ABSTAINED',
  'BUDGET_EXHAUSTED',
  'CANCELLED',
  'BLOCKED',
  'FAILED',
];

export const fmtTime = (s: string) =>
  new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(s));

export const fmtDataTime = (s: string) =>
  new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).format(new Date(s));
