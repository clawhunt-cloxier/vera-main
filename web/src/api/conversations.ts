import { api, get, post, type S } from './client';

export type ConversationAccepted = S<'ConversationAccepted'>;
export type ConversationAction = S<'ConversationAction'>;
export type ConversationMessage = S<'ConversationMessage'>;
export type ConversationFollowUp = S<'ConversationFollowUp'>;
export type ConversationHistory = S<'ConversationHistory'>;
export type ConversationThread = S<'ConversationThread'>;
export type ConversationTurn = S<'ConversationTurn'>;
export type ConversationResult = S<'ConversationResult'>;

const base = '/api/v1/conversations';
const json = { protocol: 'legacy' } as const;

export const threadPath = (id: string) => base + '/' + id;
const turnPath = (id: string, turn: string) => threadPath(id) + '/turns/' + turn;

export const historyKey = (workspace: string) => ['conversation-history', workspace];
export const threadKey = (workspace: string, id: string) => ['conversation', workspace, id];

export const conversationHistory = (cursor: string | null, signal: AbortSignal) =>
  get<ConversationHistory>(base + (cursor ? '?cursor=' + encodeURIComponent(cursor) : ''), {
    ...json,
    signal,
  });

export async function conversationThread(
  id: string,
  before: number | null,
  signal: AbortSignal
): Promise<ConversationThread & { pollMs: number | false }> {
  let pollMs: number | false = false;
  const thread = await get<ConversationThread>(
    threadPath(id) + (before ? '?before=' + before : ''),
    {
      ...json,
      signal,
      responseHeaders: (headers) => {
        const seconds = Number(headers.get('Retry-After'));
        if (seconds > 0 && Number.isFinite(seconds)) {
          pollMs = Math.max(1000, Math.min(seconds * 1000, 15_000));
        }
      },
    }
  );
  return { ...thread, pollMs };
}

export const conversationTurn = (id: string, turn: string, signal: AbortSignal) =>
  get<ConversationTurn>(turnPath(id, turn), { ...json, signal });

export const conversationResult = (id: string, turn: string, signal: AbortSignal) =>
  get<ConversationResult>(turnPath(id, turn) + '/result', { ...json, signal });

export const startConversation = (body: ConversationMessage) =>
  post<ConversationAccepted>(base, { ...body, automatic: true }, json);

export const sendFollowUp = (id: string, body: ConversationFollowUp) =>
  post<ConversationAccepted>(threadPath(id) + '/turns', { ...body, automatic: true }, json);

export const actOnTurn = (id: string, turn: string, action: ConversationAction) =>
  post<void>(turnPath(id, turn) + '/actions', action, json);

export const deleteConversation = (id: string) =>
  api<void>('DELETE', threadPath(id), undefined, json);
