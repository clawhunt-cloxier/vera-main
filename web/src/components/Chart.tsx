import { useEffect, useRef, useState } from 'react';
import type { ChartSeries } from '../api/client';

export function Chart({ load, title }: { load: () => Promise<ChartSeries>; title: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [err, setErr] = useState('');
  const [data, setData] = useState<ChartSeries | null>(null);
  useEffect(() => {
    let disposed = false;
    let chart: { dispose: () => void; resize: () => void } | null = null;
    const onResize = () => chart?.resize();
    (async () => {
      try {
        const [s, echarts] = await Promise.all([load(), import('echarts')]);
        if (disposed || !ref.current) return;
        setData(s);
        const c = echarts.init(ref.current, undefined, { renderer: 'svg' });
        chart = c;
        const x = s.series[0]?.points.map((p) => p[0]) ?? [];
        c.setOption({
          animation: false,
          tooltip: { trigger: 'axis' },
          legend: { top: 0 },
          grid: { left: 48, right: 16, top: 32, bottom: 32 },
          xAxis: { type: 'category', data: x },
          yAxis: { type: 'value', name: s.unit, scale: true },
          series: s.series.map((l) => ({
            name: l.name,
            type: s.kind === 'bar' ? 'bar' : 'line',
            areaStyle: s.kind === 'area' ? {} : undefined,
            showSymbol: false,
            connectNulls: false,
            data: l.points.map((p) => (p[1] === null ? null : Number(p[1]))),
          })),
        });
        window.addEventListener('resize', onResize);
      } catch {
        setErr('图表加载失败');
      }
    })();
    return () => {
      disposed = true;
      window.removeEventListener('resize', onResize);
      chart?.dispose();
    };
  }, [load]);
  return (
    <figure className="chart">
      <figcaption>{title}{data?.note ? '（' + data.note + '）' : ''}</figcaption>
      {err ? <p className="muted">{err}</p> : <div ref={ref} className="chart-box" role="img" aria-label={title} />}
    </figure>
  );
}
