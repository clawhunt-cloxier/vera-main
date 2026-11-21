import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import {
  fmtDataTime,
  fmtTime,
  get,
  post,
  skillTitle,
  taskStatusLabel,
  terminal,
  type Task,
} from '../api/client';
import { useMe } from '../api/hooks';
import { researchError, researchText } from '../api/researchPresentation';
import { ErrorBox, Loading, SyntheticBanner } from '../components/ui';
import './task.css';

function outcomeNote(task: Task): string {
  if (task.status === 'COMPLETED')
    return task.report_ref
      ? '报告已保存。打开报告即可阅读结论、查看数据和引用依据。'
      : '报告链接暂时无法读取，请重新加载或前往历史报告查看。';
  // These engine messages can contain integration details or verification logs.
  // Keep the actionable cause here; evidence and provenance stay with the report.
  switch (task.reason_code) {
    case 'DATA_SOURCE_UNAVAILABLE':
      return '暂时无法取得本次研究所需的行情，请稍后重新研究。';
    case 'MIXED_SOURCE_UNAVAILABLE':
    case 'SOURCE_AUTHORIZATION_UNAVAILABLE':
      return '本次研究所需的数据暂不可读取，或其授权不支持此次研究。';
    case 'SKILL_VERSION_MISSING':
    case 'REAL_RESEARCH_DISABLED':
      return '这项研究服务当前不可用，请稍后重试。';
  }
  if (task.reason_message?.trim()) {
    const message = researchText(task.reason_message);
    if (task.reason_code === 'REQUIRED_SECTION_UNSUPPORTED') {
      const section = message.match(/^必需章节\s+\S+\s+(.+?)\s+无有效结论/);
      if (section) return `「${section[1]}」没有足够的证据支持结论，本次未生成报告。`;
    }
    return message;
  }
  switch (task.status) {
    case 'CANCELLED':
      return '本次研究已按你的请求停止。';
    case 'BUDGET_EXHAUSTED':
      return '本次研究可用的额度已用完，必需章节未能完成。';
    case 'ABSTAINED':
      return '本次未交付报告，系统没有提供进一步说明。';
    case 'BLOCKED':
      return '本次研究未获准执行，系统没有提供进一步说明。';
    default:
      return '系统未能完成此次研究，也未返回具体原因。你可以重新发起研究。';
  }
}

export function TaskDetail({ id }: { id: string }) {
  const me = useMe();
  const qc = useQueryClient();
  const taskQuery = useQuery({
    queryKey: ['task', id],
    queryFn: ({ signal }) => get<Task>(`/api/v1/research-tasks/${id}`, { signal }),
    staleTime: 2000,
    refetchInterval: (q) =>
      q.state.data && !terminal.includes(q.state.data.status) ? 2000 : false,
  });
  const cancel = useMutation({
    mutationFn: (task: Task) =>
      post(`/api/v1/research-tasks/${task.id}/cancel`, { expected_revision: task.revision }),
    // A concurrent delivery or revision conflict also warrants a fresh read.
    onSettled: (_data, _error, task) =>
      Promise.all([
        qc.invalidateQueries({ queryKey: ['task', task.id] }),
        qc.invalidateQueries({ queryKey: ['tasks'] }),
      ]),
  });
  const task = taskQuery.data;
  const ended = !!task && terminal.includes(task.status);
  const cancelling =
    task?.status === 'CANCEL_REQUESTED' ||
    (cancel.variables?.id === id && (cancel.isSuccess || cancel.isPending));
  const refresh = (
    <button
      type="button"
      className="task-view__secondary"
      disabled={taskQuery.isFetching}
      onClick={() => void taskQuery.refetch()}
    >
      重新加载
    </button>
  );
  return (
    <main className="task-view">
      {me.data?.synthetic && <SyntheticBanner />}
      <nav aria-label="任务导航">
        <Link className="task-view__back" to="/app">
          <span aria-hidden="true">←</span> 返回工作台
        </Link>
      </nav>
      {taskQuery.isLoading && <Loading label="正在读取研究任务…" />}
      {taskQuery.error && (
        <>
          <ErrorBox error={researchError(taskQuery.error)} />
          {refresh}
        </>
      )}
      {task && (
        <article aria-labelledby="task-title">
          <header className="task-view__header">
            <p>{skillTitle[task.skill_id]}</p>
            <h1 id="task-title">任务：{task.subject.name}</h1>
            <span>
              创建于 <time dateTime={task.created_at}>{fmtTime(task.created_at)}</time>（北京时间）
            </span>
          </header>
          <section className="task-view__card" aria-label="研究进度与结果">
            <div role="status" aria-live="polite" aria-atomic="true" className="task-view__state">
              {ended ? (
                <span
                  className={
                    'task-view__symbol ' + (task.status === 'COMPLETED' ? 'task-view__done' : '')
                  }
                  aria-hidden="true"
                >
                  {task.status === 'COMPLETED' ? '✓' : task.status === 'CANCELLED' ? '–' : '!'}
                </span>
              ) : (
                <span className="task-view__spinner" aria-hidden="true" />
              )}
              <div>
                <h2>
                  {ended
                    ? taskStatusLabel(task.status)
                    : cancelling
                      ? '正在取消'
                      : '正在生成研究报告'}
                </h2>
                <p>
                  {ended
                    ? outcomeNote(task)
                    : cancelling
                      ? '正在确认取消结果。你可以稍后在最近任务中查看。'
                      : '你可以离开此页，稍后从工作台的最近任务查看结果。'}
                </p>
              </div>
            </div>
            <div className="task-view__actions">
              {ended &&
                task.status === 'COMPLETED' &&
                (task.report_ref ? (
                  <Link
                    to="/app/reports/$id"
                    params={{ id: task.report_ref.id }}
                    className="task-view__primary"
                  >
                    查看报告 <span aria-hidden="true">→</span>
                  </Link>
                ) : (
                  <>
                    <Link to="/app/reports" className="task-view__primary">
                      历史报告
                    </Link>
                    {!taskQuery.error && refresh}
                  </>
                ))}
              {ended && task.status !== 'COMPLETED' && (
                <Link
                  to="/app/quick/$skill"
                  params={{ skill: task.skill_id }}
                  className="task-view__primary"
                >
                  重新研究 <span aria-hidden="true">→</span>
                </Link>
              )}
              {!ended && !cancelling && (
                <button
                  type="button"
                  className="task-view__secondary"
                  onClick={() => {
                    if (window.confirm('取消这项研究？取消后如需继续，可以重新发起研究。'))
                      cancel.mutate(task);
                  }}
                >
                  取消任务
                </button>
              )}
            </div>
            {!ended && cancel.variables?.id === id && cancel.error && (
              <ErrorBox error={researchError(cancel.error)} />
            )}
            {task.snapshot_mode === 'collect_latest' && (
              <p className="task-view__cutoff">
                {task.snapshot_as_of
                  ? `快照截止 ${fmtDataTime(task.snapshot_as_of)}（北京时间）`
                  : ended
                    ? '此次任务没有已冻结的数据快照。'
                    : '数据快照尚未冻结，采集完成后确定截止时间。'}
              </p>
            )}
          </section>
        </article>
      )}
    </main>
  );
}
