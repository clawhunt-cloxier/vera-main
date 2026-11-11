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

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public requestId: string,
    public retryable = false,
    public details?: Record<string, unknown>,
  ) {
    super(message);
  }
}

let csrf = '';
export const setCsrf = (t: string) => {
  csrf = t;
};

export const newKey = () => crypto.randomUUID();

type Opts = { idem?: string; noRedirect?: boolean };

export async function api<T>(method: string, path: string, body?: unknown, opts: Opts = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET') headers['X-CSRF-Token'] = csrf;
  if (opts.idem) headers['Idempotency-Key'] = opts.idem;
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'same-origin' });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const e = data?.error ?? {};
    const err = new ApiError(res.status, e.code ?? 'HTTP_' + res.status, e.message ?? '请求失败', data?.request_id ?? '', e.retryable, e.details);
    if (!opts.noRedirect) {
      if (res.status === 401) {
        window.location.assign('/auth/login?return_to=' + encodeURIComponent(location.pathname + location.search));
      } else if (err.code === 'ONBOARDING_REQUIRED' && location.pathname !== '/onboarding') {
        window.location.assign('/onboarding');
      }
    }
    throw err;
  }
  return data as T;
}

export const get = <T,>(p: string, o?: Opts) => api<T>('GET', p, undefined, o);
export const post = <T,>(p: string, b: unknown, o?: Opts) => api<T>('POST', p, b, o);

export const skillTitle: Record<string, string> = { stock_analysis: '个股分析', capital_flow: '资金动向', deep_research: '深度研究' };
export const kindLabel: Record<string, string> = { fact: '事实', computed: '计算', inference: '推断', attributed: '引述', scenario: '情景' };
export const statusLabel: Record<string, string> = {
  QUEUED: '排队中', COLLECTING: '取数中', ANALYZING: '分析中', ASSEMBLING: '组装中', DELIVERING: '交付中', CANCEL_REQUESTED: '取消处理中',
  COMPLETED: '已完成', ABSTAINED: '已弃权', BUDGET_EXHAUSTED: '预算耗尽', CANCELLED: '已取消', BLOCKED: '已阻止', FAILED: '失败',
};
export const terminal = ['COMPLETED', 'ABSTAINED', 'BUDGET_EXHAUSTED', 'CANCELLED', 'BLOCKED', 'FAILED'];

export const fmtTime = (s: string) =>
  new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(s));
