import { useMutation, useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import {
  fmtTime,
  get,
  newKey,
  newResearchProtocol,
  post,
  skillTitle,
  taskStatusLabel,
  terminal,
  type PlanResult,
  type Skill,
  type Task,
  type WatchItem,
} from '../api/client';
import { useMe } from '../api/hooks';
import { researchError, researchText } from '../api/researchPresentation';
import { Empty, ErrorBox, Loading, Tag } from '../components/ui';
import { WorkspaceFrame } from '../components/WorkspaceFrame';

const descriptions = {
  stock_analysis: '了解一家公司的财务表现、估值与近期交易。',
  capital_flow: '跟踪资金流向、融资融券与机构参与情况。',
  deep_research: '结合财务与披露资料，梳理业务、驱动因素和风险。',
};

function ResearchComposer({ skills }: { skills: Skill[] }) {
  const me = useMe();
  const protocol = newResearchProtocol(me.data);
  const [text, setText] = useState('');
  const nav = useNavigate();
  const plan = useMutation({
    mutationFn: async (candidate?: PlanResult['candidates'][number]) => {
      const result = await post<PlanResult>(
        '/api/v1/research-plans',
        candidate
          ? { skill_id: candidate.skill_id, subject_id: candidate.subject.id, params: {} }
          : { text },
        { idem: newKey(), protocol }
      );
      if (result.kind === 'plan' && result.plan)
        nav({ to: '/app/plan/$id', params: { id: result.plan.id } });
      return result;
    },
  });
  const enabled = skills.filter((skill) => skill.available);
  const example = enabled.some((skill) => skill.id === 'capital_flow')
    ? enabled.some(
        (skill) => skill.id === 'capital_flow' && skill.subject_kinds.includes('industry')
      )
      ? '例如：看看半导体行业最近的资金动向'
      : '例如：分析宁德时代最近的资金面'
    : '例如：个股分析，比亚迪';
  return (
    <section className="desk-compose" aria-labelledby="compose-title">
      <div className="desk-compose-head">
        <h2 id="compose-title">发起研究</h2>
        <p>写下研究对象和想了解的问题，下一步确认研究计划。</p>
      </div>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (text.trim() && !plan.isPending) plan.mutate(undefined);
        }}
      >
        <label htmlFor="queryInput">描述你想要的研究</label>
        <textarea
          id="queryInput"
          value={text}
          maxLength={500}
          rows={2}
          required
          disabled={plan.isPending}
          placeholder={example}
          onChange={(e) => {
            setText(e.target.value);
            if (!plan.isIdle) plan.reset();
          }}
        />
        <div className="desk-compose-actions">
          <small>{text.length} / 500</small>
          <button className="primary" type="submit" disabled={plan.isPending || !text.trim()}>
            {plan.isPending ? '正在准备计划…' : '开始研究'} <span aria-hidden="true">→</span>
          </button>
        </div>
      </form>
      {plan.error && <ErrorBox error={researchError(plan.error)} />}
      {plan.data?.kind === 'choose' && (
        <div className="desk-choice">
          <strong>请选择研究对象和研究类型。</strong>
          <div className="actions" aria-label="研究候选项">
            {plan.data.candidates.map((item) => (
              <button
                type="button"
                key={item.skill_id + item.subject.id}
                onClick={() => plan.mutate(item)}
              >
                {skillTitle[item.skill_id]} · {item.subject.name}
                {item.subject.symbol && '（' + item.subject.symbol + '）'}
              </button>
            ))}
          </div>
          {!plan.data.candidates.length && <p>请补充股票简称或代码，也可以从快速启动选择研究。</p>}
        </div>
      )}
      {plan.data?.kind === 'unsupported' && (
        <p role="status" className="notice">
          不支持的需求：
          {researchText(plan.data.unsupported_reason || '请选择快速启动中的研究类型。')}
        </p>
      )}
    </section>
  );
}

