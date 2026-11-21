import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { useEffect, useRef, useState } from 'react';
import {
  ApiError,
  fmtTime,
  get,
  newKey,
  newResearchProtocol,
  planProtocol,
  post,
  quoteRequirementLabel,
  skillTitle,
  type Entity,
  type PlanResult,
  type ResearchPlan,
  type ResearchProtocol,
  type Skill,
  type SkillId,
  type Task,
} from '../api/client';
import { useMe } from '../api/hooks';
import { preparationFailure, researchError, researchText } from '../api/researchPresentation';
import { EntitySearch } from '../components/EntitySearch';
import { ErrorBox, Loading, SyntheticBanner, Tag } from '../components/ui';
import './research.css';

function supportsMode(schema: unknown, mode: string): boolean {
  const field = (obj: unknown, key: string): unknown =>
    typeof obj === 'object' && obj !== null ? (obj as Record<string, unknown>)[key] : undefined;
  const modes = field(field(field(schema, 'properties'), 'mode'), 'enum');
  return Array.isArray(modes) && modes.includes(mode);
}

export function Quick({ skill }: { skill: SkillId }) {
  const me = useMe();
  const protocol = newResearchProtocol(me.data);
  const s = useQuery({
    queryKey: ['skills', protocol],
    queryFn: () => get<{ items: Skill[] }>('/api/v1/skills', { protocol }),
    staleTime: 15_000,
    enabled: !!me.data,
    select: (d) => d.items.find((x) => x.id === skill),
  });
  const [selection, setSelection] = useState<{ entity: Entity; protocol: ResearchProtocol } | null>(
    null
  );
  const subject = selection?.entity;
  const searchObservation =
    !!me.data?.closing_research_available &&
    (me.data.research_protocol === 'observation.v1' || me.data.research_protocol === 'v0.4');
  const active = useRef<AbortController | null>(null);
  const [preparing, setPreparing] = useState('');
  useEffect(() => () => active.current?.abort(), []);
  const [mode, setMode] = useState('');
  const stockOnly = s.data?.subject_kinds.length === 1 && s.data.subject_kinds[0] === 'stock';
  const allowComparison =
    skill === 'deep_research' &&
    subject?.kind === 'stock' &&
    supportsMode(s.data?.params_schema, 'company_industry');
  const selectedMode = allowComparison && mode === 'company_industry' ? mode : '';
  const available = !!s.data?.available;
  const nav = useNavigate();
  const plan = useMutation({
    mutationFn: async ({
      signal,
      prepareClose,
    }: {
      signal: AbortSignal;
      prepareClose: boolean;
    }) => {
      if (!subject || !available || !s.data?.subject_kinds.includes(subject.kind)) return;
      if (prepareClose) {
        await waitForCloseData(subject, signal, setPreparing);
        setPreparing('');
      }
      signal.throwIfAborted();
      const r = await post<PlanResult>(
        '/api/v1/research-plans',
        {
          skill_id: skill,
          subject_id: subject.id,
          params: selectedMode ? { mode: selectedMode } : {},
        },
        { idem: newKey(), protocol: prepareClose ? 'legacy' : selection.protocol, signal }
      );
      signal.throwIfAborted();
      if (r.kind === 'plan' && r.plan) nav({ to: '/app/plan/$id', params: { id: r.plan.id } });
      return r;
    },
    onSettled: (_result, _error, { signal }) => {
      if (active.current?.signal === signal && !signal.aborted) {
        active.current = null;
        setPreparing('');
      }
    },
  });
  const startPlan = (prepareClose: boolean) => {
    if (active.current) return;
    active.current = new AbortController();
    plan.mutate({ signal: active.current.signal, prepareClose });
  };
  const stopWaiting = () => {
    active.current?.abort();
    active.current = null;
    plan.reset();
    setPreparing('');
  };
  const canOfferBrief =
    searchObservation && skill === 'stock_analysis' && selection?.protocol === 'observation.v1';
  return (
    <main className="research-page">
      {me.data?.synthetic && <SyntheticBanner />}
      <Link to="/app" className="research-back">
        ← 返回工作台
      </Link>
      <header className="research-intro">
        <h1>{skillTitle[skill]}</h1>
        <p>选择研究对象后，可在计划中查看资料覆盖范围并确认章节。</p>
      </header>
      {(me.isPending || (me.data && s.isPending)) && <Loading label="加载研究能力…" />}
      {me.error && <ErrorBox error={researchError(me.error)} />}
      {s.error && <ErrorBox error={researchError(s.error)} />}
      {me.data && s.isSuccess && !s.isFetching && !available && (
        <p className="notice">当前部署未开放这项研究。</p>
      )}
      {available && (
        <form
          className="research-panel"
          onSubmit={(e) => {
            e.preventDefault();
            startPlan(searchObservation && subject?.kind === 'stock');
          }}
        >
          <label>
            研究对象
            <EntitySearch
              kinds={s.data?.subject_kinds}
              onPick={(entity, source) => setSelection({ entity, protocol: source ?? protocol })}
              onEdit={() => {
                stopWaiting();
                setSelection(null);
              }}
              protocol={protocol}
              searchObservation={searchObservation}
              placeholder={stockOnly ? '输入 A 股简称或代码' : undefined}
            />
          </label>
          {skill === 'deep_research' && available && (
            <label>
              模式
              <select
                value={selectedMode}
                disabled={plan.isPending}
                onChange={(e) => setMode(e.target.value)}
              >
                <option value="">
                  {stockOnly || subject?.kind === 'stock'
                    ? '公司深度研究'
                    : subject?.kind === 'industry'
                      ? '行业深度研究'
                      : '按研究对象确定'}
                </option>
                {allowComparison && <option value="company_industry">公司 + 所在行业对比</option>}
              </select>
            </label>
          )}
          <div className="research-actions">
            <button
              className="research-primary"
              type="submit"
              disabled={
                !available ||
                !subject ||
                !s.data?.subject_kinds.includes(subject.kind) ||
                plan.isPending
              }
            >
              {plan.isPending ? '准备中…' : '发起研究'}
            </button>
            {canOfferBrief && (
              <button
                type="button"
                className="research-secondary"
                disabled={plan.isPending}
                onClick={() => startPlan(false)}
              >
                查看成交简报
              </button>
            )}
          </div>
          {canOfferBrief && !plan.isPending && (
            <p className="research-help">只想了解近期成交情况？成交简报提供已核实的日成交数据。</p>
          )}
          {plan.isPending && (
            <div className="research-wait">
              <div role="status" aria-live="polite">
                <span className="research-ring" aria-hidden="true" />
                <div>
                  <strong>
                    {preparing || `正在为${subject?.name ?? '所选对象'}准备研究计划…`}
                  </strong>
                  {preparing && <p>首次准备可能需要几分钟，请保持页面打开。</p>}
                </div>
              </div>
              <button type="button" onClick={stopWaiting}>
                停止等待
              </button>
            </div>
          )}
          {plan.error && <ErrorBox error={researchError(plan.error)} />}
          {plan.data?.kind === 'unsupported' && (
            <p className="notice">
              这个对象暂不支持本项研究。{researchText(plan.data.unsupported_reason ?? '')}
            </p>
          )}
        </form>
      )}
    </main>
  );
}

