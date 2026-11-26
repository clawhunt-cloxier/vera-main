import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { ArrowUp, ChevronDown, MessageSquare, Plus, Search, Trash2, X } from 'lucide-react';
import { useEffect, useId, useRef, useState, type RefObject } from 'react';
import {
  actOnTurn,
  conversationHistory,
  conversationResult,
  conversationThread,
  conversationTurn,
  deleteConversation,
  historyKey,
  sendFollowUp,
  startConversation,
  threadKey,
  type ConversationAction,
  type ConversationThread,
  type ConversationTurn,
} from '../api/conversations';
import {
  fmtDataTime,
  fmtTime,
  get,
  newKey,
  skillTitle,
  taskStatusLabel,
  terminal,
  type Entity,
  type Me,
} from '../api/client';
import { useMe } from '../api/hooks';
import { ConversationResult } from '../components/ConversationResult';
import { Empty, ErrorBox, Loading, VeraAvatar } from '../components/ui';
import { WorkspaceFrame } from '../components/WorkspaceFrame';
import './conversations.css';

type ComposerProps = {
  workspace: string;
  thread?: ConversationThread['conversation'];
  reply?: ConversationTurn;
  onClearReply?: () => void;
  disabled?: boolean;
  examples?: string[];
  inputRef?: RefObject<HTMLTextAreaElement | null>;
};

export function ConversationComposer({
  workspace,
  thread,
  reply,
  onClearReply,
  disabled,
  examples = [],
  inputRef,
}: ComposerProps) {
  const [text, setText] = useState('');
  const [key, setKey] = useState(() => newKey());
  const label = useId();
  const form = useRef<HTMLFormElement>(null);
  const localInput = useRef<HTMLTextAreaElement>(null);
  const input = inputRef ?? localInput;
  const nav = useNavigate();
  const cache = useQueryClient();
  const mutation = useMutation({
    mutationFn: (value: string) => {
      const body = { client_message_id: key, text: value };
      return thread
        ? sendFollowUp(thread.id, {
            ...body,
            expected_revision: thread.revision,
            reply_to_turn_id: reply?.id,
          })
        : startConversation(body);
    },
    onSuccess: async (data) => {
      setText('');
      setKey(newKey());
      onClearReply?.();
      void cache.invalidateQueries({ queryKey: historyKey(workspace) });
      if (thread) await cache.invalidateQueries({ queryKey: threadKey(workspace, thread.id) });
      else await nav({ to: '/app/chat/$id', params: { id: data.conversation_id } });
    },
    onError: () => {
      if (thread) void cache.invalidateQueries({ queryKey: threadKey(workspace, thread.id) });
    },
  });
  const update = (value: string) => {
    setText(value);
    setKey(newKey());
    if (mutation.isError) mutation.reset();
  };
  const message = text.trim();
  const canSend = !disabled && !mutation.isPending && message.length > 0;

  return (
    <section className="chat-compose-section" aria-label={thread ? '继续对话研究' : '发起对话研究'}>
      <form
        ref={form}
        className="chat-compose"
        onSubmit={(event) => {
          event.preventDefault();
          if (canSend) mutation.mutate(message);
        }}
      >
        {reply && (
          <div className="chat-reply-context">
            <span>回复 · {reply.text}</span>
            <button
              type="button"
              className="chat-icon"
              aria-label="取消引用"
              onClick={onClearReply}
            >
              <X size={15} />
            </button>
          </div>
        )}
        <label htmlFor={label} className="sr-only">
          {thread ? '继续提问' : '输入研究问题'}
        </label>
        <textarea
          id={label}
          ref={input}
          value={text}
          rows={2}
          maxLength={500}
          disabled={mutation.isPending}
          placeholder={thread ? '继续提问' : '聊聊金融概念，或提一个研究问题'}
          onChange={(event) => update(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
              event.preventDefault();
              form.current?.requestSubmit();
            }
          }}
        />
        <div className="chat-compose-foot">
          <div className="chat-examples">
            {examples.map((example) => (
              <button
                type="button"
                key={example}
                disabled={mutation.isPending}
                onClick={() => {
                  update(example);
                  input.current?.focus();
                }}
              >
                {example}
              </button>
            ))}
          </div>
          <button
            type="submit"
            className="chat-send"
            disabled={!canSend}
            aria-label="发送消息"
            title="发送消息"
          >
            <ArrowUp size={18} aria-hidden="true" />
          </button>
        </div>
      </form>
      {mutation.error && <ErrorBox error={mutation.error} />}
    </section>
  );
}

