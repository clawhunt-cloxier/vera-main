import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useCallback, useState } from 'react';
import {
  fmtTime,
  get,
  newKey,
  post,
  skillTitle,
  type Calculation,
  type ChartSeries,
  type Evidence,
  type Report,
  type ReportSummary,
} from '../api/client';
import { useMe } from '../api/hooks';
import { ReportView } from '../components/ReportView';
import { Dialog, Empty, ErrorBox, Loading, SyntheticBanner, Tag } from '../components/ui';

export function Reports() {
  const me = useMe();
  const reports = useQuery({
    queryKey: ['reports'],
    queryFn: () => get<{ items: ReportSummary[] }>('/api/v1/reports'),
    staleTime: 10_000,
  });
  return (
    <main>
      {me.data?.synthetic && <SyntheticBanner />}
      <header className="app-head">
        <strong className="logo">VERA</strong>
        <nav>
          <Link to="/app">工作台</Link>
          <Link to="/app/watchlist">自选股</Link>
        </nav>
      </header>
      <section className="content">
        <h1>历史报告</h1>
        {reports.isLoading && <Loading />}
        {reports.data?.items.length === 0 && <Empty>尚未生成报告。</Empty>}
        {reports.data && reports.data.items.length > 0 && (
          <div className="table-wrapper">
            <table>
              <thead>
                <tr>
                  <th>对象</th>
                  <th>技能</th>
                  <th>类型</th>
                  <th>交付时间</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {reports.data.items.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <strong>{r.subject.name}</strong>
                    </td>
                    <td className="muted">{skillTitle[r.skill_id]}</td>
                    <td>
                      <Tag tone={r.result_kind === 'full' ? 'ok' : 'warn'}>
                        {r.result_kind === 'full' ? '完整' : '受限'}
                      </Tag>
                    </td>
                    <td className="muted">{fmtTime(r.delivered_at)}</td>
                    <td>
                      <Link to="/app/reports/$id" params={{ id: r.id }} className="btn-link">
                        查看
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </main>
  );
}

export function ReportPage({ id }: { id: string }) {
  const me = useMe();
  const r = useQuery({
    queryKey: ['report', id],
    queryFn: () => get<Report>(`/api/v1/reports/${id}/versions/1`),
    staleTime: 60_000,
  });
  const [feedbackClaim, setFeedbackClaim] = useState('');
  const qc = useQueryClient();
  const fb = useMutation({
    mutationFn: async (body: {
      kind: 'useful' | 'confusing' | 'incorrect' | 'other';
      comment: string;
      claim_ref: string;
    }) => {
      await post(`/api/v1/reports/${id}/versions/1/feedback`, body, { idem: newKey() });
      qc.invalidateQueries({ queryKey: ['report', id] });
    },
  });
  const loaders = {
    series: useCallback(
      async (sid: string) => get<ChartSeries>(`/api/v1/reports/${id}/versions/1/series/${sid}`),
      [id]
    ),
    evidence: useCallback(async (eid: string) => get<Evidence>(`/api/v1/evidence/${eid}`), [id]),
    calculation: useCallback(
      async (cid: string) => get<Calculation>(`/api/v1/calculations/${cid}`),
      [id]
    ),
  };
  return (
    <main>
      {me.data?.synthetic && <SyntheticBanner />}
      <p>
        <Link to="/app/reports">← 返回报告列表</Link>
      </p>
      {r.isLoading && <Loading />}
      {r.error && <ErrorBox error={r.error} />}
      {r.data && (
        <ReportView
          report={r.data}
          loaders={loaders}
          actions={
            <a
              className="btn-link"
              href={`/api/v1/reports/${id}/versions/${r.data.version}/pdf`}
              download
            >
              下载 PDF
            </a>
          }
          onFeedback={(ref) => {
            fb.reset();
            setFeedbackClaim(ref);
          }}
        />
      )}
      {feedbackClaim && (
        <Dialog
          title="反馈结论"
          onClose={() => {
            fb.reset();
            setFeedbackClaim('');
          }}
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const fd = new FormData(e.currentTarget);
              fb.mutate({
                kind: fd.get('kind') as 'useful' | 'confusing' | 'incorrect' | 'other',
                comment: String(fd.get('comment') ?? '').slice(0, 2000),
                claim_ref: feedbackClaim,
              });
            }}
          >
            <label>
              类别
              <select name="kind" required>
                <option value="incorrect">不准确</option>
                <option value="confusing">表述不清</option>
                <option value="useful">有帮助</option>
                <option value="other">其他</option>
              </select>
            </label>
            <label>
              说明
              <textarea name="comment" rows={3} maxLength={2000} required />
            </label>
            <button type="submit" disabled={fb.isPending || fb.isSuccess}>
              {fb.isPending ? '提交中…' : '提交反馈'}
            </button>
            {fb.error && <ErrorBox error={fb.error} />}
            {fb.isSuccess && <p className="muted">已提交，感谢反馈。</p>}
          </form>
        </Dialog>
      )}
    </main>
  );
}