async function waitForCloseData(
  subject: Entity,
  signal: AbortSignal,
  onPending: (message: string) => void
) {
  const endpoint = '/api/v1/research-subjects/' + encodeURIComponent(subject.id) + '/prepare';
  let delay = 5000;
  for (;;) {
    try {
      const result = await post<{ status: 'pending' | 'ready' | 'failed'; message?: string }>(
        endpoint,
        {},
        { protocol: 'legacy', signal }
      );
      signal.throwIfAborted();
      if (result.status === 'ready') return;
      if (result.status !== 'pending') throw new Error(preparationFailure(result.message));
      delay = 5000;
      onPending(`正在为${subject.name}准备研究计划…`);
    } catch (error) {
      signal.throwIfAborted();
      if (!(error instanceof ApiError && error.status === 429 && error.retryable)) throw error;
      const retryAfter = error.retryAfter;
      const retryDelay =
        retryAfter && /^\d+$/.test(retryAfter)
          ? Number(retryAfter) * 1000
          : Date.parse(retryAfter ?? '') - Date.now();
      delay = Number.isFinite(retryDelay)
        ? Math.max(5000, retryDelay)
        : Math.min(delay * 2, 60_000);
      // Browsers treat delays above the signed 32-bit timer range as immediate.
      delay = Math.min(delay, 2_147_483_647);
      onPending('当前研究请求较多，页面将自动重试。');
    }
    await new Promise<void>((resolve) => {
      const abort = () => {
        clearTimeout(timer);
        resolve();
      };
      const timer = setTimeout(() => {
        signal.removeEventListener('abort', abort);
        resolve();
      }, delay);
      signal.addEventListener('abort', abort, { once: true });
    });
    signal.throwIfAborted();
  }
}

