import React from 'react';
import {createRoot} from 'react-dom/client';
import './style.css';

function App(){return <main>
 <header><strong>VERA</strong><span>金融研究 Agent · 开发预览</span></header>
 <section className="intro"><p className="eyebrow">面向基金经理与个人投资者</p><h1>从一份财报，<br/>读懂结论背后的证据。</h1><p>Vera 将整理财务数据、核验计算，并让每个关键结论都能回到原始材料。</p><p className="notice" role="status">当前为工程起始页。登录、研究任务与真实报告正在开发，尚未开放试用。</p><button disabled>研究功能开发中</button></section>
 <section className="grid" aria-label="计划提供的研究能力">{[
  ['01','年度业绩点评','固定期间与比较口径，解释收入、利润、现金流和财务结构。'],
  ['02','逐条核查依据','查看原文位置、数据版本与计算公式，区分事实和推断。'],
  ['03','明确未知与限制','证据不足时说明缺失，不用流畅文字代替依据。']
 ].map(([n,title,body])=><article key={n}><span>{n}</span><h2>{title}</h2><p>{body}</p></article>)}</section>
 <footer>首版聚焦单公司年度财报点评。所有示例将显著标注；不会以模拟数据冒充真实研究。</footer>
 </main>}

createRoot(document.getElementById('root')!).render(<React.StrictMode><App/></React.StrictMode>);