export function ConversationHistory({
  workspace,
  selected,
  preview = false,
}: {
  workspace: string;
  selected?: string;
  preview?: boolean;
}) {
  const history = useInfiniteQuery({
    queryKey: historyKey(workspace),
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) => conversationHistory(pageParam, signal),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
    staleTime: 5000,
    refetchInterval: (query) =>
      query.state.data?.pages.find((page) => page.pollMs)?.pollMs ?? false,
  });
  const rows = history.data?.pages.flatMap((page) => page.items) ?? [];
  return (
    <section className="chat-history" aria-label="对话历史">
      {!preview && (
        <header>
          <strong>
            <MessageSquare size={16} /> 对话历史
          </strong>
          <Link to="/app/chat" className="chat-icon" aria-label="新建会话" title="新建会话">
            <Plus size={18} />
          </Link>
        </header>
      )}
      {history.isLoading && <Loading label="正在读取对话…" />}
      {history.isError && <ErrorBox error={history.error} />}
      {!history.isLoading && !history.isError && rows.length === 0 && <Empty>还没有研究对话</Empty>}
      {rows.length > 0 && (
        <ul>
          {(preview ? rows.slice(0, 5) : rows).map((item) => (
            <li key={item.id}>
              <Link
                to="/app/chat/$id"
                params={{ id: item.id }}
                aria-current={selected === item.id ? 'page' : undefined}
              >
                <span>{item.title}</span>
                <small>{fmtTime(item.updated_at)}</small>
              </Link>
            </li>
          ))}
        </ul>
      )}
      {preview ? (
        <Link className="chat-action" to="/app/chat">
          查看对话历史
        </Link>
      ) : (
        history.hasNextPage && (
          <button
            type="button"
            className="chat-action"
            onClick={() => void history.fetchNextPage()}
            disabled={history.isFetchingNextPage}
          >
            <ChevronDown size={15} /> {history.isFetchingNextPage ? '正在读取…' : '更多会话'}
          </button>
        )
      )}
    </section>
  );
}

export function conversationExamples(me: Me) {
  const shortName = me.synthetic ? '演示制造' : '中国移动';
  return [
    `看看${shortName}近 20 个交易日的日成交`,
    ...(me.conversation_topics?.includes('financing_balance')
      ? [`${shortName}的融资余额怎么变`]
      : []),
  ];
}

export function ConversationPage({ id }: { id?: string }) {
  const me = useMe();
  const workspace = me.data?.workspace_id;
  return (
    <WorkspaceFrame>
      {me.isLoading && <Loading label="正在打开对话…" />}
      {workspace && me.data && (
        <div className="chat-workspace">
          <aside className="chat-sidebar" data-active={id ? true : undefined}>
            <ConversationHistory workspace={workspace} selected={id} />
          </aside>
          {id && (
            <details className="chat-mobile-history" key={id}>
              <summary>对话历史</summary>
              <ConversationHistory workspace={workspace} selected={id} />
            </details>
          )}
          {id ? (
            <ConversationThreadView key={id} id={id} workspace={workspace} />
          ) : (
            <section className="chat-new">
              <header className="desk-title">
                <h1>对话研究</h1>
              </header>
              <ConversationComposer
                workspace={workspace}
                examples={conversationExamples(me.data)}
              />
            </section>
          )}
        </div>
      )}
    </WorkspaceFrame>
  );
}