function CapitalFlowAlternative({ subject }: { subject: Entity }) {
  const nav = useNavigate();
  const skills = useQuery({
    queryKey: ['skills', 'legacy'],
    queryFn: () => get<{ items: Skill[] }>('/api/v1/skills', { protocol: 'legacy' }),
    staleTime: 0,
  });
  const preview = useMutation({
    mutationFn: async () => {
      const result = await post<PlanResult>(
        '/api/v1/research-plans',
        { skill_id: 'capital_flow', subject_id: subject.id, params: {} },
        { idem: newKey(), protocol: 'legacy' }
      );
      if (result.kind !== 'plan' || !result.plan)
        throw new Error(result.unsupported_reason || '当前无法为这只股票创建资金动向计划');
      return result.plan;
    },
    onSuccess: (plan) => nav({ to: '/app/plan/$id', params: { id: plan.id } }),
  });
  const supported = skills.data?.items.some(
    (skill) =>
      skill.id === 'capital_flow' && skill.available && skill.subject_kinds.includes(subject.kind)
  );
  if (skills.isFetching || skills.isError || !supported) return null;
  return (
    <aside className="research-alternative">
      <p>你还可以查看{subject.name}的资金动向计划，核对行情和两融数据的可用情况。</p>
      <button
        type="button"
        className="research-secondary"
        onClick={() => preview.mutate()}
        disabled={preview.isPending}
      >
        {preview.isPending ? '正在检查…' : '查看资金动向计划'}
      </button>
      {preview.error && <ErrorBox error={researchError(preview.error)} />}
    </aside>
  );
}

