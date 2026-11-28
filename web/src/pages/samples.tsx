import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useCallback } from 'react';
import { get, type Report, type ChartSeries, type Evidence, type Calculation } from '../api/client';
import { ReportView } from '../components/ReportView';
import { ErrorBox, Loading, VeraBrand } from '../components/ui';

export function Sample({ skill }: { skill: string }) {
  const report = useQuery({
    queryKey: ['sample', skill],
    queryFn: () => get<Report>(`/api/v1/samples/${skill}`),
  });

  const loaders = {
    series: useCallback(
      async (sid: string) => get<ChartSeries>(`/api/v1/samples/${skill}/series/${sid}`),
      [skill]
    ),
    evidence: useCallback(async (eid: string) => get<Evidence>(`/api/v1/evidence/${eid}`), []),
    calculation: useCallback(
      async (cid: string) => get<Calculation>(`/api/v1/calculations/${cid}`),
      []
    ),
  };

  return (
    <main className="reader-page reader-sample">
      <header className="site-header">
        <div className="site-header__inner">
          <Link to="/" className="site-header__brand">
            <VeraBrand size={28} />
          </Link>
          <nav className="hdr-actions">
            <Link to="/" className="hdr-btn hdr-btn--secondary">
              返回首页
            </Link>
          </nav>
        </div>
      </header>
      <div className="content">
        {report.isLoading && <Loading />}
        {report.error && <ErrorBox error={report.error} />}
        {report.data && <ReportView report={report.data} loaders={loaders} />}
      </div>
    </main>
  );
}

export function Privacy() {
  return (
    <main>
      <header className="site-header">
        <div className="site-header__inner">
          <Link to="/" className="site-header__brand">
            <VeraBrand size={28} />
          </Link>
        </div>
      </header>
      <div className="content" style={{ maxWidth: 720, margin: '0 auto', padding: 40 }}>
        <h1>数据与隐私说明</h1>
        <p className="muted">最后更新：2026 年 11 月</p>

        <h2>数据来源</h2>
        <p>研究数据来自登记的授权数据源。</p>
        <ul>
          <li>Choice（东方财富）A 股行情、财务、资金、公告</li>
          <li>TuShare Pro A 股收盘行情、资金及披露数据</li>
          <li>仿真数据（内部测试与演示）</li>
        </ul>
        <p>每条数据都标注来源、口径、时点。报告可追溯到原始数据记录和厂商文档。</p>
        <p>通用金融知识问答可调用外部搜索查证，相关网页链接随答复展示。</p>

        <h2>工作区隔离</h2>
        <p>
          每个用户的工作区（对话、任务、报告、自选股）默认私密。Vera
          采用行级安全（RLS）、加密传输和权限控制保护数据。
        </p>

        <h2>模型调用</h2>
        <p>研究模型基于已准备的证据编写和核验结论。取数、计算和过滤由系统执行。</p>
        <p>
          处理对话时，当前消息、近期相关问答和研究范围会发送给服务配置的模型。研究写作与核验使用所需的已授权资料。对话在工作区中保留
          90 天，也可以提前删除。
        </p>
        <p>启用知识搜索时，系统会提炼供外部服务查证的通用问题。搜索请求只包含该问题。</p>

        <h2>联系方式</h2>
        <p>如有疑问，请联系 support@vera.finance（示例，未实际配置）</p>
      </div>
    </main>
  );
}
