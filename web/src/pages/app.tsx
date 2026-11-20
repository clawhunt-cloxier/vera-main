import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import {
  combinedResearch,
  fmtTime,
  get,
  newKey,
  planProtocol,
  post,
  quoteRequirementLabel,
  skillTitle,
  statusLabel,
  terminal,
  type Entity,
  type PlanResult,
  type QuoteRequirement,
  type ResearchPlan,
  type ResearchProtocol,
  type Skill,
  type SkillId,
  type Task,
} from '../api/client';
import { useMe } from '../api/hooks';
import { EntitySearch } from '../components/EntitySearch';
import { QuoteRequirementField } from '../components/QuoteRequirementField';
import { ResearchSourceField } from '../components/ResearchSourceField';
import { Empty, ErrorBox, Loading, Skeleton, SyntheticBanner, Tag } from '../components/ui';

function supportsMode(schema: unknown, mode: string): boolean {
  const field = (obj: unknown, key: string): unknown =>
    typeof obj === 'object' && obj !== null ? (obj as Record<string, unknown>)[key] : undefined;
  const modes = field(field(field(schema, 'properties'), 'mode'), 'enum');
  return Array.isArray(modes) && modes.includes(mode);
}

export function App() {
  const me = useMe();
  const combined = combinedResearch(me.data);
  const [source, setSource] = useState<ResearchProtocol>('legacy');
  const protocol: ResearchProtocol = combined ? source : (me.data?.research_protocol ?? 'legacy');
  const registryProtocol = combined ? 'legacy' : protocol;
  const realtime = protocol === 'v0.4';
  const skills = useQuery({
    queryKey: ['skills', registryProtocol],
    queryFn: () => get<{ items: Skill[] }>('/api/v1/skills', { protocol: registryProtocol }),
    enabled: !!me.data,
    staleTime: 3600_000,
  });
  const tasks = useQuery({
    queryKey: ['tasks'],
    queryFn: () => get<{ items: Task[] }>('/api/v1/research-tasks'),
    staleTime: 5000,
    refetchInterval: 5000,
  });
  const [text, setText] = useState('');
  const [selection, setQuoteRequirement] = useState<QuoteRequirement>('latest_available');
  const allowRealtime = me.data?.realtime_trade_available === true;
  const quoteRequirement = allowRealtime ? selection : 'latest_available';
  const nav = useNavigate();
  const plan = useMutation({
    mutationFn: async () => {
      if (realtime && !quoteRequirement) throw new Error('请选择行情要求');
      const r = await post<PlanResult>(
        '/api/v1/research-plans',
        { text, ...(realtime ? { quote_requirement: quoteRequirement } : {}) },
        { idem: newKey(), protocol }
      );
      if (r.kind === 'plan' && r.plan) nav({ to: '/app/plan/$id', params: { id: r.plan.id } });
      return r;
    },
    onSuccess: () => setText(''),
  });
  const synthetic = me.data?.synthetic ?? false;
  const observation = protocol === 'observation.v1';
  const enabledSkills = skills.data?.items.filter((sk) => sk.available) ?? [];
  const stockOnly =
    enabledSkills.length > 0 &&
    enabledSkills.every((sk) => sk.subject_kinds.length === 1 && sk.subject_kinds[0] === 'stock');
  const placeholders = [
    '分析宁德时代最近的资金面',
    '深度研究贵州茅台',
    ...(stockOnly ? [] : ['看看半导体行业最近的资金动向']),
    '个股分析：比亚迪',
  ];
  const [placeholderIdx, setPlaceholderIdx] = useState(0);

  return (
    <main>
      {me.isLoading && <Loading />}
      {(me.isError || (!me.isLoading && !me.data)) && null}
      {me.data && (
        <>
          {synthetic && <SyntheticBanner />}
          {observation && !combined && (
            <p className="notice">
              当前可生成已接入股票的真实成交数据简报，包含成交量、成交额和换手率。使用已取得的日成交数据，非盘中实时行情；实时行情与财务分析尚未开放。
            </p>
          )}
          {realtime && (
            <p className="notice">
              请选择行情要求。确认后采集并冻结一次数据快照，报告会分别显示行情实际时点与来源限制。
            </p>
          )}
          {!combined && !synthetic && protocol === 'legacy' && stockOnly && (
            <p className="notice">
              可研究已登记的 A 股个股。行情采用收盘数据，财报与公告按计划截止时点取用。
            </p>
          )}
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
                {combined && (
                  <ResearchSourceField
                    value={source}
                    onChange={(value) => {
                      setSource(value);
                      plan.reset();
                    }}
                    snapshot={me.data.research_protocol === 'v0.4'}
                  />
                )}
                <label htmlFor="queryInput" className="form-label">
                  描述你想要的研究
                </label>
                <textarea
                  id="queryInput"
                  value={text}
                  onChange={(e) => setText(e.target.value)}
                  placeholder={
                    observation
                      ? '输入已接入的股票名称或代码，生成真实成交简报'
                      : placeholders[placeholderIdx] || '输入研究需求…'
                  }
                  onFocus={() => setPlaceholderIdx((i) => (i + 1) % placeholders.length)}
                  rows={3}
                  required
                  maxLength={500}
                  className="form-input"
                />
                <div className="meta" style={{ marginTop: 12 }}>
                  <span className="muted">{text.length}/500</span>
                </div>
                {realtime && (
                  <QuoteRequirementField
                    value={quoteRequirement}
                    onChange={setQuoteRequirement}
                    allowRealtime={allowRealtime}
                  />
                )}
                <div className="actions">
                  <button
                    type="submit"
                    disabled={plan.isPending || !text.trim() || (realtime && !quoteRequirement)}
                    className="primary"
                  >
                    {plan.isPending ? '识别中…' : '开始研究'}
                  </button>
                </div>
              </form>
              {plan.error && <ErrorBox error={plan.error} />}
              {plan.data?.kind === 'choose' && (
                <p className="notice">
                  您的需求对应多个可能的研究对象，请在下方快速启动中选择具体对象。
                </p>
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
              {(combined || realtime) && (
                <Link
                  to="/app/quick/$skill"
                  params={{ skill: 'daily-observation' }}
                  className="card interactive"
                >
                  <h3>{combined ? '日成交简报（Choice + TuShare）' : 'Choice 日成交简报'}</h3>
                </Link>
              )}
              {combined && me.data.research_protocol === 'v0.4' && (
                <Link
                  to="/app/quick/$skill"
                  params={{ skill: 'choice-quote' }}
                  className="card interactive"
                >
                  <h3>Choice 行情快照</h3>
                </Link>
              )}
              {skills.isLoading ? (
                Array.from({ length: 4 }).map((_, i) => (
                  <div key={i} className="card small">
                    <Skeleton height={24} />
                    <Skeleton height={16} style={{ marginTop: 12 }} />
                  </div>
                ))
              ) : skills.data?.items.length === 0 ? (
                <Empty>暂无可用技能</Empty>
              ) : (
                skills.data?.items.map((sk) => (
                  <Link
                    key={sk.id}
                    to="/app/quick/$skill"
                    params={{ skill: sk.id as any }}
                    className="card interactive"
                  >
                    <h3>{sk.title}</h3>
                    {combined && <span className="muted">TuShare 收盘研究</span>}
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

export function Quick({
  skill,
  daily = false,
  quote = false,
}: {
  skill: SkillId;
  daily?: boolean;
  quote?: boolean;
}) {
  const me = useMe();
  const combined = combinedResearch(me.data);
  const protocol: ResearchProtocol = daily
    ? 'observation.v1'
    : quote
      ? 'v0.4'
      : combined
        ? 'legacy'
        : (me.data?.research_protocol ?? 'legacy');
  const realtime = protocol === 'v0.4';
  const routeAvailable =
    (!daily || !!me.data?.research_protocol) &&
    (!quote || (combined && me.data?.research_protocol === 'v0.4'));
  const s = useQuery({
    queryKey: ['skills', protocol],
    queryFn: () => get<{ items: Skill[] }>('/api/v1/skills', { protocol }),
    staleTime: 3600_000,
    enabled: !!me.data && routeAvailable,
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
  const available = routeAvailable && !!s.data?.available;
  const [selection, setQuoteRequirement] = useState<QuoteRequirement>('latest_available');
  const allowRealtime = me.data?.realtime_trade_available === true;
  const quoteRequirement = allowRealtime ? selection : 'latest_available';
  const nav = useNavigate();
  const plan = useMutation({
    mutationFn: async () => {
      if (!subject || !available || !s.data?.subject_kinds.includes(subject.kind)) return;
      if (realtime && !quoteRequirement) throw new Error('请选择行情要求');
      const r = await post<PlanResult>(
        '/api/v1/research-plans',
        {
          skill_id: skill,
          subject_id: subject.id,
          params: selectedMode ? { mode: selectedMode } : {},
          ...(realtime ? { quote_requirement: quoteRequirement } : {}),
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
      <h1>
        {daily
          ? combined
            ? '日成交简报（Choice + TuShare）'
            : 'Choice 日成交简报'
          : quote
            ? 'Choice 行情快照'
            : s.data?.title}
      </h1>
      {daily && combined && (
        <p className="muted">
          成交数据来自 Choice。已登记股票可补充 TuShare 数据，实际引用来源见报告。
        </p>
      )}
      {s.error && <ErrorBox error={s.error} />}
      {me.data && !s.isFetching && !s.error && !available && (
        <p className="notice">当前部署未开放这项研究。</p>
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          plan.mutate();
        }}
      >
        <label>
          研究对象
          {available && (
            <EntitySearch
              kinds={s.data?.subject_kinds}
              onPick={setSubject}
              protocol={protocol}
              placeholder={stockOnly ? '输入已登记的股票名称或代码' : undefined}
            />
          )}
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
        {realtime && (
          <QuoteRequirementField
            value={quoteRequirement}
            onChange={setQuoteRequirement}
            allowRealtime={allowRealtime}
          />
        )}
        <button
          type="submit"
          disabled={
            !available ||
            !subject ||
            !s.data?.subject_kinds.includes(subject.kind) ||
            plan.isPending ||
            (realtime && !quoteRequirement)
          }
        >
          {plan.isPending ? '准备中…' : '发起研究'}
        </button>
      </form>
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
    queryFn: () => get<ResearchPlan>(`/api/v1/research-plans/${id}`),
    staleTime: 60_000,
  });
  const [disabled, setDisabled] = useState<string[]>([]);
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
          </div>
          {p.data.executable && (
            <p>
              计划生成 {p.data.sections.length} 个章节，你可以取消勾选不需要的章节再提交。
              {p.data.sections.some((s) => s.availability === 'limited') &&
                '部分章节因数据不足而受限。'}
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
                <label key={s.id} className={disabled.includes(s.id) ? 'muted' : ''}>
                  <input
                    type="checkbox"
                    checked={s.availability !== 'unavailable' && !disabled.includes(s.id)}
                    onChange={() => toggle(s.id)}
                    disabled={s.availability === 'unavailable' || s.required}
                  />
                  {s.id} {s.title} {s.availability === 'limited' && <Tag tone="warn">受限</Tag>}{' '}
                  {s.availability === 'unavailable' && <Tag tone="bad">不可用</Tag>}
                  {s.reason && <span className="muted"> — {s.reason}</span>}
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
            ((me.data.research_protocol ?? 'legacy') === 'legacy' || combinedResearch(me.data)) &&
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
