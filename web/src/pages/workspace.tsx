import { useMutation, useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import {
  ArrowDownUp,
  ArrowRight,
  ChartNoAxesCombined,
  Clock3,
  Files,
  Search,
  Star,
} from 'lucide-react';
import { useRef, useState } from 'react';
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
  type ResearchProtocol,
  type Skill,
  type SkillId,
  type Task,
  type WatchItem,
} from '../api/client';
import { useMe } from '../api/hooks';
import { researchError, researchText } from '../api/researchPresentation';
import { Empty, ErrorBox, Loading, Tag, VeraAvatar } from '../components/ui';
import { WorkspaceFrame } from '../components/WorkspaceFrame';
import { ConversationComposer, ConversationHistory, conversationExamples } from './conversations';

const modes = {
  stock_analysis: {
    icon: ChartNoAxesCombined,
    description: '梳理一家公司的财务、估值与近期交易。',
    examples: [{ kind: 'stock', text: '分析宁德时代', demo: '分析演示制造' }],
  },
  capital_flow: {
    icon: ArrowDownUp,
    description: '跟踪资金流向、融资融券与机构持仓。',
    examples: [
      { kind: 'industry', text: '看看半导体行业的资金动向', demo: '看看演示通用设备行业的资金面' },
      { kind: 'stock', text: '看看中国移动的资金动向', demo: '看看演示制造的资金动向' },
      { kind: 'market', text: '全市场资金动向', demo: '全市场资金动向' },
    ],
  },
  deep_research: {
    icon: Search,
    description: '结合财务与披露资料，研究业务、驱动因素和风险。',
    examples: [
      { kind: 'stock', text: '深度研究贵州茅台', demo: '深度研究演示制造' },
      { kind: 'industry', text: '深度研究半导体行业', demo: '深度研究演示通用设备行业' },
    ],
  },
} satisfies Record<
  SkillId,
  {
    icon: typeof Search;
    description: string;
    examples: { kind: Skill['subject_kinds'][number]; text: string; demo: string }[];
  }
>;

