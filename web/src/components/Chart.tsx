import type { ECharts, EChartsOption, SeriesOption } from 'echarts';
import { useEffect, useRef, useState } from 'react';
import type { ChartSeries } from '../api/client';

const colors = [
  '#145ec5',
  '#a85d28',
  '#1b7568',
  '#6b5797',
  '#a23631',
  '#086880',
  '#527521',
  '#963f78',
  '#534234',
];
const strokes = ['solid', 'dashed', 'dotted'] as const;
const tickFormat = new Intl.NumberFormat('zh-CN', { maximumSignificantDigits: 8 });

export function Chart({
  load,
  seriesId,
  title,
}: {
  load: (id: string) => Promise<ChartSeries>;
  seriesId: string;
  title: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [result, setResult] = useState<{
    seriesId: string;
    load: typeof load;
    data: { series: ChartSeries; draw: (() => ECharts | undefined) | null } | null;
    selected?: Record<string, boolean>;
  } | null>(null);
  const current = result?.seriesId === seriesId && result.load === load ? result : null;
  const data = current?.data?.series;
  const draw = current?.data?.draw;
  function toggleSeries(name: string) {
    const plot = draw?.();
    if (!plot || !current) return;
    const selected = { ...current.selected, [name]: current.selected?.[name] === false };
    plot.setOption({ legend: { selected } });
    setResult({ ...current, selected });
  }
  useEffect(() => {
    let disposed = false;
    let chart: ECharts | undefined;
    let stopObserving: (() => void) | undefined;
    (async () => {
      try {
        const [s, echarts] = await Promise.all([load(seriesId), import('echarts')]);
        if (disposed || !ref.current) return;
        const box = ref.current;
        const dates = [...new Set(s.series.flatMap((line) => line.points.map(([date]) => date)))]
          .filter(Boolean)
          .sort();
        const bars = s.kind === 'bar';
        const values = s.series.map((line) => {
          const byDate = new Map(line.points);
          return dates.map((date) => {
            const value = byDate.get(date);
            return value?.trim() && Number.isFinite(Number(value)) ? value : null;
          });
        });
        if (!values.some((line) => line.some((value) => value !== null))) {
          setResult({ seriesId, load, data: { series: s, draw: null } });
          return;
        }
        const series = s.series.map((line, index): SeriesOption => {
          const points = values[index];
          const common = {
            name: line.name,
            data: points,
            tooltip: { valueFormatter: (_: unknown, i: number) => points[i] ?? '—' },
          };
          if (bars) return { ...common, type: 'bar', barMaxWidth: 28 };
          return {
            ...common,
            type: 'line',
            lineStyle: { width: 2, type: strokes[index % strokes.length] },
            areaStyle: s.kind === 'area' && index === 0 ? { opacity: 0.13 } : undefined,
            symbol: 'circle',
            showAllSymbol: true,
            symbolSize: (_, point) =>
              dates.length <= 10 ||
              (points[point.dataIndex - 1] == null && points[point.dataIndex + 1] == null)
                ? 5
                : 0,
            connectNulls: false,
          };
        });
        const options = {
          animation: false,
          color: colors,
          textStyle: { fontFamily: getComputedStyle(box).fontFamily },
          tooltip: { trigger: 'axis', renderMode: 'richText', confine: true },
          legend: { show: false },
          grid: {
            left: 12,
            right: 12,
            top: 18,
            bottom: 12,
            outerBoundsMode: 'same',
            outerBoundsContain: 'axisLabel',
          },
          xAxis: {
            type: 'category',
            boundaryGap: bars,
            data: dates,
            axisLine: { lineStyle: { color: '#9ba9b8' } },
            axisTick: { show: false },
            axisLabel: {
              color: '#586474',
              fontSize: 11,
              margin: 10,
              hideOverlap: true,
              showMinLabel: true,
              showMaxLabel: true,
              formatter: (date: string) =>
                s.x === 'report_period' && /^\d{4}-12-31$/.test(date) ? date.slice(0, 4) : date,
            },
          },
          yAxis: {
            type: 'value',
            scale: !bars,
            splitNumber: 4,
            axisLabel: {
              color: '#586474',
              fontSize: 11,
              formatter: (value: number) => tickFormat.format(value),
            },
            splitLine: { lineStyle: { color: '#dfe4eb' } },
          },
          series,
        } satisfies EChartsOption;
        function draw() {
          if (disposed || chart) return chart;
          try {
            chart = echarts.init(box, undefined, { renderer: 'svg' });
            chart.setOption(options);
            return chart;
          } catch {
            chart?.dispose();
            chart = undefined;
            stopObserving?.();
            setResult({ seriesId, load, data: null });
          }
        }
        const fit = (width: number, height: number) => {
          const plot = draw();
          if (
            plot &&
            width > 0 &&
            height > 0 &&
            (width !== plot.getWidth() || height !== plot.getHeight())
          ) {
            plot.resize({ width, height });
          }
        };
        const size = new ResizeObserver(([entry]) => {
          fit(Math.round(entry.contentRect.width), Math.round(entry.contentRect.height));
        });
        const view = new IntersectionObserver(
          ([entry]) => {
            if (entry.isIntersecting) size.observe(box);
            else size.unobserve(box);
          },
          { rootMargin: '160px 0px' }
        );
        const print = () => fit(box.clientWidth, box.clientHeight);
        const media = matchMedia('print');
        const syncPrint = () => {
          if (media.matches) print();
        };
        view.observe(box);
        window.addEventListener('beforeprint', print);
        media.addEventListener('change', syncPrint);
        stopObserving = () => {
          view.disconnect();
          size.disconnect();
          window.removeEventListener('beforeprint', print);
          media.removeEventListener('change', syncPrint);
        };
        setResult({ seriesId, load, data: { series: s, draw } });
        syncPrint();
      } catch {
        if (disposed) return;
        chart?.dispose();
        chart = undefined;
        setResult({ seriesId, load, data: null });
      }
    })();
    return () => {
      disposed = true;
      stopObserving?.();
      chart?.dispose();
    };
  }, [load, seriesId]);
  return (
    <figure className="chart">
      <figcaption>
        <span>{title}</span>
        {data?.unit && <small>单位 {data.unit}</small>}
      </figcaption>
      {draw && data && (
        <ul className="chart-legend" aria-label="图例" data-kind={data.kind}>
          {data.series.map((line, index) => (
            <li key={index}>
              <button
                type="button"
                aria-pressed={current.selected?.[line.name] !== false}
                onClick={() => toggleSeries(line.name)}
              >
                <i
                  aria-hidden="true"
                  data-stroke={strokes[index % strokes.length]}
                  style={{ color: colors[index % colors.length] }}
                />
                {line.name}
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="chart-plot">
        <div ref={ref} className="chart-box" role="img" aria-label={title} aria-hidden={!draw} />
        {!draw && (
          <p className="muted" role="status">
            {!current ? '正在读取图表…' : data ? '暂无可绘制的数据' : '图表加载失败'}
          </p>
        )}
      </div>
      {data?.note && <p className="chart-note">{data.note}</p>}
    </figure>
  );
}