function ConversationThreadView({ id, workspace }: { id: string; workspace: string }) {
  const key = threadKey(workspace, id);
  const thread = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => conversationThread(id, null, signal),
    refetchInterval: (query) => query.state.data?.pollMs ?? false,
    retry: false,
  });
  const cache = useQueryClient();
  const nav = useNavigate();
  const [reply, setReply] = useState<ConversationTurn>();
  const [deleting, setDeleting] = useState(false);
  const [older, setOlder] = useState<ConversationTurn[]>([]);
  const next = useRef<number | null | undefined>(undefined);
  const newest = thread.data?.turns.at(-1);
  const detail = useQuery({
    queryKey: [...key, 'detail', newest?.id, newest?.revision],
    queryFn: ({ signal }) => conversationTurn(id, newest!.id, signal),
    enabled: newest?.state === 'clarify' || newest?.state === 'confirm',
    retry: false,
  });
  const activeTurn = detail.data ?? newest;
  const taskStatus = activeTurn?.task_status;
  const busy =
    activeTurn?.state === 'pending' || Boolean(taskStatus && !terminal.includes(taskStatus));
  const input = useRef<HTMLTextAreaElement>(null);
  const end = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (newest?.id) end.current?.scrollIntoView({ block: 'nearest' });
  }, [newest?.id]);
  const more = useMutation({
    mutationFn: (before: number) => conversationThread(id, before, new AbortController().signal),
    onSuccess: (page) => {
      next.current = page.next_before;
      setOlder((items) => [...page.turns, ...items]);
    },
  });
  const remove = useMutation({
    mutationFn: () => deleteConversation(id),
    onSuccess: async () => {
      await cache.cancelQueries({ queryKey: key });
      cache.removeQueries({ queryKey: key });
      void cache.invalidateQueries({ queryKey: historyKey(workspace) });
      await nav({ to: '/app/chat' });
    },
  });
  if (thread.isLoading) return <Loading label="正在读取研究对话…" />;
  if (thread.isError || !thread.data)
    return <ErrorBox error={thread.error ?? new Error('会话不存在')} />;
  const olderCursor = next.current === undefined ? thread.data.next_before : next.current;
  const turns = [
    ...older.filter((turn) => !thread.data.turns.some((item) => item.id === turn.id)),
    ...thread.data.turns,
  ];
  const onReply = (turn: ConversationTurn) => {
    setReply(turn);
    input.current?.focus();
  };

  return (
    <section className="chat-thread" aria-label="研究对话">
      <header className="chat-thread-heading">
        <h1>{thread.data.conversation.title}</h1>
        {!deleting && (
          <button
            type="button"
            className="chat-icon"
            title="删除会话"
            aria-label="删除会话"
            onClick={() => setDeleting(true)}
          >
            <Trash2 size={17} />
          </button>
        )}
      </header>
      <div className="chat-scroll" tabIndex={0} aria-label="消息记录">
        {deleting && (
          <div className="chat-delete" role="group" aria-label="确认删除会话">
            <p>删除对话及其短研究结果？</p>
            <button type="button" disabled={remove.isPending} onClick={() => setDeleting(false)}>
              取消
            </button>
            <button type="button" disabled={remove.isPending} onClick={() => remove.mutate()}>
              删除
            </button>
          </div>
        )}
        {remove.error && <ErrorBox error={remove.error} />}
        {olderCursor != null && (
          <button
            type="button"
            className="chat-action"
            disabled={more.isPending}
            onClick={() => more.mutate(olderCursor)}
          >
            读取更早的消息
          </button>
        )}
        {more.error && <ErrorBox error={more.error} />}
        <ol className="chat-turns" aria-label="对话消息">
          {turns.map((turn) => (
            <li key={turn.id}>
              <p className="chat-question">{turn.text}</p>
              <div className="chat-answer">
                <VeraAvatar size={28} />
                <div className="chat-answer-content">
                  <TurnBody
                    key={turn.id}
                    id={id}
                    workspace={workspace}
                    turn={turn.id === newest?.id && activeTurn ? activeTurn : turn}
                    current={turn.id === newest?.id}
                    detailError={turn.id === newest?.id ? detail.error : null}
                    onReply={() => onReply(turn)}
                  />
                </div>
              </div>
            </li>
          ))}
        </ol>
        <div ref={end} />
      </div>
      <div className="chat-thread-composer">
        <ConversationComposer
          workspace={workspace}
          thread={thread.data.conversation}
          reply={reply}
          inputRef={input}
          onClearReply={() => setReply(undefined)}
          disabled={busy}
        />
      </div>
    </section>
  );
}

