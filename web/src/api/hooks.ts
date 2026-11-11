import { useQuery } from '@tanstack/react-query';
import { get, setCsrf, type Me } from './client';

export function useMe(noRedirect = false) {
  return useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      const me = await get<Me>('/api/v1/me', { noRedirect });
      setCsrf(me.csrf_token);
      return me;
    },
    retry: false,
    staleTime: 30_000,
  });
}
