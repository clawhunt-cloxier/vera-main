import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { fmtTime, get, newKey, post, skillTitle, statusLabel, terminal, type PlanResult, type ResearchPlan, type S, type Skill, type SkillId, type Task } from '../api/client';
import { useMe } from '../api/hooks';
import { EntitySearch } from '../components/EntitySearch';
import { Empty, ErrorBox, Loading, SyntheticBanner, Tag } from '../components/ui';

export function App() {
  const me = useMe();
  const skills = useQuery({ queryKey: ['skills'], queryFn: () => get<{ skills: Skill[] }>('/api/v1/skills'), staleTime: 3600_000 });
  const tasks = useQuery({ queryKey: ['tasks'], queryFn: () => get<{ items: Task[] }>('/api/v1/research-tasks'), staleTime: 5000, refetchInterval: 5000 });
  const [text, setText] = useState('');
  const qc = useQueryClient();
  const nav = useNavigate();
  const plan = useMutation({
    mutationFn: async () => {
      const r = await post<PlanResult>('/api/v1/research-plans', { text }, { idem: newKey() });
      if (r.kind === 'plan') nav({ to: '/app/plan/$id', params: { id: r.plan.id } });
      return r;
    },
    onSuccess: () => setText(''),
  });
  const synthetic = me.data?.synthetic ?? false;
  return (
    <main>
      {synthetic && <SyntheticBanner />}
      <header className="app-head">
        <strong className="logo">VERA</strong>
        <nav>
          <Link to="/app/watchlist">自选股</Link>
          <Link to="/app/reports">报告</Link>
          <details className="menu">
            <summary aria-label="账户菜单">{me.data?.display_name ?? '账户'}</summary>
            <a href="/auth/logout">退出登录</a>
          </details>
        </nav>
      </header>
      <section className="quick">
        <h1>研究</h1>
        <form onSubmit={(e) => { e.preventDefault(); plan.mutate(); }}>
          <textarea value={text} onChange={(e) => setText(e.target.value)} placeholder="输入研究需求，例如：深度研究演示制造" rows={2} required />
          <button type="submit" disabled={plan.isPending || !text.trim()}>{plan.isPending ? '识别中…' : '开始研究'}</button>
        </form>
        {plan.error && <ErrorBox error={plan.error} />}
        {plan.data?.kind === 'not_understood' && <p className="muted">未识别出具体研究对象或技能，请换个说法重试。</p>}
        {plan.data?.kind === 'not_supported' && <p className="muted">这个任务暂不支持：{plan.data.reason}</p>}
      </section>
      <section className="skills">
        <h2>快速启动</h2>
        <div className="grid">
          {skills.data?.skills.map((sk) => (
            <Link key={sk.id} to="/app/quick/$skill" params={{ skill: sk.id as SkillId }} className="card">
              <h3>{sk.name}</h3>
              <p>{sk.description}</p>
            </Link>
          ))}
        </div>
      </section>
      <section className="recent">
        <h2>进行中与最近完成</h2>
        {tasks.isLoading && <Loading />}
        {tasks.data?.items.length === 0 && <Empty>尚未发起研究任务。</Empty>}
        {tasks.data && tasks.data.items.length > 0 && (
          <table>
            <thead><tr><th>对象</th><th>技能</th><th>状态</th><th>创建时间</th><th></th></tr></thead>
            <tbody>
              {tasks.data.items.map((t) => (
                <tr key={t.id}>
                  <td>{t.subject.name}</td>
                  <td>{skillTitle[t.skill_id]}</td>
                  <td><Tag tone={t.status === 'COMPLETED' ? 'ok' : terminal.includes(t.status) ? 'bad' : 'info'}>{statusLabel[t.status]}</Tag></td>
                  <td>{fmtTime(t.created_at)}</td>
                  <td>
                    {t.status === 'COMPLETED' && t.report_ref && <Link to="/app/reports/$id" params={{ id: t.report_ref.id }}>查看报告</Link>}
                    {!terminal.includes(t.status) && <Link to="/app/tasks/$id" params={{ id: t.id }}>详情</Link>}
                    {terminal.includes(t.status) && t.status !== 'COMPLETED' && <button className="ghost small" onClick={() => { if (confirm('重新运行？')) qc.invalidateQueries({ queryKey: ['tasks'] }); }}>重试</button>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </main>
  );
}

export function Quick({ skill }: { skill: SkillId }) {
  const me = useMe();
  const s = useQuery({ queryKey: ['skill', skill], queryFn: () => get<Skill>(`/api/v1/skills/${skill}`), staleTime: 3600_000 });
  const [subject, setSubject] = useState<S<'Entity'] | null>(null);
  const [mode, setMode] = useState('');
  const nav = useNavigate();
  const plan = useMutation({
    mutationFn: async () => {
      if (!subject) return;
      const r = await post<PlanResult>('/api/v1/research-plans', { text: `${s.data!.name} ${subject.name}`, params: mode ? { mode } : undefined }, { idem: newKey() });
      if (r.kind === 'plan') nav({ to: '/app/plan/$id', params: { id: r.plan.id } });
      return r;
    },
  });
  return (
    <main>
      {me.data?.synthetic && <SyntheticBanner />}
      <p><Link to="/app">← 返回工作台</Link></p>
      <h1>{s.data?.name}</h1>
      <p>{s.data?.description}</p>
      <form onSubmit={(e) => { e.preventDefault(); plan.mutate(); }}>
        <label>研究对象<EntitySearch kinds={s.data?.subject_kinds} onPick={setSubject} /></label>
        {skill === 'deep_research' && (
          <label>
            模式
            <select value={mode} onChange={(e) => setMode(e.target.value)}>
              <option value="">标准</option>
              <option value="concise">精简（跳过经营分析、竞争格局与风险因素）</option>
            </select>
          </label>
        )}
        <button type="submit" disabled={!subject || plan.isPending}>{plan.isPending ? '准备中…' : '发起研究'}</button>
      </form>
      {plan.error && <ErrorBox error={plan.error} />}
      {plan.data?.kind === 'not_supported' && <p className="muted">这个对象暂不支持：{plan.data.reason}</p>}
    </main>
  );
}

export function PlanReview({ id }: { id: string }) {
  const me = useMe();
  const p = useQuery({ queryKey: ['plan', id], queryFn: () => get<ResearchPlan>(`/api/v1/research-plans/${id}`), staleTime: 60_000 });
  const [disabled, setDisabled] = useState<string[]>([]);
  const qc = useQueryClient();
  const nav = useNavigate();
  const submit = useMutation({
    mutationFn: async () => {
      const t = await post<Task>('/api/v1/research-tasks', { plan_id: id, plan_hash: p.data!.plan_hash, disabled_sections: disabled }, { idem: newKey() });
      qc.invalidateQueries({ queryKey: ['tasks'] });
      nav({ to: '/app/tasks/$id', params: { id: t.id } });
    },
  });
  const toggle = (sid: string) => setDisabled((d) => (d.includes(sid) ? d.filter((x) => x !== sid) : [...d, sid]));
  return (
    <main>
      {me.data?.synthetic && <SyntheticBanner />}
      <p><Link to="/app">← 返回工作台</Link></p>
      {p.isLoading && <Loading />}
      {p.data && (
        <>
          <h1>研究计划：{p.data.subject.name}</h1>
          <div className="meta"><Tag>{skillTitle[p.data.skill_id]}</Tag> <span className="muted">数据截止 {p.data.data_as_of}</span></div>
          <p>计划生成 {p.data.sections.length} 个章节，你可以取消勾选不需要的章节再提交。{p.data.result_kind === 'limited' && '部分章节因数据不足而受限。'}</p>
          <form onSubmit={(e) => { e.preventDefault(); submit.mutate(); }}>
            <fieldset className="sections">
              <legend>章节</legend>
              {p.data.sections.map((s) => (
                <label key={s.id} className={disabled.includes(s.id) ? 'muted' : ''}>
                  <input type="checkbox" checked={!disabled.includes(s.id)} onChange={() => toggle(s.id)} disabled={s.availability === 'unavailable'} />
                  {s.id} {s.title} {s.availability === 'limited' && <Tag tone="warn">受限</Tag>} {s.availability === 'unavailable' && <Tag tone="bad">不可用</Tag>}
                  {s.note && <span className="muted"> — {s.note}</span>}
                </label>
              ))}
            </fieldset>
            <button type="submit" disabled={submit.isPending}>{submit.isPending ? '提交中…' : '提交任务'}</button>
          </form>
          {submit.error && <ErrorBox error={submit.error} />}
        </>
      )}
    </main>
  );
}
