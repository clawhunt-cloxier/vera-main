import { Link } from '@tanstack/react-router';

export function Landing() {
  return (
    <>
      <header className="site-header">
        <div className="site-header__inner">
          <a href="/" className="site-header__brand">
            VERA
          </a>
          <nav className="hdr-actions">
            <a href="/auth/login" className="hdr-btn hdr-btn--secondary">
              登录
            </a>
          </nav>
        </div>
      </header>

      <main className="landing">
        <section className="top">
          <div className="intro">
            <p className="eyebrow">投研 AI Agent</p>
            <h1>可信金融投研，一句话交付</h1>
            <p style={{ fontSize: 18, marginTop: 20, marginBottom: 32, color: 'var(--text-secondary)' }}>
              只用授权数据，自动完成研究。每个数字可溯源，每个结论可验证。
            </p>
            <a href="/app" className="btn btn--primary">
              开始使用
            </a>
          </div>
        </section>

        <section style={{ paddingTop: 80 }}>
          <h2>四大研究模块</h2>
          <div className="grid">
            <div className="card interactive">
              <h3>个股分析</h3>
              <p className="muted">财务、估值、行情、资金、风险的综合分析</p>
              <Link to="/sample/$skill" params={{ skill: 'stock_analysis' as any }} className="btn-link">
                查看示例
              </Link>
            </div>
            <div className="card interactive">
              <h3>资金动向</h3>
              <p className="muted">融资融券、龙虎榜、大宗、陆股通与公募持仓</p>
              <Link to="/sample/$skill" params={{ skill: 'capital_flow' as any }} className="btn-link">
                查看示例
              </Link>
            </div>
            <div className="card interactive">
              <h3>深度研究</h3>
              <p className="muted">对公司、行业或公司+行业的长篇研究报告</p>
              <Link to="/sample/$skill" params={{ skill: 'deep_research' as any }} className="btn-link">
                查看示例
              </Link>
            </div>
            <div className="card interactive">
              <h3>自选股管理</h3>
              <p className="muted">维护关注列表、收盘提醒、查看历史研究</p>
            </div>
          </div>
        </section>

        <section style={{ paddingTop: 80, paddingBottom: 120 }}>
          <h2>产品特点</h2>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(300px, 1fr))', gap: 40 }}>
            <div>
              <h3>只用授权数据</h3>
              <p className="muted">研究不搜索互联网，不接受用户上传。所有数据都来自登记的授权源。</p>
            </div>
            <div>
              <h3>可溯源的数字</h3>
              <p className="muted">每条数字可追溯到来源、口径、时点和原文。伪造、错位、颠倒会被拦截。</p>
            </div>
            <div>
              <h3>结论分类展示</h3>
              <p className="muted">区分事实、计算、推断、引述和情景。让你分清 AI 在说什么。</p>
            </div>
            <div>
              <h3>私人工作区</h3>
              <p className="muted">邀请制注册。任务、报告、自选股默认私密。隔离、权限、加密都有保障。</p>
            </div>
            <div>
              <h3>Full / Limited 标记</h3>
              <p className="muted">数据不足的章节会被标记或省略。告诉你报告有什么缺陷。</p>
            </div>
            <div>
              <h3>证据侧栏</h3>
              <p className="muted">报告中的每个数字可点开查看计算过程、数据来源和原文片段。</p>
            </div>
          </div>
        </section>

        <footer>
          <p className="muted">
            <Link to="/privacy">数据与隐私说明</Link> ·
            {' '}<span>© 2026 Vera</span>
          </p>
        </footer>
      </main>
    </>
  );
}
