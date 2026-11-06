import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { fmtTime, kindLabel, skillTitle, type Calculation, type ChartSeries, type Claim, type Evidence, type Report } from '../api/client';
import { Chart } from './Chart';
import { Tag } from './ui';

export type Loaders = {
  series: (id: string) => Promise<ChartSeries>;
  evidence: (id: string) => Promise<Evidence>;
  calculation: (id: string) => Promise<Calculation>;
};

const tierLabel: Record<string, string> = { L1: 'L1 原始披露', L2: 'L2 权威转载', L3: 'L3 授权媒体', L4: 'L4 厂商加工口径' };

function EvidencePanel({ claim, loaders, onClose }: { claim: Claim; loaders: Loaders; onClose: () => void }) {
  const [ev, setEv] = useState<Record<string, Evidence | 'err'>>({});
  const [calc, setCalc] = useState<Record<string, Calculation | 'err'>>({});
  useEffect(() => {
    claim.evidence_refs.forEach((id) => loaders.evidence(id).then((e) => setEv((m) => ({ ...m, [id]: e })), () => setEv((m) => ({ ...m, [id]: 'err' }))));
    claim.calculation_refs.forEach((id) =>
      loaders.calculation(id).then((c) => setCalc((m) => ({ ...m, [id]: c })), () => setCalc((m) => ({ ...m, [id]: 'err' }))),
    );
  }, [claim, loaders]);
  return (
    <aside className="evidence" aria-label="证据" data-testid="evidence-panel">
      <div className="dialog-head">
        <h2>证据 {claim.ref}</h2>
        <button className="ghost" onClick={onClose} aria-label="关闭证据">×</button>
      </div>
      <p className="claim-text">{claim.text}</p>
      {claim.calculation_refs.length > 0 && <h3>计算过程</h3>}
      {claim.calculation_refs.map((id) => {
        const c = calc[id];
        if (!c) return <p key={id} className="muted">加载中…</p>;
        if (c === 'err') return <p key={id} className="muted">计算记录不可用</p>;
        return (
          <div key={id} className="card small">
            <div><strong>{c.formula_id}</strong> <span className="muted">v{c.formula_version}</span></div>
            <div>结果：{c.result ?? '—'} {c.unit} · {c.applicability === 'APPLICABLE' ? '适用' : '不适用'}{c.reason ? '（' + c.reason + '）' : ''}</div>
            <div className="muted">输入 {c.inputs.length} 项：{c.inputs.slice(0, 6).map((i) => i.value + (i.unit ? ' ' + i.unit : '')).join('，')}{c.inputs.length > 6 ? '…' : ''}</div>
          </div>
        );
      })}
      <h3>来源</h3>
      {claim.evidence_refs.slice(0, 12).map((id) => {
        const e = ev[id];
        if (!e) return <p key={id} className="muted">加载中…</p>;
        if (e === 'err') return <p key={id} className="muted">证据不可用</p>;
        return (
          <div key={id} className="card small">
            <div><strong>{e.title}</strong></div>
            <div>{e.label}{e.value ? '：' + e.value + (e.unit ? ' ' + e.unit : '') : ''}</div>
            <div className="muted">
              {tierLabel[e.source_tier] ?? e.source_tier} · {e.provider} · {e.period ?? ''} · 可用时点 {fmtTime(e.available_at)}
            </div>
            {e.basis && <div className="muted">口径：{e.basis}</div>}
            {e.locator && <div className="muted">位置：{e.locator}</div>}
            {e.excerpt && <blockquote>{e.excerpt}</blockquote>}
          </div>
        );
      })}
      {claim.evidence_refs.length > 12 && <p className="muted">另有 {claim.evidence_refs.length - 12} 条证据（区间序列）。</p>}
    </aside>
  );
}

