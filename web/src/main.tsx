import React from 'react';
import {createRoot} from 'react-dom/client';
import './style.css';

function App(){return <main>
 <header><strong>VERA</strong><span>金融投研 Agent · 开发预览</span></header>
 <section className="intro"><p className="eyebrow">面向基金经理与个人投资者</p><h1>围绕你的研究问题，<br/>找到结论背后的证据。</h1><p>Vera 将整理财务数据、核验计算，并让每个关键结论都能回到原始材料。</p><p className="notice" role="status">当前为工程起始页。登录、研究任务与真实报告正在开发，尚未开放试用。</p><button disabled>研究功能开发中</button></section>
 <section className="grid" aria-label="计划提供的研究能力">{[
  ['01','自主选择研究对象','围绕个股、行业或其他金融标的，结合已接入的数据开展研究。'],
  ['02','逐条核查依据','查看原文位置、数据版本与计算公式，区分事实和推断。'],
  ['03','明确未知与限制','证据不足时说明缺失，不用流畅文字代替依据。']
 ].map(([n,title,body])=><article key={n}><span>{n}</span><h2>{title}</h2><p>{body}</p></article>)}</section>
 <footer>Vera 连接金融数据，为你的投研问题提供有依据的分析。所有示例将显著标注；不会以模拟数据冒充真实研究。</footer>
 </main>}

createRoot(document.getElementById('root')!).render(<React.StrictMode><App/></React.StrictMode>);
