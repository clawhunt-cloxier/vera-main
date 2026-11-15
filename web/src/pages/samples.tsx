import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useCallback } from 'react';
import { get, type Report, type ChartSeries, type Evidence, type Calculation } from '../api/client';
import { ReportView } from '../components/ReportView';
import { ErrorBox, Loading } from '../components/ui';

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
    <main>
      <header className="site-header">
        <div className="site-header__inner">
          <Link to="/" className="site-header__brand">
            VERA
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
            VERA
          </Link>
        </div>
      </header>
      <div className="content" style={{ maxWidth: 720, margin: '0 auto', padding: 40 }}>
        <h1>数据与隐私说明</h1>
        <p className="muted">最后更新：2026 年 11 月</p>

        <h2>数据来源</h2>
        <p>
          VERA 不搜索互联网，不接受用户上传文件或 URL。所有用于研究的数据都来自登记的授权数据源：
        </p>
        <ul>
          <li>Choice（东方财富）A 股行情、财务、资金、公告</li>
          <li>仿真数据（内部测试与演示）</li>
        </ul>
        <p>
          每条数据都标注来源、口径、时点。报告可追溯到原始数据记录和厂商文档。
        </p>

        <h2>工作区隔离</h2>
        <p>
          每个用户的工作区（任务、报告、自选股）默认私密，不向其他用户展示。
          VERA 采用行级安全（RLS）、加密传输和权限控制保护数据。
        </p>

        <h2>模型调用</h2>
        <p>
          研究由 Claude 5.5 Opus 模型完成。模型不调用工具，不决定数据查询，不决定算术计算。
          所有数据准备、计算和过滤由确定性代码完成。模型只接收已准备好的证据，输出研究文本。
        </p>
        <p>
          模型不会记住你的任务内容。每次研究是独立的，Anthropic 不会用你的输入训练模型。
        </p>

        <h2>联系方式</h2>
        <p>如有疑问，请联系 support@vera.finance（示例，未实际配置）</p>
      </div>
    </main>
  );
}
