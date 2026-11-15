import { useMutation } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { post, type S } from '../api/client';
import { useMe } from '../api/hooks';
import { ErrorBox, Loading } from '../components/ui';

export function Onboarding() {
  const me = useMe();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [persona, setPersona] = useState<S<'OnboardingInput'>['persona']>('individual');
  const nav = useNavigate();
  const mut = useMutation({
    mutationFn: async () => {
      const resp = await post<{ success: boolean; return_to: string }>('/auth/login', {
        username,
        password,
        return_to: '/app',
      });
      if (resp.success) {
        nav({ to: resp.return_to || '/app' });
      }
    },
  });
  if (me.isLoading)
    return (
      <main>
        <Loading />
      </main>
    );
  if (!me.data || me.data.onboarded) {
    nav({ to: '/app' });
    return null;
  }
  return (
    <main className="prose">
      <h1>登录工作区</h1>
      <p>请输入账号和密码登录。演示账号：administrator / administrator</p>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          mut.mutate();
        }}
      >
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
        <button type="submit" disabled={mut.isPending || !username || !password}>
          {mut.isPending ? '登录中…' : '登录'}
        </button>
      </form>
      {mut.error && <ErrorBox error={mut.error} />}
    </main>
  );
}