const reasonText: Record<string, string> = {
  REQUIRED_DATA_MISSING: '此次研究缺少可核验的必需数据。',
  REQUIRED_SECTION_UNSUPPORTED: '现有证据不足，无法交付结论。',
  DEADLINE_EXCEEDED: '研究已超时。可以缩小范围后重试。',
  BUDGET_EXHAUSTED: '本轮研究未能完成。可以缩小范围后重试。',
  DATA_SOURCE_FORBIDDEN: '本轮未能取得所需的数据用途授权。',
  USER_CANCELLED: '已取消本轮研究。',
};

function TurnBody({
  id,
  workspace,
  turn,
  current,
  detailError,
  onReply,
}: {
  id: string;
  workspace: string;
  turn: ConversationTurn;
  current: boolean;
  detailError: unknown;
  onReply: () => void;
}) {
  const key = threadKey(workspace, id);
  const cache = useQueryClient();
  const [open, setOpen] = useState(false);
  const result = useQuery({
    queryKey: [...key, 'result', turn.id],
    queryFn: ({ signal }) => conversationResult(id, turn.id, signal),
    enabled: (current || open) && turn.result_ready,
    refetchInterval: 30_000,
    retry: false,
  });
  const action = useMutation({
    mutationFn: (payload: ConversationAction) => actOnTurn(id, turn.id, payload),
    onSettled: async () => {
      await cache.invalidateQueries({ queryKey: key });
      void cache.invalidateQueries({ queryKey: historyKey(workspace) });
      void cache.invalidateQueries({ queryKey: ['me'] });
      void cache.invalidateQueries({ queryKey: ['reports'] });
      void cache.invalidateQueries({ queryKey: ['tasks'] });
    },
  });
  const expected_revision = turn.revision;
  const status = turn.task_status;
  const working = status != null && !terminal.includes(status);
  const plan = turn.state === 'confirm' && current ? turn.plan : undefined;

  return (
    <>
      {current && detailError != null && <ErrorBox error={detailError} />}
      {action.error && <ErrorBox error={action.error} />}
      {turn.state === 'pending' && <Loading label={turn.message ?? '正在理解你的问题…'} />}
      {turn.state === 'unavailable' && <p role="status">{turn.message}</p>}
      {turn.notice && <p className="chat-reply">{turn.notice}</p>}
      {turn.state === 'confirm' && !plan && <p>{turn.message ?? '该轮计划未执行。'}</p>}
      {plan && (
        <section className="chat-plan" aria-label="待确认的研究计划">
          <h3>
            {plan.subject.name} ·{' '}
            {turn.scope?.topic === 'daily_trading'
              ? '日成交'
              : turn.scope?.topic === 'financing_balance'
                ? '融资余额'
                : skillTitle[plan.skill_id]}
          </h3>
          {Boolean(turn.scope?.window) && <p>窗口为 {turn.scope?.window} 个交易日</p>}
          <p>
            {plan.snapshot_mode === 'collect_latest'
              ? '确认后采集并冻结数据'
              : '数据截至 ' + fmtDataTime(plan.as_of)}
          </p>
          {plan.sections
            .filter((section) => section.reason && section.availability !== 'available')
            .map((section) => (
              <p key={section.id}>
                {section.title} · {section.reason}
              </p>
            ))}
          {plan.executable && (
            <button
              type="button"
              className="primary"
              disabled={action.isPending}
              onClick={() =>
                action.mutate({
                  action: 'confirm',
                  plan_id: plan.id,
                  plan_hash: plan.plan_hash,
                  expected_revision,
                })
              }
            >
              确认研究
            </button>
          )}
        </section>
      )}
      {turn.state === 'answer' && <p className="chat-reply">{turn.message}</p>}
      {turn.web_sources && turn.web_sources.length > 0 && (
        <section className="chat-web-sources" aria-label="网页来源">
          <h3>参考来源</h3>
          <ul>
            {turn.web_sources.map((source) => (
              <li key={source.url}>
                <a href={source.url} target="_blank" rel="noopener noreferrer">
                  {source.title}
                </a>
              </li>
            ))}
          </ul>
        </section>
      )}
      {turn.state === 'clarify' && (
        <>
          <p>{turn.message}</p>
          {current && turn.needs_subject && (
            <SubjectPicker
              candidates={turn.candidates}
              choosing={action.isPending}
              onChoose={(subject) =>
                action.mutate({ action: 'choose', subject_id: subject, expected_revision })
              }
            />
          )}
        </>
      )}
      {working && <Loading label={taskStatusLabel(status)} />}
      {status && !working && status !== 'COMPLETED' && (
        <p role="status">
          {(turn.reason_code && reasonText[turn.reason_code]) || taskStatusLabel(status)}
        </p>
      )}
      {((current && (turn.state === 'pending' || turn.state === 'confirm')) || working) &&
        status !== 'CANCEL_REQUESTED' && (
          <button
            type="button"
            className="chat-action"
            disabled={action.isPending}
            onClick={() =>
              action.mutate({
                action: 'cancel',
                task_revision: turn.task_revision,
                expected_revision,
              })
            }
          >
            取消本轮
          </button>
        )}
      {turn.result_ready && !(current || open) && (
        <button type="button" className="chat-action" onClick={() => setOpen(true)}>
          查看该轮结果
        </button>
      )}
      {turn.result_ready && (current || open) && (
        <>
          {result.isLoading && <Loading label="正在读取研究结果…" />}
          {result.isError && <ErrorBox error={result.error} />}
          {!result.isError && result.data && (
            <ConversationResult result={result.data} turn={turn} onReply={onReply} />
          )}
        </>
      )}
    </>
  );
}

