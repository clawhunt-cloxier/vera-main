import { useMutation } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { post, type S } from '../api/client';
import { useMe } from '../api/hooks';
import { ErrorBox, Loading } from '../components/ui';

export function Onboarding() {
  const me = useMe();
  const [code, setCode] = useState('');
  const [persona, setPersona] = useState<S<'OnboardingInput'>['persona']>('individual');
  const nav = useNavigate();
  const mut = useMutation({
    mutationFn: async () => {
      await post<S<'Me'>>('/api/v1/onboarding', { invite_code: code, persona });
      nav({ to: '/app' });
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
      <h1>开通工作区</h1>
      <p>首次登录需要输入邀请码来开通私人工作区。试用采用邀请制，如尚未收到邀请码，请联系我们。</p>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          mut.mutate();
        }}
      >
        <label>
          邀请码
          <input
            value={code}
            onChange={(e) => setCode(e.target.value.toUpperCase().replace(/[^A-Z0-9-]/g, ''))}
            placeholder="VERA-XXXX-XXXX-XXXX-XXXX"
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
        <button type="submit" disabled={mut.isPending || code.length < 10}>
          {mut.isPending ? '开通中…' : '开通'}
        </button>
      </form>
      {mut.error && <ErrorBox error={mut.error} />}
    </main>
  );
}
