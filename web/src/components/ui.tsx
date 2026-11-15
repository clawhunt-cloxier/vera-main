import { useEffect, useRef, type ReactNode } from 'react';
import { ApiError } from '../api/client';

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const e = error instanceof ApiError ? error : null;
  return (
    <div className="error" role="alert">
      <strong>{e?.status === 429 ? '已达到使用上限' : '出错了'}</strong>{' '}
      {e ? e.message : String(error)}
      {e?.requestId && <span className="muted"> （请求编号 {e.requestId}）</span>}
    </div>
  );
}

export function Loading({ label = '加载中…' }: { label?: string }) {
  return (
    <div className="loading" role="status" aria-live="polite">
      <div className="spinner" aria-hidden="true"></div>
      <p className="muted">{label}</p>
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="empty">{children}</p>;
}

export function Tag({
  children,
  tone,
}: {
  children: ReactNode;
  tone?: 'ok' | 'warn' | 'bad' | 'info';
}) {
  return <span className={'tag ' + (tone ?? '')}>{children}</span>;
}

export function Dialog({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLElement>('input,select,textarea,button')?.focus();
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      prev?.focus();
    };
  }, [onClose]);
  return (
    <div className="overlay" onClick={onClose}>
      <div
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        ref={ref}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="dialog-head">
          <h2>{title}</h2>
          <button className="ghost" onClick={onClose} aria-label="关闭">
            ×
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

export function SyntheticBanner() {
  return (
    <div className="banner" role="note">
      <strong>演示环境</strong> — 数据或模型输出为模拟内容，报告不对应任何真实公司或市场，仅用于演示功能。
    </div>
  );
}

/* ========================================================================
   增强型按钮组件
   ======================================================================== */

export function Button({
  children,
  variant = 'default',
  size = 'md',
  disabled,
  className,
  ...props
}: {
  children: ReactNode;
  variant?: 'default' | 'primary' | 'ghost' | 'warn' | 'ok';
  size?: 'sm' | 'md' | 'lg';
  disabled?: boolean;
  className?: string;
} & React.ButtonHTMLAttributes<HTMLButtonElement>) {
  const classList = [
    'btn',
    `btn--${variant}`,
    `btn--${size}`,
    className,
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <button {...props} disabled={disabled} className={classList}>
      {children}
    </button>
  );
}

/* ========================================================================
   加载骨架屏
   ======================================================================== */

export function Skeleton({ height = 20, style }: { height?: number; style?: React.CSSProperties }) {
  return <div className="skeleton" style={{ height: `${height}px`, ...style }} aria-hidden="true" />;
}

export function SkeletonCard() {
  return (
    <div className="card">
      <Skeleton height={24} />
      <Skeleton height={16} style={{ marginTop: 12 }} />
      <Skeleton height={16} style={{ marginTop: 8 }} />
    </div>
  );
}
