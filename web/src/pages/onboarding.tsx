import { useMutation } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { post, type S } from '../api/client';
import { useMe } from '../api/hooks';
import { ErrorBox, Loading } from '../components/ui';

export function Onboarding() {
  // noRedirect=true：/onboarding 本身就是登录页，不能因为 401 再跳回来死循环。
  const me = useMe(true);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [invite, setInvite] = useState('');
  const [persona, setPersona] = useState<S<'OnboardingInput'>['persona']>('individual');
  const nav = useNavigate();
  const params = new URLSearchParams(typeof window !== 'undefined' ? window.location.search : '');
  const returnTo = params.get('return_to') || '/app';
  const mut = useMutation({
    mutationFn: async () => {
      if (me.data && !me.data.onboarded) {
        await post('/api/v1/onboarding', { invite_code: invite.trim(), persona });
        window.location.assign('/app');
        return;
      }
      const resp = await post<{ success: boolean; return_to: string }>('/auth/login', {
        username,
        password,
        return_to: returnTo,
      });
      if (resp.success) {
        window.location.assign(resp.return_to || returnTo);
      }
    },
  });
  if (me.isLoading)
    return (
      <main>
        <Loading />
      </main>
    );
  // 已登录且有工作区 → 直接去工作台。
  if (me.data?.onboarded) {
    nav({ to: '/app' });
    return null;
  }
  return (
    <main className="prose">
      <h1>{me.data ? '开通工作区' : '登录工作区'}</h1>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          mut.mutate();
        }}
      >
        {me.data ? (
          <label>
            邀请码
            <input value={invite} onChange={(e) => setInvite(e.target.value)} required />
          </label>
        ) : (
          <>
            <label>
              账号
              <input
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="输入账号"
                required
              />
            </label>
            <label>
              密码
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="输入密码"
                required
              />
            </label>
          </>
        )}
        {me.data && (
          <label>
            你的身份
            <select value={persona} onChange={(e) => setPersona(e.target.value as typeof persona)}>
              <option value="individual">个人投资者</option>
              <option value="analyst">研究员</option>
              <option value="fund_manager">基金经理</option>
              <option value="unspecified">暂不说明</option>
            </select>
            <span className="muted">只用于改进体验，不授予任何权限。</span>
          </label>
        )}
        <button
          type="submit"
          disabled={mut.isPending || (me.data ? !invite.trim() : !username || !password)}
        >
          {mut.isPending ? '提交中…' : me.data ? '开通工作区' : '登录'}
        </button>
      </form>
      {mut.error && <ErrorBox error={mut.error} />}
    </main>
  );
}