function RecentTasks() {
  const tasks = useQuery({
    queryKey: ['tasks'],
    queryFn: () => get<{ items: Task[] }>('/api/v1/research-tasks'),
    staleTime: 5000,
    refetchInterval: 5000,
  });
  return (
    <section className="desk-panel desk-recent" aria-labelledby="recent-title">
      <div className="desk-section-head">
        <h2 id="recent-title">最近任务</h2>
        <Link to="/app/reports">
          历史报告 <span aria-hidden="true">→</span>
        </Link>
      </div>
      {tasks.isLoading && <Loading label="正在读取最近任务…" />}
      {tasks.error && <ErrorBox error={tasks.error} />}
      {tasks.data?.items.length === 0 && (
        <Empty>尚未发起研究。提交研究计划后，可以在这里查看进度和报告。</Empty>
      )}
      <ul className="desk-tasks">
        {tasks.data?.items.map((t) => {
          const busy = !terminal.includes(t.status);
          const report = t.status === 'COMPLETED' && t.report_ref;
          return (
            <li key={t.id}>
              <div className="desk-task-subject">
                <strong>{t.subject.name}</strong>
                <small>
                  {skillTitle[t.skill_id]} · {fmtTime(t.created_at)}
                </small>
              </div>
              <span
                className={
                  'desk-outcome ' + (busy ? 'busy' : t.status === 'COMPLETED' ? 'done' : '')
                }
              >
                {busy && <i aria-hidden="true" />}
                {taskStatusLabel(t.status)}
              </span>
              {report ? (
                <Link to="/app/reports/$id" params={{ id: report.id }}>
                  查看报告
                </Link>
              ) : (
                <Link to="/app/tasks/$id" params={{ id: t.id }}>
                  查看详情
                </Link>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

function WatchSummary() {
  const watch = useQuery({
    queryKey: ['watchlist'],
    queryFn: () => get<{ items: WatchItem[] }>('/api/v1/watchlist'),
    staleTime: 10_000,
  });
  return (
    <aside className="desk-panel desk-watch" aria-labelledby="watch-title">
      <div className="desk-section-head">
        <h2 id="watch-title">自选股</h2>
        <Link to="/app/watchlist">
          管理 <span aria-hidden="true">→</span>
        </Link>
      </div>
      {watch.isLoading && <Loading label="正在读取自选股…" />}
      {watch.error && <ErrorBox error={watch.error} />}
      {watch.data?.items.length === 0 && <Empty>添加关注的股票后，在这里查看标的和提醒。</Empty>}
      <ul>
        {watch.data?.items.slice(0, 10).map((item) => (
          <li key={item.id}>
            <strong>{item.entity.name}</strong>
            <small>{item.entity.symbol}</small>
            {!!item.snapshot?.triggered?.length && <Tag tone="warn">有提醒</Tag>}
          </li>
        ))}
      </ul>
    </aside>
  );
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
  return (
    <WorkspaceFrame>
      {me.isLoading && <Loading label="正在打开工作台…" />}
      {me.data && (
        <>
          <header className="desk-title">
            <h1>研究工作台</h1>
            {me.data.quota && (
              <p>
                今日剩余研究次数 <strong>{me.data.quota.daily_remaining}</strong>
              </p>
            )}
          </header>
          <ResearchComposer skills={skills.data?.items ?? []} />
          <section className="desk-skills" aria-labelledby="skills-title">
            <h2 id="skills-title">快速启动</h2>
            {skills.isLoading && <Loading label="正在读取研究类型…" />}
            {skills.error && <ErrorBox error={skills.error} />}
            {skills.data?.items.length === 0 && <Empty>当前没有可用的研究类型。</Empty>}
            <div className="desk-skill-grid">
              {skills.data?.items.map((skill) => (
                <Link
                  to="/app/quick/$skill"
                  params={{ skill: skill.id }}
                  key={skill.id}
                  aria-label={skillTitle[skill.id]}
                  aria-describedby={'skill-' + skill.id}
                  className={'desk-skill ' + skill.id}
                >
                  <span className="desk-skill-top">
                    <i aria-hidden="true" className="desk-glyph">
                      <b />
                      <b />
                      <b />
                    </i>
                    <strong>{skillTitle[skill.id]}</strong>
                    <span aria-hidden="true">↗</span>
                  </span>
                  <small id={'skill-' + skill.id}>
                    {skill.available ? descriptions[skill.id] : '暂不可用'}
                  </small>
                </Link>
              ))}
            </div>
          </section>
          <div className="desk-library">
            <RecentTasks />
            <WatchSummary />
          </div>
        </>
      )}
    </WorkspaceFrame>
  );
}
