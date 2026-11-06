import { Link } from '@tanstack/react-router';
import { useCallback, useEffect, useState } from 'react';
import type { Calculation, ChartSeries, Evidence, Report } from '../api/client';
import { useMe } from '../api/hooks';
import { ReportView } from '../components/ReportView';
import { Loading, SyntheticBanner } from '../components/ui';

const modules = [
  ['研报生成', '对 A 股公司或申万行业生成深度研究报告；管理层表述与外部观点单独标注，缺少资料的章节明确说明。', 'deep_research'],
  ['个股分析', '财务、盈利质量、估值分位、行情、资金与公司事项，只陈述事实与条件化情景，不给评级与目标价。', 'stock_analysis'],
  ['资金动向', '融资融券、龙虎榜、大宗交易、陆股通与公募持仓；厂商加工口径的数据注明来源与定义。', 'capital_flow'],
  ['自选股', '收盘后跟踪关注列表与提醒，一键发起研究，保留历史报告。', ''],
];

export function Landing() {
  const me = useMe(true);
  return (
    <main className="landing">
      <header className="top">
        <strong className="logo">VERA</strong>
        <nav>
          <Link to="/privacy">数据与隐私</Link>
          {me.data ? <Link to="/app" className="button">进入工作台</Link> : <a className="button" href="/auth/login?return_to=/app">登录</a>}
        </nav>
      </header>
      <section className="intro">
        <p className="eyebrow">面向一线投研人员与个人投资者</p>
        <h1>一句话提出研究需求，<br />每个数字都能追溯来源。</h1>
        <p>Vera 只使用已接入的授权数据，按固定流程完成取数、计算、写作与核验，交付标准格式的研报。证据不足时明确说明缺失，不用流畅文字代替依据。</p>
        <p className="notice">试用采用邀请制。首次登录后输入邀请码即可开通私人工作区。</p>
      </section>
      <section className="grid" aria-label="研究能力">
        {modules.map(([title, body, skill]) => (
          <article key={title}>
            <h2>{title}</h2>
            <p>{body}</p>
            {skill && <Link to="/sample/$skill" params={{ skill }}>查看模拟样例</Link>}
          </article>
        ))}
      </section>
      <footer>Vera 生成的内容为 AI 生成，仅供研究参考，不构成投资建议。</footer>
    </main>
  );
}

// Samples are real pipeline outputs on the synthetic demo universe, exported by `vera harness export-samples`.
export function Sample({ skill }: { skill: string }) {
  const [data, setData] = useState<{ report: Report; series: Record<string, ChartSeries>; evidence: Record<string, Evidence>; calculations: Record<string, Calculation> } | null>(null);
  const [err, setErr] = useState('');
  useEffect(() => {
    fetch('/samples/' + skill + '.json').then((r) => (r.ok ? r.json() : Promise.reject(r.status))).then(setData, () => setErr('样例尚未生成'));
  }, [skill]);
  const loaders = {
    series: useCallback(async (id: string) => data!.series[id], [data]),
    evidence: useCallback(async (id: string) => data!.evidence[id] ?? Promise.reject('missing'), [data]),
    calculation: useCallback(async (id: string) => data!.calculations[id] ?? Promise.reject('missing'), [data]),
  };
  return (
    <main>
      <SyntheticBanner />
      <p><Link to="/">← 返回首页</Link></p>
      {err && <p className="muted">{err}</p>}
      {!data && !err && <Loading />}
      {data && <ReportView report={data.report} loaders={loaders} />}
    </main>
  );
}

export function Privacy() {
  return (
    <main className="prose">
      <p><Link to="/">← 返回首页</Link></p>
      <h1>数据与隐私</h1>
      <ul>
        <li>每个账号对应一个私人工作区，研究任务、报告、自选股与反馈默认只对本人可见。</li>
        <li>研究只使用平台接入并授权的数据源，不进行自由联网搜索。报告中的每个数字绑定数据版本或计算记录。</li>
        <li>身份标签（基金经理、研究员、个人投资者）只用于改进体验，不授予任何权限。</li>
        <li>模型调用发送的是结构化事实与写作要求；不发送你的账号信息。</li>
        <li>报告为 AI 生成内容，仅供研究参考，不构成投资建议。</li>
      </ul>
    </main>
  );
}
