import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { fmtTime, get, newKey, post, type S, type WatchAlert, type WatchItem } from '../api/client';
import { useMe } from '../api/hooks';
import { EntitySearch } from '../components/EntitySearch';
import { Empty, ErrorBox, Loading, SyntheticBanner, Tag } from '../components/ui';

export function Watchlist() {
  const me = useMe();
  const items = useQuery({ queryKey: ['watchlist'], queryFn: () => get<{ items: WatchItem[] }>('/api/v1/watchlist'), staleTime: 10_000 });
  const [adding, setAdding] = useState(false);
  const qc = useQueryClient();
  const nav = useNavigate();
  const add = useMutation({
    mutationFn: async (entity_id: string) => {
      await post('/api/v1/watchlist', { entity_id }, { idem: newKey() });
      qc.invalidateQueries({ queryKey: ['watchlist'] });
      setAdding(false);
    },
  });
  const remove = async (id: string) => {
    if (!confirm('移出自选股？')) return;
    try {
      await post(`/api/v1/watchlist/${id}/remove`, {}, { idem: newKey() });
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
      {items.data?.items.length === 0 && <Empty>自选股为空。</Empty>}
      {items.data && items.data.items.length > 0 && (
        <table>
          <thead><tr><th>标的</th><th>最新价</th><th>涨跌幅</th><th>更新时间</th><th>提醒</th><th></th></tr></thead>
          <tbody>
            {items.data.items.map((it) => (
              <tr key={it.id}>
                <td><strong>{it.entity.name}</strong> <span className="muted">{it.entity.symbol}</span></td>
                <td>{it.snapshot?.close?.toFixed(2) ?? '—'}</td>
                <td className={it.snapshot && it.snapshot.change_pct !== null ? (it.snapshot.change_pct > 0 ? 'up' : it.snapshot.change_pct < 0 ? 'down' : '') : ''}>
                  {it.snapshot?.change_pct !== null ? (it.snapshot.change_pct > 0 ? '+' : '') + it.snapshot.change_pct.toFixed(2) + '%' : '—'}
                </td>
                <td>{it.snapshot?.trade_date ?? '—'}</td>
                <td>{it.alerts.length > 0 ? <Link to="/app/watchlist/$id/alerts" params={{ id: it.id }}><Tag tone="warn">{it.alerts.length} 条</Tag></Link> : '—'}</td>
                <td>
                  <button className="ghost small" onClick={() => nav({ to: '/app/quick/stock_analysis', search: { code: it.entity.code } })}>分析</button>
                  <button className="ghost small" onClick={() => remove(it.id)}>移出</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {adding && (
        <div className="overlay" onClick={() => setAdding(false)}>
          <div className="dialog" onClick={(e) => e.stopPropagation()}>
            <div className="dialog-head"><h2>添加自选股</h2><button className="ghost" onClick={() => setAdding(false)}>×</button></div>
            <EntitySearch kinds={['stock']} onPick={(e) => add.mutate(e.id)} placeholder="输入公司名称或代码" />
            {add.error && <ErrorBox error={add.error} />}
          </div>
        </div>
      )}
    </main>
  );
}

export function Alerts({ id }: { id: string }) {
  const me = useMe();
  const alerts = useQuery({ queryKey: ['alerts', id], queryFn: () => get<{ item: WatchItem; alerts: WatchAlert[] }>(`/api/v1/watchlist/${id}/alerts`), staleTime: 10_000 });
  const qc = useQueryClient();
  const ack = async (aid: string) => {
    try {
      await post<S<'AckAlertResponse'>>(`/api/v1/watchlist/${id}/alerts/${aid}/ack`, {});
      qc.invalidateQueries({ queryKey: ['alerts', id] });
      qc.invalidateQueries({ queryKey: ['watchlist'] });
    } catch (e) {
      alert('操作失败：' + (e instanceof Error ? e.message : String(e)));
    }
  };
  return (
    <main>
      {me.data?.synthetic && <SyntheticBanner />}
      <p><Link to="/app/watchlist">← 返回自选股</Link></p>
      {alerts.isLoading && <Loading />}
      {alerts.data && (
        <>
          <h1>{alerts.data.item.entity.name} 提醒</h1>
          {alerts.data.alerts.length === 0 && <Empty>暂无提醒。</Empty>}
          {alerts.data.alerts.map((a) => (
            <div key={a.id} className="card">
              <div><Tag tone={a.severity === 'high' ? 'bad' : 'warn'}>{a.severity === 'high' ? '重要' : '一般'}</Tag> {a.event_type}</div>
              <p>{a.message}</p>
              <div className="muted">触发时间 {fmtTime(a.triggered_at)}</div>
              {!a.acknowledged_at && <button onClick={() => ack(a.id)}>标记已读</button>}
              {a.acknowledged_at && <span className="muted">已于 {fmtTime(a.acknowledged_at)} 标记已读</span>}
            </div>
          ))}
        </>
      )}
    </main>
  );
}
