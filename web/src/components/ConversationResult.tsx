import { Link } from '@tanstack/react-router';
import { ArrowUpRight, Calculator, FileSearch, Reply } from 'lucide-react';
import { useCallback, useId, useMemo, useRef, useState } from 'react';
import type { ConversationResult as Result, ConversationTurn } from '../api/conversations';
import { fmtDataTime, get, type Calculation, type Claim, type Evidence } from '../api/client';
import { Chart } from './Chart';
import { EvidencePanel, ReportTable, type Loaders } from './ReportView';
import { Tag } from './ui';

export function ConversationResult({
  result,
  turn,
  onReply,
}: {
  result: Result;
  turn: ConversationTurn;
  onReply: () => void;
}) {
  const [evidence, setEvidence] = useState<{ claim: Claim; number: number; visible: boolean }>();
  const [expanded, setExpanded] = useState(false);
  const trigger = useRef<HTMLElement>(null);
  const panel = useId();
  const loaders = useMemo<Loaders>(
    () => ({
      series: async (id) => {
        const series = result.series.find((item) => item.series_id === id);
        if (!series) throw new Error('图表暂不可用');
        return series;
      },
      evidence: (id) => get<Evidence>('/api/v1/evidence/' + id),
      calculation: (id) => get<Calculation>('/api/v1/calculations/' + id),
    }),
    [result.series]
  );
  const dismiss = useCallback((ref: string) => {
    setEvidence((value) => (value?.claim.ref === ref && !value.visible ? undefined : value));
  }, []);
  const close = () => {
    setEvidence((value) => (value ? { ...value, visible: false } : undefined));
    trigger.current?.focus();
  };
  const open = (claim: Claim, number: number, element: HTMLElement) => {
    trigger.current = element;
    setEvidence({ claim, number, visible: true });
  };
  const openBlock = (refs: string[], label: string, element: HTMLElement) =>
    open(
      {
        ref: label,
        text: label,
        kind: 'fact',
        evidence_refs: refs,
        calculation_refs: [],
        scenario: null,
      },
      1,
      element
    );
  const comparison = result.series.some((item) => item.series_id === 'stock_comparison');
  const calculation = result.claims.find((claim) => claim.calculation_refs.length);
  const briefReference =
    turn.reference_kind === 'calculation' || turn.reference_kind === 'evidence';
  const claims =
    turn.reference_kind === 'calculation' && calculation && !comparison
      ? [calculation]
      : result.claims;
  const series = briefReference ? undefined : result.series[0];
  const table = briefReference ? undefined : result.tables[0];
  const tablePreview =
    table && !comparison && !expanded && table.rows.length > 8
      ? { ...table, rows: table.rows.slice(-8) }
      : table;
  return (
    <article className="chat-result">
      <header>
        <h3>{result.title}</h3>
        <div className="chat-result-meta">
          {result.subject.symbol && <span>{result.subject.symbol}</span>}
          {result.result_kind === 'limited' && <Tag tone="warn">数据受限</Tag>}
          {result.ai_generated && <Tag tone="info">AI 生成</Tag>}
          {result.synthetic && <Tag tone="bad">模拟数据</Tag>}
        </div>
        <p className="chat-dateline">数据截止 · {result.data_as_of}</p>
      </header>
      {turn.reference_kind === 'calculation' &&
        !comparison &&
        (calculation ? (
          <button
            type="button"
            className="chat-action"
            onClick={(e) =>
              open(calculation, result.claims.indexOf(calculation) + 1, e.currentTarget)
            }
          >
            <Calculator size={15} /> 查看原计算
          </button>
        ) : (
          <p className="muted">该轮结论使用直接披露的数据。可打开证据查看原始记录。</p>
        ))}
      <ul className="chat-claims">
        {claims.map((claim, index) => (
          <li key={claim.ref}>
            <span>{claim.text}</span>
            {(claim.evidence_refs.length > 0 || claim.calculation_refs.length > 0) && (
              <button
                type="button"
                className="chat-reference"
                aria-label={
                  comparison ? `查看结论 ${index + 1} 的计算与证据` : `查看结论 ${index + 1} 的证据`
                }
                aria-expanded={evidence?.visible === true && evidence.claim.ref === claim.ref}
                aria-controls={evidence?.claim.ref === claim.ref ? panel : undefined}
                onClick={(e) => open(claim, index + 1, e.currentTarget)}
              >
                <FileSearch size={15} /> {index + 1}
              </button>
            )}
          </li>
        ))}
      </ul>
      {series && (
        <section className="chat-result-block">
          <Chart load={loaders.series} title={series.title} seriesId={series.series_id} />
          {series.evidence_refs.length > 0 && (
            <button
              type="button"
              className="chat-action"
              onClick={(e) => openBlock(series.evidence_refs, '图表依据', e.currentTarget)}
            >
              <FileSearch size={15} /> 图表依据
            </button>
          )}
        </section>
      )}
      {table && tablePreview && (
        <section className="chat-result-block">
          <ReportTable table={tablePreview} />
          <div className="chat-inline-actions">
            {!comparison && table.rows.length > 8 && (
              <button
                type="button"
                className="chat-action"
                aria-expanded={expanded}
                onClick={() => setExpanded((value) => !value)}
              >
                {expanded ? '收起明细' : `查看全部 ${table.rows.length} 个交易日`}
              </button>
            )}
            {table.evidence_refs.length > 0 && (
              <button
                type="button"
                className="chat-action"
                onClick={(e) => openBlock(table.evidence_refs, '数据依据', e.currentTarget)}
              >
                <FileSearch size={15} /> 数据依据
              </button>
            )}
          </div>
        </section>
      )}
      <details className="chat-source-notes">
        <summary>数据来源与限制</summary>
        {result.sources.map((source) => (
          <p key={source.provider + source.dataset_id}>
            {source.provider} · {source.dataset_id} · {fmtDataTime(source.updated_at)}
          </p>
        ))}
        {result.limitations.map((note, index) => (
          <p key={index}>{note}</p>
        ))}
        <p>{result.disclaimer}</p>
      </details>
      <footer className="chat-inline-actions">
        <button type="button" className="chat-action" onClick={onReply}>
          <Reply size={15} /> 追问
        </button>
        {!result.is_brief && (
          <Link to="/app/reports/$id" params={{ id: result.report_id }} className="chat-action">
            <ArrowUpRight size={15} /> 阅读完整研报
          </Link>
        )}
      </footer>
      {evidence && (
        <EvidencePanel
          key={evidence.claim.ref}
          id={panel}
          modal
          claim={evidence.claim}
          number={evidence.number}
          visible={evidence.visible}
          showClaimText
          loaders={loaders}
          onClose={close}
          onDismissed={dismiss}
        />
      )}
    </article>
  );
}