function SubjectPicker({
  candidates,
  choosing,
  onChoose,
}: {
  candidates?: Entity[];
  choosing: boolean;
  onChoose: (id: string) => void;
}) {
  const me = useMe();
  const [text, setText] = useState('');
  const query = useMutation({
    mutationFn: (value: string) =>
      get<{ items: Entity[] }>('/api/v1/entities?kind=stock&q=' + encodeURIComponent(value), {
        protocol: me.data?.research_protocol,
      }),
  });
  const rows = candidates?.length ? candidates : (query.data?.items ?? []);
  return (
    <div className="chat-subject">
      {!candidates?.length && (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            const value = text.trim();
            if (value) query.mutate(value);
          }}
        >
          <input
            aria-label="证券名称或代码"
            placeholder="证券名称或代码"
            value={text}
            maxLength={50}
            onChange={(event) => setText(event.target.value)}
          />
          <button
            type="submit"
            className="chat-icon"
            title="搜索证券"
            aria-label="搜索证券"
            disabled={query.isPending || !text.trim()}
          >
            <Search size={16} />
          </button>
        </form>
      )}
      {query.error && <ErrorBox error={query.error} />}
      {query.data?.items.length === 0 && <p className="muted">未找到已登记证券。</p>}
      {rows.length > 0 && (
        <ul>
          {rows.map((entity) => (
            <li key={entity.id}>
              <button
                type="button"
                className="chat-action"
                disabled={choosing}
                onClick={() => onChoose(entity.id)}
              >
                {entity.name} {entity.symbol && <small>{entity.symbol}</small>}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
