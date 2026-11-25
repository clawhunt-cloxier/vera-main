import { ChevronDown, List, MessageSquare, X } from 'lucide-react';
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react';
import {
  fmtDataTime,
  fmtTime,
  kindLabel,
  skillTitle,
  type Calculation,
  type ChartSeries,
  type Claim,
  type Evidence,
  type Report,
} from '../api/client';
import { Chart } from './Chart';
import { Tag } from './ui';
import './evidence.css';

export type Loaders = {
  series: (id: string) => Promise<ChartSeries>;
  evidence: (id: string) => Promise<Evidence>;
  calculation: (id: string) => Promise<Calculation>;
};

const tierLabel: Record<string, string> = {
  L1: 'L1 原始披露',
  L2: 'L2 权威转载',
  L3: 'L3 授权媒体',
  L4: 'L4 厂商加工口径',
};

const latencyLabel: Record<string, string> = {
  real_time: '实时传输',
  delayed: '延迟行情',
  unknown: '延迟性质未知',
};
const freshnessLabel: Record<string, string> = {
  fresh: '符合新鲜度策略',
  stale: '已过期',
  unavailable: '不可用',
  unknown: '新鲜度未知',
};

const sourceTime = (value: string | null | undefined) => (value ? fmtDataTime(value) : '未提供');

// These sources identify a publication, page or news outlet in their locator.
const publishedSources = new Set([
  'fin.statement',
  'doc.periodic_text',
  'doc.announcement',
  'news.item',
]);

const evidencePageSize = 12;
const calculationUnits: Record<string, string> = {
  CNY: '元',
  'CNY/share': '元/股',
  index_point: '点',
  shares: '股',
  times: '倍',
  ratio: '（小数比例）',
  pp: '（比例差，小数）',
};
const numericCell = /^[+\-−]?(?:\d{1,3}(?:,\d{3})+|\d+)(?:\.\d+)?[%％]?$/;
const dateCell = /^\d{4}-\d{2}-\d{2}$/;
const compactContents = '(max-width: 759px)';

function ReportContents({ sections }: { sections: Report['sections'] }) {
  const [expanded, setExpanded] = useState(false);
  const [compact, setCompact] = useState(() => window.matchMedia(compactContents).matches);
  const toggle = useRef<HTMLButtonElement>(null);
  const chapters = useId();
  useEffect(() => {
    const media = window.matchMedia(compactContents);
    const update = () => setCompact(media.matches);
    update();
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  }, []);
  return (
    <nav
      className="toc"
      aria-label="章节目录"
      onBlur={(event) => {
        if (compact && !event.currentTarget.contains(event.relatedTarget)) setExpanded(false);
      }}
      onKeyDown={(event) => {
        if (compact && expanded && event.key === 'Escape') {
          setExpanded(false);
          toggle.current?.focus();
        }
      }}
    >
      <strong className="toc-heading">章节目录</strong>
      <button
        className="toc-toggle"
        ref={toggle}
        type="button"
        aria-expanded={expanded}
        aria-controls={chapters}
        onClick={() => setExpanded((open) => !open)}
      >
        <List size={16} aria-hidden="true" /> 章节目录 <ChevronDown size={16} aria-hidden="true" />
      </button>
      <div id={chapters} className="toc-fold" inert={compact && !expanded}>
        <div className="toc-links">
          {sections.map((s) => (
            <a
              key={s.id}
              href={'#' + s.id}
              className={s.status !== 'ok' ? 'muted' : undefined}
              onClick={() => setExpanded(false)}
            >
              <span className="section-id">{s.id}</span> <span>{s.title}</span>
            </a>
          ))}
          <a className="toc-limits" href="#limits" onClick={() => setExpanded(false)}>
            数据与限制
          </a>
        </div>
      </div>
    </nav>
  );
}

