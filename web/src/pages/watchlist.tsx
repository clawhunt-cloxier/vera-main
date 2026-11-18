import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import {
  api,
  get,
  newKey,
  post,
  type PlanResult,
  type QuoteRequirement,
  type WatchItem,
} from '../api/client';
import { useMe } from '../api/hooks';
import { EntitySearch } from '../components/EntitySearch';
import { QuoteRequirementField } from '../components/QuoteRequirementField';
import { Dialog, Empty, ErrorBox, Loading, SyntheticBanner, Tag } from '../components/ui';

const ruleLabel: Record<string, string> = {
  price_above: '价格高于阈值',
  price_below: '价格低于阈值',
  daily_change: '日涨跌幅超阈值',
  margin_change: '融资余额5日变化超阈值',
  new_filing: '新公告',
};

export function Watchlist() {
  const me = useMe();
  const realtime = me.data?.research_protocol === 'v0.4';
  const items = useQuery({
    queryKey: ['watchlist'],
    queryFn: () => get<{ items: WatchItem[] }>('/api/v1/watchlist'),
    staleTime: 10_000,
  });
  const [adding, setAdding] = useState(false);
  const [researchItem, setResearchItem] = useState<WatchItem | null>(null);
  const [selection, setQuoteRequirement] = useState<QuoteRequirement>('latest_available');
  const allowRealtime = me.data?.realtime_trade_available === true;
  const quoteRequirement = allowRealtime ? selection : 'latest_available';
  const qc = useQueryClient();
  const nav = useNavigate();
  const add = useMutation({
    mutationFn: async (entity_id: string) => {
      await post<WatchItem>('/api/v1/watchlist', { entity_id }, { idem: newKey() });
      qc.invalidateQueries({ queryKey: ['watchlist'] });
      setAdding(false);
    },
  });
  const analyze = useMutation({
    mutationFn: async (subject_id: string) => {
      if (realtime && !quoteRequirement) throw new Error('请选择行情要求');
      const r = await post<PlanResult>(
        '/api/v1/research-plans',
        {
          skill_id: 'stock_analysis',
          subject_id,
          params: {},
          ...(realtime ? { quote_requirement: quoteRequirement } : {}),
        },
        { idem: newKey(), protocol: realtime ? 'v0.4' : undefined }
      );
      if (r.kind === 'plan' && r.plan) {
        nav({ to: '/app/plan/$id', params: { id: r.plan.id } });
        return;
      }
      throw new Error(r.unsupported_reason ?? '暂时无法为该标的生成研究计划');
    },
  });
  const remove = async (id: string) => {
    if (!confirm('移出自选股？')) return;
    try {
      await api<void>('DELETE', `/api/v1/watchlist/${id}`);
      qc.invalidateQueries({ queryKey: ['watchlist'] });
    } catch (e) {
      alert('操作失败：' + (e instanceof Error ? e.message : String(e)));
    }
  };
  return (
    <main>
      {me.data?.synthetic && <SyntheticBanner />}
      <header className="app-head">
        <strong className="logo">VERA</strong>
        <nav>
          <Link to="/app">工作台</Link>
          <Link to="/app/reports">报告</Link>
        </nav>
      </header>
      <h1>自选股</h1>
      <button onClick={() => setAdding(true)}>添加</button>
      {items.isLoading && <Loading />}
      {items.error && <ErrorBox error={items.error} />}
      {analyze.error && <ErrorBox error={analyze.error} />}
      {items.data?.items.length === 0 && <Empty>自选股为空。</Empty>}
      {items.data && items.data.items.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>标的</th>
              <th>最新价</th>
              <th>涨跌幅</th>
              <th>数据日期</th>
              <th>已触发提醒</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {items.data.items.map((it) => {
              const close = it.snapshot ? Number(it.snapshot.close) : null;
              const chg = it.snapshot ? Number(it.snapshot.change_pct) : null;
              return (
                <tr key={it.id}>
                  <td>
                    <strong>{it.entity.name}</strong>{' '}
                    <span className="muted">{it.entity.symbol}</span>
                  </td>
                  <td>{close !== null ? close.toFixed(2) : '—'}</td>
                  <td className={chg === null ? '' : chg > 0 ? 'up' : chg < 0 ? 'down' : ''}>
                    {chg !== null ? (chg > 0 ? '+' : '') + chg.toFixed(2) + '%' : '—'}
                  </td>
                  <td>{it.snapshot?.trade_date ?? '—'}</td>
                  <td>
                    {it.snapshot && it.snapshot.triggered.length > 0
                      ? it.snapshot.triggered.map((k) => (
                          <Tag key={k} tone="warn">
                            {ruleLabel[k] ?? k}
                          </Tag>
                        ))
                      : '—'}
                  </td>
                  <td>
                    <button
                      className="ghost small"
                      disabled={!me.data || analyze.isPending}
                      onClick={() => {
                        if (realtime) {
                          setQuoteRequirement('latest_available');
                          setResearchItem(it);
                          analyze.reset();
                        } else {
                          analyze.mutate(it.entity.id);
                        }
                      }}
                    >
                      分析
                    </button>
                    <button className="ghost small" onClick={() => remove(it.id)}>
                      移出
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {researchItem && (
        <Dialog title={`研究 ${researchItem.entity.name}`} onClose={() => setResearchItem(null)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              analyze.mutate(researchItem.entity.id);
            }}
          >
            <QuoteRequirementField
              value={quoteRequirement}
              onChange={setQuoteRequirement}
              allowRealtime={allowRealtime}
            />
            <button type="submit" disabled={!quoteRequirement || analyze.isPending}>
              {analyze.isPending ? '准备中…' : '生成研究计划'}
            </button>
            {analyze.error && <ErrorBox error={analyze.error} />}
          </form>
        </Dialog>
      )}
      {adding && (
        <div className="overlay" onClick={() => setAdding(false)}>
          <div className="dialog" onClick={(e) => e.stopPropagation()}>
            <div className="dialog-head">
              <h2>添加自选股</h2>
              <button className="ghost" onClick={() => setAdding(false)}>
                ×
              </button>
            </div>
            <EntitySearch
              kinds={['stock']}
              onPick={(e) => add.mutate(e.id)}
              placeholder="输入公司名称或代码"
            />
            {add.error && <ErrorBox error={add.error} />}
          </div>
        </div>
      )}
    </main>
  );
}