export function ReportView({ report, loaders, actions, onFeedback }: {
  report: Report;
  loaders: Loaders;
  actions?: ReactNode;
  onFeedback?: (claimRef: string) => void;
}) {
  const [open, setOpen] = useState<Claim | null>(null);
  const claims = useMemo(() => Object.fromEntries(report.claims.map((c) => [c.ref, c])), [report]);
  const num = useMemo(() => Object.fromEntries(report.claims.map((c, i) => [c.ref, i + 1])), [report]);
  const series = useCallback((id: string) => () => loaders.series(id), [loaders]);
  return (
    <div className={'report' + (open ? ' with-panel' : '')}>
      <nav className="toc" aria-label="章节目录">
        {report.sections.map((s) => (
          <a key={s.id} href={'#' + s.id} className={s.status !== 'ok' ? 'muted' : ''}>{s.id} {s.title}</a>
        ))}
        <a href="#limits">数据与限制</a>
      </nav>
      <article className="report-body">
        <header className="report-head">
          <h1>{report.title}</h1>
          <div className="meta">
            <Tag>{skillTitle[report.skill_id]}</Tag>
            <Tag tone={report.result_kind === 'full' ? 'ok' : 'warn'}>{report.result_kind === 'full' ? '完整' : '受限'}</Tag>
            <Tag tone="info">AI 生成</Tag>
            {report.synthetic && <Tag tone="bad">模拟数据</Tag>}
            <span className="muted">数据截止 {report.data_as_of} · 版本 {report.version} · 交付 {fmtTime(report.delivered_at)}</span>
          </div>
          {actions && <div className="actions">{actions}</div>}
        </header>
        {report.sections.map((s) => (
          <section key={s.id} id={s.id} className="rsec" aria-labelledby={'h-' + s.id}>
            <h2 id={'h-' + s.id}>{s.id} {s.title} {s.status !== 'ok' && <Tag tone="warn">{s.status === 'limited' ? '受限' : '未生成'}</Tag>}</h2>
            {s.note && <p className="note">{s.note}</p>}
            {s.claim_refs.map((r) => {
              const c = claims[r];
              if (!c) return null;
              const body = (
                <>
                  <Tag tone={c.kind === 'inference' || c.kind === 'scenario' ? 'info' : undefined}>{kindLabel[c.kind]}</Tag> {c.text}{' '}
                  <button className="cite" onClick={() => setOpen(c)} aria-label={'查看证据 ' + num[r]}>[{num[r]}]</button>
                  {onFeedback && <button className="ghost small" onClick={() => onFeedback(c.ref)} aria-label={'反馈结论 ' + c.ref}>反馈</button>}
                </>
              );
              return c.kind === 'scenario' && c.scenario ? (
                <div key={r} className="scenario" data-testid="claim">
                  <p>{body}</p>
                  <dl>
                    <dt>触发条件</dt><dd>{c.scenario.trigger}</dd>
                    <dt>观察指标</dt><dd>{c.scenario.watch.join('、')}</dd>
                    <dt>失效条件</dt><dd>{c.scenario.invalidation}</dd>
                  </dl>
                </div>
              ) : (
                <p key={r} className="claim" data-testid="claim">{body}</p>
              );
            })}
            {s.tables.map((t, i) => (
              <div key={i} className="table-wrap">
                <table>
                  <caption>{t.title}</caption>
                  <thead><tr>{t.columns.map((c) => <th key={c} scope="col">{c}</th>)}</tr></thead>
                  <tbody>{t.rows.map((row, j) => <tr key={j}>{row.map((v, k) => (k === 0 ? <th key={k} scope="row">{v ?? '—'}</th> : <td key={k}>{v ?? '—'}</td>))}</tr>)}</tbody>
                </table>
              </div>
            ))}
            {s.charts.map((c) => <Chart key={c.series_id} title={c.title} load={series(c.series_id)} />)}
          </section>
        ))}
        <section id="limits" className="rsec">
          <h2>数据与限制</h2>
          <ul>{report.limitations.map((l, i) => <li key={i}>{l}</li>)}</ul>
          <h3>数据来源</h3>
          <ul className="sources">{report.sources.map((s) => <li key={s.dataset_id}>{s.provider} · {s.dataset_id} · 更新至 {fmtTime(s.updated_at)}</li>)}</ul>
          <p className="disclaimer">{report.disclaimer}</p>
        </section>
      </article>
      {open && <EvidencePanel claim={open} loaders={loaders} onClose={() => setOpen(null)} />}
    </div>
  );
}
