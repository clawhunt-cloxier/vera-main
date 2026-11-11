import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { get, post, skillTitle, statusLabel, terminal, type Task } from '../api/client';
import { useMe } from '../api/hooks';
import { ErrorBox, Loading, SyntheticBanner, Tag } from '../components/ui';

export function TaskDetail({ id }: { id: string }) {
  const me = useMe();
  const qc = useQueryClient();
  const t = useQuery({
    queryKey: ['task', id],
    queryFn: () => get<Task>(`/api/v1/research-tasks/${id}`),
    staleTime: 2000,
    refetchInterval: (q) =>
      q.state.data && !terminal.includes(q.state.data.status) ? 2000 : false,
  });
  const cancel = async () => {
    if (!t.data || !confirm('取消任务？已完成的工作无法恢复。')) return;
    try {
      await post(`/api/v1/research-tasks/${id}/cancel`, { expected_revision: t.data.revision });
      qc.invalidateQueries({ queryKey: ['task', id] });
      qc.invalidateQueries({ queryKey: ['tasks'] });
    } catch (e) {
      alert('取消失败：' + (e instanceof Error ? e.message : String(e)));
    }
  };
  return (
    <main>
      {me.data?.synthetic && <SyntheticBanner />}
      <p>
        <Link to="/app">← 返回工作台</Link>
      </p>
      {t.isLoading && <Loading />}
      {t.error && <ErrorBox error={t.error} />}
      {t.data && (
        <>
          <h1>任务：{t.data.subject.name}</h1>
          <div className="meta">
            <Tag>{skillTitle[t.data.skill_id]}</Tag>
            <Tag
              tone={
                t.data.status === 'COMPLETED'
                  ? 'ok'
                  : terminal.includes(t.data.status)
                    ? 'bad'
                    : 'info'
              }
            >
              {statusLabel[t.data.status]}
            </Tag>
            <span className="muted">
              创建于 {new Date(t.data.created_at).toLocaleString('zh-CN')}
            </span>
          </div>
          {t.data.reason_message && <p className="notice">{t.data.reason_message}</p>}
          {t.data.status === 'COMPLETED' && t.data.report_ref && (
            <p>
              <Link to="/app/reports/$id" params={{ id: t.data.report_ref.id }}>
                查看报告
              </Link>
            </p>
          )}
          {!terminal.includes(t.data.status) && (
            <button onClick={cancel} className="warn">
              取消任务
            </button>
          )}
          <h2>步骤</h2>
          <table>
            <thead>
              <tr>
                <th>步骤</th>
                <th>状态</th>
              </tr>
            </thead>
            <tbody>
              {t.data.steps.map((s) => (
                <tr key={s.key}>
                  <td>{s.label}</td>
                  <td>
                    <Tag
                      tone={
                        s.status === 'SUCCEEDED' ? 'ok' : s.status === 'FAILED' ? 'bad' : 'info'
                      }
                    >
                      {s.status}
                    </Tag>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </main>
  );
}