function ResearchComposer({
  skills,
  protocol,
  synthetic,
}: {
  skills: Skill[];
  protocol: ResearchProtocol;
  synthetic: boolean;
}) {
  const [text, setText] = useState('');
  const input = useRef<HTMLTextAreaElement>(null);
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
  const examples = skills.flatMap((skill) => {
    if (!skill.available) return [];
    const example = modes[skill.id].examples.find(({ kind }) => skill.subject_kinds.includes(kind));
    return example ? [synthetic ? example.demo : example.text] : [];
  });
  function updateText(value: string) {
    setText(value);
    if (!plan.isIdle) plan.reset();
  }

  return (
    <section aria-label="发起研究">
      <form
        className="desk-compose"
        onSubmit={(event) => {
          event.preventDefault();
          if (text.trim() && !plan.isPending) plan.mutate(undefined);
        }}
      >
        <label htmlFor="queryInput">描述你想要的研究</label>
        <textarea
          id="queryInput"
          ref={input}
          value={text}
          maxLength={500}
          rows={2}
          required
          disabled={plan.isPending}
          placeholder="输入股票简称、代码或研究问题"
          onChange={(event) => updateText(event.target.value)}
        />
        <div className="desk-compose-footer">
          <div className="desk-prompts" aria-label="研究示例">
            {examples.map((example) => (
              <button
                key={example}
                type="button"
                disabled={plan.isPending}
                onClick={() => {
                  updateText(example);
                  input.current?.focus();
                }}
              >
                {example}
              </button>
            ))}
          </div>
          <div className="desk-compose-actions">
            <small>{text.length} / 500</small>
            <button className="primary" type="submit" disabled={plan.isPending || !text.trim()}>
              {plan.isPending ? '正在准备计划…' : '开始研究'}
              <ArrowRight size={16} aria-hidden="true" />
            </button>
          </div>
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
    <section className="desk-recent" aria-labelledby="recent-title">
      <div className="desk-section-head">
        <h2 id="recent-title">最近任务</h2>
        <Link to="/app/reports">
          历史报告 <ArrowRight size={14} aria-hidden="true" />
        </Link>
      </div>
      {tasks.isLoading && <Loading label="正在读取最近任务…" />}
      {tasks.error && <ErrorBox error={tasks.error} />}
      {tasks.data?.items.length === 0 && <Empty>尚未发起研究</Empty>}
      {!!tasks.data?.items.length && (
        <ul className="desk-tasks">
          {tasks.data.items.map((task) => {
            const busy = !terminal.includes(task.status);
            const report = task.status === 'COMPLETED' && task.report_ref;
            return (
              <li key={task.id}>
                <div className="desk-task-subject">
                  <strong>{task.subject.name}</strong>
                  <small>
                    {skillTitle[task.skill_id]} · {fmtTime(task.created_at)}
                  </small>
                </div>
                <span className={'desk-outcome ' + (busy ? 'busy' : report ? 'done' : '')}>
                  {busy && <i aria-hidden="true" />}
                  {taskStatusLabel(task.status)}
                </span>
                {report ? (
                  <Link to="/app/reports/$id" params={{ id: report.id }}>
                    查看报告
                  </Link>
                ) : (
                  <Link to="/app/tasks/$id" params={{ id: task.id }}>
                    查看详情
                  </Link>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function WorkAssistant() {
  const watch = useQuery({
    queryKey: ['watchlist'],
    queryFn: () => get<{ items: WatchItem[] }>('/api/v1/watchlist'),
    staleTime: 10_000,
  });
  return (
    <aside className="desk-assistant" aria-labelledby="assistant-title">
      <h2 id="assistant-title">
        <VeraAvatar size={42} /> 工作助手
      </h2>
      <Link className="desk-assistant-link" to="/app/reports">
        <Files size={17} aria-hidden="true" /> 历史报告 <ArrowRight size={14} aria-hidden="true" />
      </Link>
      <section className="desk-watch" aria-labelledby="watch-title">
        <div className="desk-section-head">
          <h3 id="watch-title">
            <Star size={17} aria-hidden="true" /> 自选股
          </h3>
          <Link to="/app/watchlist">管理</Link>
        </div>
        {watch.isLoading && <Loading label="正在读取自选股…" />}
        {watch.error && <ErrorBox error={watch.error} />}
        {watch.data?.items.length === 0 && <Empty>暂无自选股</Empty>}
        {!!watch.data?.items.length && (
          <ul aria-label="关注标的">
            {watch.data.items.slice(0, 10).map((item) => (
              <li key={item.id}>
                <div className="desk-watch-symbol">
                  <strong>{item.entity.name}</strong>
                  <small>{item.entity.symbol}</small>
                </div>
                {!!item.snapshot?.triggered?.length && <Tag tone="warn">有提醒</Tag>}
              </li>
            ))}
          </ul>
        )}
      </section>
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
        <div className="desk-workbench">
          <div className="desk-primary">
            <header className="desk-title">
              <h1>今天想研究什么？</h1>
              {typeof me.data.quota?.daily_remaining === 'number' && (
                <p className="desk-quota">
                  <Clock3 size={15} aria-hidden="true" />
                  今日剩余 <strong>{me.data.quota.daily_remaining}</strong> 次研究
                </p>
              )}
            </header>
            {me.data.workspace_id && Boolean(me.data.conversation_topics?.length) ? (
              <>
                <ConversationComposer
                  workspace={me.data.workspace_id}
                  examples={conversationExamples(me.data)}
                />
                <details className="chat-full-entry">
                  <summary>用一句话生成完整研报</summary>
                  <ResearchComposer
                    skills={skills.data?.items ?? []}
                    protocol={protocol}
                    synthetic={me.data.synthetic}
                  />
                </details>
              </>
            ) : (
              <ResearchComposer
                skills={skills.data?.items ?? []}
                protocol={protocol}
                synthetic={me.data.synthetic}
              />
            )}
            <section className="desk-skills" aria-labelledby="skills-title">
              <div className="desk-section-head">
                <h2 id="skills-title">快速启动</h2>
              </div>
              {skills.isLoading && <Loading label="正在读取研究类型…" />}
              {skills.error && <ErrorBox error={skills.error} />}
              {skills.data?.items.length === 0 && <Empty>当前没有可用的研究类型。</Empty>}
              <div className="desk-skill-grid">
                {skills.data?.items.map((skill) => {
                  const { icon: Icon, description } = modes[skill.id];
                  return (
                    <Link
                      to="/app/quick/$skill"
                      params={{ skill: skill.id }}
                      key={skill.id}
                      aria-label={skillTitle[skill.id]}
                      aria-describedby={'skill-' + skill.id}
                      className={'desk-skill ' + skill.id}
                    >
                      <span className="desk-skill-icon" aria-hidden="true">
                        <Icon size={21} />
                      </span>
                      <strong>{skillTitle[skill.id]}</strong>
                      <small id={'skill-' + skill.id}>
                        {skill.available ? description : '暂不可用'}
                      </small>
                      <ArrowRight className="desk-skill-arrow" size={16} aria-hidden="true" />
                    </Link>
                  );
                })}
              </div>
            </section>
            <RecentTasks />
          </div>
          <div className="desk-side-tools">
            {me.data.workspace_id && Boolean(me.data.conversation_topics?.length) && (
              <ConversationHistory workspace={me.data.workspace_id} preview />
            )}
            <WorkAssistant />
          </div>
        </div>
      )}
    </WorkspaceFrame>
  );
}
