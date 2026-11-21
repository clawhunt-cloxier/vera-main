import { Link } from '@tanstack/react-router';
import { useState, type ReactNode } from 'react';
import { post } from '../api/client';
import { useMe } from '../api/hooks';
import { ErrorBox, SyntheticBanner } from './ui';
import './workspace.css';

export function WorkspaceFrame({ children }: { children: ReactNode }) {
  const me = useMe();
  const [signingOut, setSigningOut] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const signOut = async () => {
    setSigningOut(true);
    setError(null);
    try {
      await post('/auth/logout', {});
      window.location.assign('/');
    } catch (err) {
      setError(err);
      setSigningOut(false);
    }
  };
  return (
    <div className="desk">
      <header className="desk-header">
        <div className="desk-bar">
          <Link to="/app" className="desk-brand" aria-label="VERA 工作台">
            <b aria-hidden="true">V</b> VERA
          </Link>
          <nav aria-label="工作区导航">
            <Link to="/app" activeOptions={{ exact: true }}>
              工作台
            </Link>
            <Link to="/app/reports">报告</Link>
            <Link to="/app/watchlist">自选股</Link>
          </nav>
          {me.data && (
            <details className="desk-account">
              <summary aria-label="账户菜单">
                <b aria-hidden="true">{Array.from(me.data.display_name || '我')[0]}</b>
                <span>{me.data.display_name || '账户'}</span>
                <i aria-hidden="true">⌄</i>
              </summary>
              <div>
                <button type="button" disabled={signingOut} onClick={signOut}>
                  {signingOut ? '正在退出…' : '退出登录'}
                </button>
              </div>
            </details>
          )}
        </div>
      </header>
      <main className="desk-main">
        {me.data?.synthetic && <SyntheticBanner />}
        {me.error && <ErrorBox error={me.error} />}
        {error != null && <ErrorBox error={error} />}
        {children}
      </main>
    </div>
  );
}