export function EvidencePanel({
  id,
  claim,
  number,
  visible,
  showClaimText,
  modal = false,
  loaders,
  onClose,
  onDismissed,
}: {
  id: string;
  claim: Claim;
  number: number;
  visible: boolean;
  showClaimText: boolean;
  modal?: boolean;
  loaders: Loaders;
  onClose: () => void;
  onDismissed: (ref: string) => void;
}) {
  const [ev, setEv] = useState<Record<string, Evidence | 'err'>>({});
  const [calc, setCalc] = useState<Record<string, Calculation | 'err'>>({});
  const [page, setPage] = useState(0);
  const dialogRef = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (!visible) {
      dialog.close();
      let current = true;
      void Promise.allSettled(dialog.getAnimations().map((animation) => animation.finished)).then(
        () => {
          if (current) onDismissed(claim.ref);
        }
      );
      return () => {
        current = false;
      };
    }
    const drawer = window.matchMedia('(max-width: 1099px)');
    const show = () => {
      const showAsModal = modal || drawer.matches;
      if (dialog.open && dialog.matches(':modal') === showAsModal) return;
      if (dialog.open) dialog.close();
      if (showAsModal) dialog.showModal();
      else dialog.show();
    };
    show();
    drawer.addEventListener('change', show);
    return () => drawer.removeEventListener('change', show);
  }, [claim.ref, modal, onDismissed, visible]);
  const pageRefs = useMemo(
    () => claim.evidence_refs.slice(page * evidencePageSize, (page + 1) * evidencePageSize),
    [claim.evidence_refs, page]
  );
  useEffect(() => {
    let active = true;
    pageRefs.forEach((id) =>
      loaders.evidence(id).then(
        (e) => active && setEv((m) => ({ ...m, [id]: e })),
        () => active && setEv((m) => ({ ...m, [id]: 'err' }))
      )
    );
    return () => {
      active = false;
    };
  }, [pageRefs, loaders.evidence]);
  useEffect(() => {
    claim.calculation_refs.forEach((id) =>
      loaders.calculation(id).then(
        (c) => setCalc((m) => ({ ...m, [id]: c })),
        () => setCalc((m) => ({ ...m, [id]: 'err' }))
      )
    );
  }, [claim.calculation_refs, loaders.calculation]);
  return (
    <dialog
      id={id}
      ref={dialogRef}
      className="evidence"
      inert={!visible}
      aria-label="证据"
      data-testid="evidence-panel"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onKeyDown={(event) => {
        if (event.key === 'Escape' && !event.currentTarget.matches(':modal')) onClose();
      }}
      onClick={(event) => {
        const dialog = event.currentTarget;
        if (event.target !== dialog || !dialog.matches(':modal')) return;
        const { left, right, top, bottom } = dialog.getBoundingClientRect();
        const { clientX: x, clientY: y } = event;
        if (x < left || x > right || y < top || y > bottom) onClose();
      }}
    >
      <div className="dialog-head">
        <h2>证据 {number}</h2>
        <button
          type="button"
          className="ghost"
          onClick={onClose}
          aria-label="关闭证据"
          title="关闭证据"
        >
          <X size={18} aria-hidden="true" />
        </button>
      </div>
      {showClaimText && <p className="claim-text">{claim.text}</p>}
      {claim.calculation_refs.length > 0 && <h3>计算过程</h3>}
      {claim.calculation_refs.map((id) => {
        const c = calc[id];
        if (!c)
          return (
            <p key={id} className="muted">
              加载中…
            </p>
          );
        if (c === 'err')
          return (
            <p key={id} className="muted">
              计算记录不可用
            </p>
          );
        return (
          <div key={id} className="card small">
            <div>
              <strong>{c.formula_id}</strong> <span className="muted">v{c.formula_version}</span>
            </div>
            <div>
              结果：{c.result ?? '—'} {calculationUnits[c.unit] ?? c.unit} ·{' '}
              {c.applicability === 'APPLICABLE' ? '适用' : '不适用'}
              {c.reason ? '（' + c.reason + '）' : ''}
            </div>
            <div className="muted">
              输入 {c.inputs.length} 项：
              {c.inputs
                .slice(0, 6)
                .map((i) => i.value + (i.unit ? ' ' + (calculationUnits[i.unit] ?? i.unit) : ''))
                .join('，')}
              {c.inputs.length > 6 ? '…' : ''}
            </div>
          </div>
        );
      })}
      <h3>来源</h3>
      {pageRefs.map((id) => {
        const e = ev[id];
        if (!e)
          return (
            <p key={id} className="muted">
              加载中…
            </p>
          );
        if (e === 'err')
          return (
            <p key={id} className="muted">
              证据不可用
            </p>
          );
        return (
          <div key={id} className="card small">
            <div>
              <strong>{e.title}</strong>
            </div>
            <div>
              {e.label}
              {e.value ? '：' + e.value + (e.unit ? ' ' + e.unit : '') : ''}
            </div>
            <div className="muted">
              {tierLabel[e.source_tier] ?? e.source_tier} · {e.provider}
            </div>
            <dl className="source-times">
              {e.period && (
                <>
                  <dt>数据期间</dt>
                  <dd>{e.period}</dd>
                </>
              )}
              {e.published_at && (
                <>
                  <dt>披露时间</dt>
                  <dd>
                    {e.published_at.includes('T') ? fmtDataTime(e.published_at) : e.published_at}
                  </dd>
                </>
              )}
              {(e.event_at || e.latency_mode) && (
                <>
                  <dt>行情事件时间</dt>
                  <dd>{sourceTime(e.event_at)}</dd>
                </>
              )}
              {(e.received_at || e.latency_mode) && (
                <>
                  <dt>系统接收时间</dt>
                  <dd>{sourceTime(e.received_at)}</dd>
                </>
              )}
              <dt>可用时间</dt>
              <dd>{sourceTime(e.available_at)}</dd>
              {e.latency_mode && (
                <>
                  <dt>延迟性质</dt>
                  <dd>{latencyLabel[e.latency_mode]}</dd>
                </>
              )}
            </dl>
            <p className="muted">以上时间按北京时间显示。</p>
            {e.basis && <div className="muted">口径：{e.basis}</div>}
            {e.locator && publishedSources.has(e.dataset_id) && (
              <div className="evidence-location">
                <span className="muted">证据位置</span>
                <span>{e.locator}</span>
              </div>
            )}
            {e.excerpt && <blockquote className="evidence-excerpt">{e.excerpt}</blockquote>}
          </div>
        );
      })}
      {claim.evidence_refs.length > evidencePageSize && (
        <nav aria-label="来源翻页" className="actions">
          <button type="button" disabled={page === 0} onClick={() => setPage(page - 1)}>
            上一页
          </button>
          <span>
            第 {page * evidencePageSize + 1}–{page * evidencePageSize + pageRefs.length} 条，共{' '}
            {claim.evidence_refs.length} 条
          </span>
          <button
            type="button"
            disabled={(page + 1) * evidencePageSize >= claim.evidence_refs.length}
            onClick={() => setPage(page + 1)}
          >
            下一页
          </button>
        </nav>
      )}
    </dialog>
  );
}