export function PlanReview({ id }: { id: string }) {
  const me = useMe();
  const p = useQuery({
    queryKey: ['plan', id],
    queryFn: () => get<ResearchPlan>(`/api/v1/research-plans/${id}`),
    staleTime: 60_000,
  });
  const [disabled, setDisabled] = useState<string[]>([]);
  const coverage = p.data?.sections.reduce(
    (counts, section) => {
      counts[section.availability]++;
      return counts;
    },
    { available: 0, limited: 0, unavailable: 0 }
  );
  const qc = useQueryClient();
  const nav = useNavigate();
  const submit = useMutation({
    mutationFn: async () => {
      const t = await post<Task>(
        '/api/v1/research-tasks',
        { plan_id: id, plan_hash: p.data!.plan_hash, disabled_sections: disabled },
        { idem: newKey(), protocol: planProtocol(p.data!) }
      );
      qc.invalidateQueries({ queryKey: ['tasks'] });
      nav({ to: '/app/tasks/$id', params: { id: t.id } });
    },
  });
  const toggle = (sid: string) => {
    const sec = p.data?.sections.find((s) => s.id === sid);
    if (!sec || sec.required || sec.availability === 'unavailable') return;
    setDisabled((d) => (d.includes(sid) ? d.filter((x) => x !== sid) : [...d, sid]));
  };
  return (
    <main className="research-page">
      {me.data?.synthetic && <SyntheticBanner />}
      <Link to="/app" className="research-back">
        ← 返回工作台
      </Link>
      {p.isLoading && <Loading />}
      {p.error && <ErrorBox error={researchError(p.error)} />}
      {p.data && (
        <>
          <header className="research-intro">
            <h1>研究计划：{p.data.subject.name}</h1>
            <p>确认要纳入报告的章节，然后提交研究。</p>
          </header>
          <div className="research-plan-summary">
            <div className="research-plan-meta">
              <Tag>{skillTitle[p.data.skill_id]}</Tag>
              <span className="muted">
                {p.data.snapshot_mode === 'collect_latest' ? '计划预览时间' : '数据截止'}{' '}
                {fmtTime(p.data.as_of)}
              </span>
              {p.data.quote_requirement && (
                <Tag tone="info">{quoteRequirementLabel[p.data.quote_requirement]}</Tag>
              )}
            </div>
            {p.data.snapshot_mode === 'collect_latest' && p.data.quote_requirement && (
              <p>
                提交后获取行情，报告将标明交易、接收与数据可用的时间。
                {p.data.quote_requirement === 'realtime_trade'
                  ? '如果无法核实实时成交行情，本次研究将无法交付。'
                  : '最近可用行情可能是延迟、盘后或休市数据，具体性质将在报告中注明。'}
              </p>
            )}
            {p.data.snapshot_mode === 'collect_latest' && !p.data.quote_requirement && (
              <p>
                {p.data.skill_version === '1.1.0-demo.4'
                  ? '确认后获取日成交数据，并取用截止时已发布的收盘资料。报告会列明各项数据的来源和期间。'
                  : '确认后采集真实成交数据并冻结快照。这是非实时简报；实时行情和未核实字段不纳入分析。'}
              </p>
            )}
            {p.data.snapshot_mode !== 'collect_latest' && (
              <p className="muted">报告将列明各项数据的来源、日期和覆盖限制。</p>
            )}
          </div>
          <form
            className="research-panel"
            onSubmit={(e) => {
              e.preventDefault();
              submit.mutate();
            }}
          >
            {coverage && (
              <p className="coverage-summary">
                <span>{coverage.available} 个可用</span>
                <span>{coverage.limited} 个受限</span>
                <span>{coverage.unavailable} 个不可用</span>
              </p>
            )}
            <fieldset className="sections">
              <legend>章节</legend>
              {p.data.sections.map((s) => (
                <label
                  key={s.id}
                  className={disabled.includes(s.id) ? 'section-option muted' : 'section-option'}
                >
                  <input
                    type="checkbox"
                    checked={s.availability !== 'unavailable' && !disabled.includes(s.id)}
                    onChange={() => toggle(s.id)}
                    disabled={s.availability === 'unavailable' || s.required}
                  />
                  <span className="section-option-body">
                    <span className="section-option-title">
                      <span>
                        {s.id} {researchText(s.title)}
                      </span>
                      {s.required && <Tag>必需</Tag>}
                      {s.availability === 'limited' && <Tag tone="warn">受限</Tag>}
                      {s.availability === 'unavailable' && <Tag tone="bad">不可用</Tag>}
                    </span>
                    {s.reason && (
                      <span className="muted small section-option-reason">
                        {researchText(s.reason)}
                      </span>
                    )}
                  </span>
                </label>
              ))}
            </fieldset>
            {!p.data.executable && (
              <p className="notice">
                {p.data.reason_code === 'REQUIRED_SECTION_UNAVAILABLE'
                  ? '必需章节缺少可用数据，本次计划无法提交。'
                  : '暂时无法提交此计划。请检查章节说明，或返回工作台选择其他研究。'}
              </p>
            )}
            <div className="research-plan-footer">
              {p.data.estimate && (
                <p>
                  参考用时约 {Math.max(1, Math.ceil(p.data.estimate.p95_seconds / 60))} 分钟 · 配额{' '}
                  {p.data.estimate.quota_cost} 次
                </p>
              )}
              <button
                type="submit"
                className="research-primary"
                disabled={submit.isPending || !p.data.executable}
              >
                {submit.isPending ? '提交中…' : '提交任务'}
              </button>
            </div>
          </form>
          {submit.error && <ErrorBox error={researchError(submit.error)} />}
          {me.data &&
            newResearchProtocol(me.data) === 'legacy' &&
            !p.data.executable &&
            p.data.reason_code === 'REQUIRED_SECTION_UNAVAILABLE' &&
            planProtocol(p.data) === 'legacy' &&
            (p.data.skill_id === 'stock_analysis' || p.data.skill_id === 'deep_research') &&
            p.data.subject.kind === 'stock' &&
            p.data.subject.skills.some((sk) => sk.skill_id === 'capital_flow' && sk.available) && (
              <CapitalFlowAlternative subject={p.data.subject} />
            )}
        </>
      )}
    </main>
  );
}
