import { Link } from '@tanstack/react-router';
import { ChevronDown, LogOut } from 'lucide-react';
import { useId, useRef, useState, type ReactNode } from 'react';
import { post } from '../api/client';
import { useMe } from '../api/hooks';
import { ErrorBox, SyntheticBanner, VeraBrand } from './ui';
import './workspace.css';

export function WorkspaceFrame({ children }: { children: ReactNode }) {
  const me = useMe();
  const [accountOpen, setAccountOpen] = useState(false);
  const accountButton = useRef<HTMLButtonElement>(null);
  const accountMenu = useId();
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
          <Link to="/app" className="desk-brand" aria-label="Vera 工作台">
            <VeraBrand size={34} />
          </Link>
          <nav aria-label="工作区导航">
            <Link to="/app" activeOptions={{ exact: true }}>
              工作台
            </Link>
            <Link to="/app/reports">报告</Link>
            <Link to="/app/watchlist">自选股</Link>
          </nav>
          {me.data && (
            <div
              className="desk-account"
              onBlur={(event) => {
                if (!event.currentTarget.contains(event.relatedTarget)) setAccountOpen(false);
              }}
              onKeyDown={(event) => {
                if (accountOpen && event.key === 'Escape') {
                  setAccountOpen(false);
                  accountButton.current?.focus();
                }
              }}
            >
              <button
                ref={accountButton}
                type="button"
                className="desk-account-toggle"
                aria-label="账户菜单"
                aria-expanded={accountOpen}
                aria-controls={accountMenu}
                onClick={() => setAccountOpen((open) => !open)}
              >
                <b aria-hidden="true">{Array.from(me.data.display_name || '我')[0]}</b>
                <span>{me.data.display_name || '账户'}</span>
                <ChevronDown size={14} aria-hidden="true" />
              </button>
              <div id={accountMenu} className="desk-account-options" inert={!accountOpen}>
                <button type="button" disabled={signingOut} onClick={signOut}>
                  <LogOut size={15} aria-hidden="true" />
                  {signingOut ? '正在退出…' : '退出登录'}
                </button>
              </div>
            </div>
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
