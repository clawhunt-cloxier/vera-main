import React from 'react';
import { createRoot } from 'react-dom/client';
import './style.css';

function App() {
  return (
    <main>
      <header>
        <strong>VERA</strong>
        <span>金融投研 Agent · 开发预览</span>
      </header>
      <section className="intro">
        <p className="eyebrow">面向一线投研人员与个人投资者</p>
        <h1>
          一句话提出研究需求，
          <br />
          每个数字都能追溯来源。
        </h1>
        <p>Vera 只使用已接入的授权数据，自动完成取数、计算、写作与核验，交付标准格式的研报。</p>
        <p className="notice" role="status">
          当前为工程起始页。登录、数据接入与研究功能正在开发，尚未开放试用。
        </p>
        <button disabled>研究功能开发中</button>
      </section>
      <section className="grid" aria-label="计划提供的研究能力">
        {[
          [
            '01',
            '研报生成',
            '对 A 股公司或申万行业生成深度研究报告，管理层表述与外部观点单独标注。',
          ],
          [
            '02',
            '个股分析',
            '财务、估值分位、行情、资金与风险，只陈述事实和条件化情景，不给评级与目标价。',
          ],
          [
            '03',
            '资金动向',
            '融资融券、龙虎榜、大宗交易、陆股通与公募持仓，厂商口径数据注明来源。',
          ],
          ['04', '自选股', '收盘后跟踪关注列表与提醒，一键发起研究，保留历史报告。'],
        ].map(([n, title, body]) => (
          <article key={n}>
            <span>{n}</span>
            <h2>{title}</h2>
            <p>{body}</p>
          </article>
        ))}
      </section>
      <footer>
        证据不足时明确说明缺失，不用流畅文字代替依据。所有示例将显著标注，不会以模拟数据冒充真实研究。
      </footer>
    </main>
  );
}

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