export function ReportView({
  report,
  loaders,
  onFeedback,
}: {
  report: Report;
  loaders: Loaders;
  onFeedback?: (claimRef: string) => void;
}) {
  const [evidence, setEvidence] = useState<{ claim: Claim; visible: boolean } | null>(null);
  const evidenceId = useId();
  const dismissEvidence = useCallback((ref: string) => {
    setEvidence((current) => (current?.claim.ref === ref && !current.visible ? null : current));
  }, []);
  const claims = useMemo(() => Object.fromEntries(report.claims.map((c) => [c.ref, c])), [report]);
  const num = useMemo(
    () => Object.fromEntries(report.claims.map((c, i) => [c.ref, i + 1])),
    [report]
  );
  const additionalSources = report.sources.filter(
    (source) =>
      !report.source_freshness?.some(
        (item) => item.dataset_id === source.dataset_id && item.provider === source.provider
      )
  );
  return (
    <div className={'report' + (evidence?.visible ? ' with-panel' : '')}>
      <ReportContents sections={report.sections} />
      <article className="report-body">
        <header className="report-head">
          <h1>{report.title}</h1>
          <div className="meta">
            <Tag>{skillTitle[report.skill_id]}</Tag>
            <Tag tone={report.result_kind === 'full' ? 'ok' : 'warn'}>
              {report.result_kind === 'full' ? '完整' : '受限'}
            </Tag>
            <Tag tone="info">AI 生成</Tag>
            {report.synthetic && <Tag tone="bad">模拟数据</Tag>}
          </div>
          <ul className="reader-dateline">
            {report.subject.symbol && <li>{report.subject.symbol}</li>}
            <li>
              {report.snapshot_mode === 'collect_latest'
                ? `快照截止 ${sourceTime(report.snapshot_as_of)}（北京时间）`
                : `数据截止 ${report.data_as_of}`}
            </li>
            <li>交付 {fmtTime(report.delivered_at)}</li>
            <li>版本 {report.version}</li>
          </ul>
          {report.snapshot_mode === 'collect_latest' && (
            <p className="muted">
              快照截止是本次研究冻结数据的时间；各来源的实际事件与披露时间见“数据与限制”。
            </p>
          )}
        </header>
        {report.sections.map((s) => (
          <section
            key={s.id}
            id={s.id}
            className="rsec"
            tabIndex={-1}
            aria-labelledby={'h-' + s.id}
          >
            <h2 id={'h-' + s.id}>
              <span className="section-id">{s.id}</span> {s.title}{' '}
              {s.status !== 'ok' && (
                <Tag tone="warn">{s.status === 'limited' ? '受限' : '未生成'}</Tag>
              )}
            </h2>
            {s.note && <p className="note">{s.note}</p>}
            {s.claim_refs.map((r) => {
              const c = claims[r];
              if (!c) return null;
              const body = (
                <>
                  <Tag tone={c.kind === 'inference' || c.kind === 'scenario' ? 'info' : undefined}>
                    {kindLabel[c.kind]}
                  </Tag>
                  <span className="claim-copy">
                    {c.text}{' '}
                    <span className="claim-tools">
                      <button
                        type="button"
                        className="cite"
                        onClick={() =>
                          setEvidence((current) => ({
                            claim: c,
                            visible: current?.claim.ref !== r || !current.visible,
                          }))
                        }
                        aria-label={'查看证据 ' + num[r]}
                        aria-expanded={evidence?.visible === true && evidence.claim.ref === r}
                        aria-controls={evidence ? evidenceId : undefined}
                      >
                        [{num[r]}]
                      </button>
                      {onFeedback && (
                        <button
                          type="button"
                          className="ghost small"
                          onClick={() => onFeedback(c.ref)}
                          aria-label={'反馈结论 ' + c.ref}
                          title="反馈结论"
                        >
                          <MessageSquare size={14} aria-hidden="true" />
                        </button>
                      )}
                    </span>
                  </span>
                </>
              );
              return c.kind === 'scenario' && c.scenario ? (
                <div key={r} className="scenario" data-testid="claim">
                  <p>{body}</p>
                  <dl>
                    <dt>触发条件</dt>
                    <dd>{c.scenario.trigger}</dd>
                    <dt>观察指标</dt>
                    <dd>{c.scenario.watch.join('、')}</dd>
                    <dt>失效条件</dt>
                    <dd>{c.scenario.invalidation}</dd>
                  </dl>
                </div>
              ) : (
                <p key={r} className="claim" data-testid="claim">
                  {body}
                </p>
              );
            })}
            {s.tables.map((table, i) => (
              <ReportTable key={i} table={table} />
            ))}
            {s.charts.map((c) => (
              <Chart
                key={c.series_id}
                title={c.title}
                seriesId={c.series_id}
                load={loaders.series}
              />
            ))}
          </section>
        ))}
        <section id="limits" className="rsec" tabIndex={-1} aria-labelledby="h-limits">
          <h2 id="h-limits">数据与限制</h2>
          <ul>
            {report.limitations.map((l, i) => (
              <li key={i}>{l}</li>
            ))}
          </ul>
          <h3>数据来源</h3>
          {report.source_freshness && report.source_freshness.length > 0 && (
            <div className="sources">
              <p className="muted">
                各来源时间按北京时间显示；实时传输不代表休市或停牌时发生新成交。
              </p>
              {report.source_freshness.map((source, index) => (
                <div
                  key={`${source.dataset_id}-${source.provider}-${index}`}
                  className="card small"
                >
                  <strong>
                    {source.provider} · {source.dataset_id}
                  </strong>{' '}
                  <Tag tone={source.status === 'fresh' ? 'ok' : 'warn'}>
                    {freshnessLabel[source.status]}
                  </Tag>{' '}
                  <Tag tone={source.latency_mode === 'real_time' ? 'info' : 'warn'}>
                    {latencyLabel[source.latency_mode]}
                  </Tag>
                  <dl className="source-times">
                    <dt>行情事件时间</dt>
                    <dd>{sourceTime(source.event_at)}</dd>
                    <dt>披露时间</dt>
                    <dd>{sourceTime(source.published_at)}</dd>
                    <dt>系统接收时间</dt>
                    <dd>{sourceTime(source.received_at)}</dd>
                    <dt>可用时间</dt>
                    <dd>{sourceTime(source.available_at)}</dd>
                    <dt>数据期间</dt>
                    <dd>{source.period ?? '未提供'}</dd>
                  </dl>
                  {source.note && <p className="muted">{source.note}</p>}
                </div>
              ))}
            </div>
          )}
          {additionalSources.length > 0 && (
            <ul className="sources">
              {additionalSources.map((s) => (
                <li key={JSON.stringify([s.provider, s.dataset_id])}>
                  {s.provider} · {s.dataset_id} · 更新至 {fmtTime(s.updated_at)}
                </li>
              ))}
            </ul>
          )}
          <p className="disclaimer">{report.disclaimer}</p>
        </section>
      </article>
      {evidence && (
        <div className="evidence-slot">
          <EvidencePanel
            key={evidence.claim.ref}
            id={evidenceId}
            claim={evidence.claim}
            number={num[evidence.claim.ref]}
            visible={evidence.visible}
            showClaimText={
              report.skill_version !== '1.1.0-demo.2' || report.skill_id !== 'stock_analysis'
            }
            loaders={loaders}
            onClose={() => setEvidence((current) => current && { ...current, visible: false })}
            onDismissed={dismissEvidence}
          />
        </div>
      )}
    </div>
  );
}

export function ReportTable({ table }: { table: Report['sections'][number]['tables'][number] }) {
  const choiceDaily = table.title === 'Choice 日成交观察（非实时）';
  const numeric = table.columns.map((_, index) => {
    if (index === 0) return false;
    if (choiceDaily) return true;
    const values = table.rows
      .map((row) => row[index]?.trim())
      .filter((value) => value && value !== '—');
    return values.length > 0 && values.every((value) => numericCell.test(value ?? ''));
  });
  return (
    <div className="table-wrap">
      <table className="report-table">
        <caption>{table.title}</caption>
        <thead>
          <tr>
            {table.columns.map((label, index) => (
              <th key={label} scope="col" data-numeric={numeric[index] || undefined}>
                {label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {table.rows.map((row, index) => (
            <tr key={index}>
              {row.map((value, column) =>
                column === 0 ? (
                  <th key={column} scope="row" data-date={dateCell.test(value ?? '') || undefined}>
                    {value ?? '—'}
                  </th>
                ) : (
                  <td
                    key={column}
                    data-numeric={numeric[column] || undefined}
                    data-date={dateCell.test(value ?? '') || undefined}
                  >
                    {value ?? '—'}
                  </td>
                )
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
