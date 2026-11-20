import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
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
  statusLabel,
  terminal,
  type Entity,
  type PlanResult,
  type ResearchPlan,
  type Skill,
  type SkillId,
  type Task,
} from '../api/client';
import { useMe } from '../api/hooks';
import { EntitySearch } from '../components/EntitySearch';
import { Empty, ErrorBox, Loading, Skeleton, SyntheticBanner, Tag } from '../components/ui';

function supportsMode(schema: unknown, mode: string): boolean {
  const field = (obj: unknown, key: string): unknown =>
    typeof obj === 'object' && obj !== null ? (obj as Record<string, unknown>)[key] : undefined;
  const modes = field(field(field(schema, 'properties'), 'mode'), 'enum');
  return Array.isArray(modes) && modes.includes(mode);
}

export function App() {
  const me = useMe();
  const protocol = newResearchProtocol(me.data);
  const skills = useQuery({
    queryKey: ['skills', protocol],
    queryFn: () => get<{ items: Skill[] }>('/api/v1/skills', { protocol }),
    enabled: !!me.data,
    staleTime: 15_000,
  });
  const tasks = useQuery({
    queryKey: ['tasks'],
    queryFn: () => get<{ items: Task[] }>('/api/v1/research-tasks'),
    staleTime: 5000,
    refetchInterval: 5000,
  });
  const [text, setText] = useState('');
  const nav = useNavigate();
  const plan = useMutation({
    mutationFn: async (candidate?: PlanResult['candidates'][number]) => {
      const r = await post<PlanResult>(
        '/api/v1/research-plans',
        candidate
          ? { skill_id: candidate.skill_id, subject_id: candidate.subject.id, params: {} }
          : { text },
        { idem: newKey(), protocol }
      );
      if (r.kind === 'plan' && r.plan) nav({ to: '/app/plan/$id', params: { id: r.plan.id } });
      return r;
    },
    onSuccess: () => setText(''),
  });
  const synthetic = me.data?.synthetic ?? false;
  const enabledSkills = skills.data?.items.filter((sk) => sk.available) ?? [];
  const stockOnly =
    enabledSkills.length > 0 &&
    enabledSkills.every((sk) => sk.subject_kinds.length === 1 && sk.subject_kinds[0] === 'stock');
  const placeholder = enabledSkills.some((skill) => skill.id === 'capital_flow')
    ? stockOnly
      ? '例如：分析宁德时代最近的资金面'
      : '例如：看看半导体行业最近的资金动向'
    : '例如：个股分析，比亚迪';

  return (
    <main>
      {me.isLoading && <Loading />}
      {me.error && <ErrorBox error={me.error} />}
      {me.data && (
        <>
          {synthetic && <SyntheticBanner />}
          <header className="app-head">
            <strong className="logo">VERA</strong>
            <nav>
              <Link to="/app/watchlist">自选股</Link>
              <Link to="/app/reports">报告</Link>
              <details className="menu">
                <summary aria-label="账户菜单">{me.data?.display_name ?? '账户'}</summary>
                <button
                  type="button"
                  className="ghost small"
                  onClick={async () => {
                    try {
                      await post('/auth/logout', {});
                    } finally {
                      window.location.assign('/');
                    }
                  }}
                >
                  退出登录
                </button>
              </details>
            </nav>
          </header>

          <section className="quick">
            <h1>发起研究</h1>
            <div className="card">
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  plan.mutate();
                }}
              >
                <label htmlFor="queryInput" className="form-label">
                  描述你想要的研究
                </label>
                <textarea
                  id="queryInput"
                  value={text}
                  onChange={(e) => setText(e.target.value)}
                  placeholder={placeholder}
                  rows={3}
                  required
                  maxLength={500}
                  className="form-input"
                />
                <div className="meta" style={{ marginTop: 12 }}>
                  <span className="muted">{text.length}/500</span>
                </div>
                <div className="actions">
                  <button
                    type="submit"
                    disabled={plan.isPending || !text.trim()}
                    className="primary"
                  >
                    {plan.isPending ? '识别中…' : '开始研究'}
                  </button>
                </div>
              </form>
              {plan.error && <ErrorBox error={plan.error} />}
              {plan.data?.kind === 'choose' && (
                <div className="notice">
                  <p>请选择研究对象和研究类型。</p>
                  {plan.data.candidates.length > 0 && (
                    <div className="actions" role="list" aria-label="研究候选项">
                      {plan.data.candidates.map((candidate) => (
                        <button
                          key={`${candidate.skill_id}-${candidate.subject.id}`}
                          type="button"
                          className="secondary"
                          disabled={plan.isPending}
                          onClick={() => plan.mutate(candidate)}
                        >
                          {skillTitle[candidate.skill_id] ?? candidate.skill_id} ·{' '}
                          {candidate.subject.name}
                          {candidate.subject.symbol ? `（${candidate.subject.symbol}）` : ''}
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              )}
              {plan.data?.kind === 'unsupported' && (
                <p className="error">
                  <strong>不支持的需求：</strong> {plan.data.unsupported_reason}
                </p>
              )}
            </div>
          </section>

          <section className="skills">
            <h2>快速启动</h2>
            {skills.error && <ErrorBox error={skills.error} />}
            <div className="grid">
              {skills.isLoading ? (
                Array.from({ length: 3 }).map((_, i) => (
                  <div key={i} className="card small">
                    <Skeleton height={24} />
                    <Skeleton height={16} style={{ marginTop: 12 }} />
                  </div>
                ))
              ) : skills.isSuccess && skills.data.items.length === 0 ? (
                <Empty>暂无可用技能</Empty>
              ) : (
                skills.data?.items.map((sk) => (
                  <Link
                    key={sk.id}
                    to="/app/quick/$skill"
                    params={{ skill: sk.id }}
                    className="card interactive"
                  >
                    <h3>{skillTitle[sk.id]}</h3>
                    {!sk.available && <Tag tone="warn">暂不可用</Tag>}
                  </Link>
                ))
              )}
            </div>
          </section>

          <section className="recent">
            <h2>最近任务</h2>
            {tasks.isLoading && <Loading label="加载任务中…" />}
            {tasks.data?.items.length === 0 && (
              <Empty>尚未发起研究任务。在上方输入需求或选择快速启动试试。</Empty>
            )}
            {tasks.data && tasks.data.items.length > 0 && (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>研究对象</th>
                      <th>技能</th>
                      <th>状态</th>
                      <th>创建时间</th>
                      <th style={{ width: 100 }}>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {tasks.data.items.map((t) => (
                      <tr key={t.id}>
                        <td>
                          <strong>{t.subject.name}</strong>
                        </td>
                        <td>{skillTitle[t.skill_id]}</td>
                        <td>
                          <Tag
                            tone={
                              t.status === 'COMPLETED'
                                ? 'ok'
                                : terminal.includes(t.status)
                                  ? 'bad'
                                  : 'info'
                            }
                          >
                            {statusLabel[t.status]}
                          </Tag>
                        </td>
                        <td>{fmtTime(t.created_at)}</td>
                        <td>
                          <div style={{ display: 'flex', gap: 8 }}>
                            {t.status === 'COMPLETED' && t.report_ref && (
                              <Link
                                to="/app/reports/$id"
                                params={{ id: t.report_ref.id }}
                                className="btn-link"
                              >
                                查看
                              </Link>
                            )}
                            {!terminal.includes(t.status) && (
                              <Link to="/app/tasks/$id" params={{ id: t.id }} className="btn-link">
                                详情
                              </Link>
                            )}
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
    </main>
  );
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
  const [subject, setSubject] = useState<Entity | null>(null);
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
    mutationFn: async () => {
      if (!subject || !available || !s.data?.subject_kinds.includes(subject.kind)) return;
      const r = await post<PlanResult>(
        '/api/v1/research-plans',
        {
          skill_id: skill,
          subject_id: subject.id,
          params: selectedMode ? { mode: selectedMode } : {},
        },
        { idem: newKey(), protocol }
      );
      if (r.kind === 'plan' && r.plan) nav({ to: '/app/plan/$id', params: { id: r.plan.id } });
      return r;
    },
  });
  return (
    <main>
      {me.data?.synthetic && <SyntheticBanner />}
      <p>
        <Link to="/app">← 返回工作台</Link>
      </p>
      <h1>{skillTitle[skill]}</h1>
      {(me.isPending || (me.data && s.isPending)) && <Loading label="加载研究能力…" />}
      {me.error && <ErrorBox error={me.error} />}
      {s.error && <ErrorBox error={s.error} />}
      {me.data && s.isSuccess && !s.isFetching && !available && (
        <p className="notice">当前部署未开放这项研究。</p>
      )}
      {available && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            plan.mutate();
          }}
        >
          <label>
            研究对象
            <EntitySearch
              kinds={s.data?.subject_kinds}
              onPick={setSubject}
              protocol={protocol}
              placeholder={stockOnly ? '输入已登记的股票名称或代码' : undefined}
            />
          </label>
          {skill === 'deep_research' && available && (
            <label>
              模式
              <select value={selectedMode} onChange={(e) => setMode(e.target.value)}>
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
          <button
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
        </form>
      )}
      {plan.error && <ErrorBox error={plan.error} />}
      {plan.data?.kind === 'unsupported' && (
        <p className="muted">这个对象暂不支持：{plan.data.unsupported_reason}</p>
      )}
    </main>
  );
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
    <div className="card">
      <p>你还可以查看{subject.name}的资金动向计划，核对行情和两融数据的可用情况。</p>
      <button type="button" onClick={() => preview.mutate()} disabled={preview.isPending}>
        {preview.isPending ? '正在检查…' : '查看资金动向计划'}
      </button>
      {preview.error && <ErrorBox error={preview.error} />}
    </div>
  );
}

export function PlanReview({ id }: { id: string }) {
  const me = useMe();
  const p = useQuery({
    queryKey: ['plan', id],
    queryFn: async () => {
      // Keep the negotiated session representation first.  A text request
      // can be created through the historical JSON protocol before the
      // server discovers that the selected security needs a Choice
      // collect_latest plan.  In that case the server returns 406 and the
      // observation representation exposes the original confirmation mode.
      try {
        return await get<ResearchPlan>(`/api/v1/research-plans/${id}`);
      } catch (err) {
        if (!(err instanceof ApiError) || err.status !== 406) throw err;
        return get<ResearchPlan>(`/api/v1/research-plans/${id}`, {
          protocol: 'observation.v1',
        });
      }
    },
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
    <main>
      {me.data?.synthetic && <SyntheticBanner />}
      <p>
        <Link to="/app">← 返回工作台</Link>
      </p>
      {p.isLoading && <Loading />}
      {p.error && <ErrorBox error={p.error} />}
      {p.data && (
        <>
          <h1>研究计划：{p.data.subject.name}</h1>
          <div className="meta">
            <Tag>{skillTitle[p.data.skill_id]}</Tag>{' '}
            <span className="muted">
              {p.data.snapshot_mode === 'collect_latest' ? '计划预览时间' : '数据截止'}{' '}
              {fmtTime(p.data.as_of)}
            </span>
            {p.data.quote_requirement && (
              <Tag tone="info">{quoteRequirementLabel[p.data.quote_requirement]}</Tag>
            )}
            {p.data.snapshot_mode === 'collect_latest' && p.data.quote_requirement && (
              <p>
                确认后采集数据，再冻结一次快照。报告分别标明行情事件、接收、可用时点和交易状态。
                {p.data.quote_requirement === 'realtime_trade'
                  ? '若无法核实交易中实时成交条件，任务将弃权。'
                  : '最近可用行情可能是延迟、盘后或休市数据，具体性质将在报告中注明。'}
              </p>
            )}
            {p.data.snapshot_mode === 'collect_latest' && !p.data.quote_requirement && (
              <p>
                {p.data.skill_version === '1.1.0-demo.4'
                  ? '确认后采集 Choice 日成交数据，并按冻结时点取用已发布的 TuShare 数据。报告会列明各项数据的实际来源和期间。'
                  : '确认后采集真实成交数据并冻结快照。这是非实时简报；实时行情和未核实字段不纳入分析。'}
              </p>
            )}
            {p.data.snapshot_mode !== 'collect_latest' && (
              <p className="muted">报告将列明各项数据的来源、日期和覆盖限制。</p>
            )}
          </div>
          {coverage && (
            <p className="coverage-summary">
              <span>{coverage.available} 个可用</span>
              <span>{coverage.limited} 个受限</span>
              <span>{coverage.unavailable} 个不可用</span>
            </p>
          )}
          <form
            onSubmit={(e) => {
              e.preventDefault();
              submit.mutate();
            }}
          >
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
                        {s.id} {s.title}
                      </span>
                      {s.required && <Tag>必需</Tag>}
                      {s.availability === 'limited' && <Tag tone="warn">受限</Tag>}
                      {s.availability === 'unavailable' && <Tag tone="bad">不可用</Tag>}
                    </span>
                    {s.reason && (
                      <span className="muted small section-option-reason">{s.reason}</span>
                    )}
                  </span>
                </label>
              ))}
            </fieldset>
            {!p.data.executable && (
              <p className="notice">
                {p.data.reason_code === 'REQUIRED_SECTION_UNAVAILABLE'
                  ? '必需章节缺少可用数据，本次计划无法提交。'
                  : `该计划当前无法执行${p.data.reason_code ? `（${p.data.reason_code}）` : ''}。`}
              </p>
            )}
            <button type="submit" disabled={submit.isPending || !p.data.executable}>
              {submit.isPending ? '提交中…' : '提交任务'}
            </button>
          </form>
          {submit.error && <ErrorBox error={submit.error} />}
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
