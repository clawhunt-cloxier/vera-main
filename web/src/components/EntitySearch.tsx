import { useEffect, useId, useState } from 'react';
import { get, type Entity, type ResearchProtocol } from '../api/client';

type Match = { entity: Entity; protocol?: ResearchProtocol };

function exactStock(entity: Entity, query: string) {
  const normalized = query.toUpperCase();
  return (
    entity.name.toUpperCase() === normalized ||
    entity.symbol?.toUpperCase() === normalized ||
    (/^\d{6}$/.test(query) && entity.symbol?.split('.')[0] === query)
  );
}

export function EntitySearch({
  kinds,
  onPick,
  onEdit,
  placeholder,
  protocol,
  searchObservation = false,
}: {
  kinds?: string[];
  onPick: (e: Entity, protocol?: ResearchProtocol) => void;
  onEdit?: () => void;
  placeholder?: string;
  protocol?: ResearchProtocol;
  searchObservation?: boolean;
}) {
  const [q, setQ] = useState('');
  const [editing, setEditing] = useState(false);
  const [items, setItems] = useState<Match[]>([]);
  const [open, setOpen] = useState(false);
  const [failed, setFailed] = useState(false);
  const [active, setActive] = useState(0);
  const id = useId();
  const expanded = open && items.length > 0;
  const kindFilter = kinds?.join(',');
  useEffect(() => {
    const query = q.trim();
    if (!editing || !query) return;
    let current = true;
    const allowedKinds = kindFilter?.split(',');
    const params = new URLSearchParams({ q: query });
    if (allowedKinds?.length === 1) params.set('kind', allowedKinds[0]);
    const search = async (source?: ResearchProtocol): Promise<Match[]> => {
      const r = await get<{ items: Entity[] }>('/api/v1/entities?' + params, { protocol: source });
      return r.items
        .filter((e) => !allowedKinds || allowedKinds.includes(e.kind))
        .map((entity) => ({ entity, protocol: source }));
    };
    const t = setTimeout(async () => {
      let matches: Match[] = [];
      try {
        matches = await search(protocol);
        if (!current) return;
        if (searchObservation && !matches.some(({ entity }) => exactStock(entity, query))) {
          const extra = await search('observation.v1');
          const seen = new Set(matches.map(({ entity }) => entity.id));
          for (const match of extra) {
            if (seen.has(match.entity.id)) continue;
            seen.add(match.entity.id);
            matches.push(match);
          }
          matches.sort(
            (a, b) => Number(exactStock(b.entity, query)) - Number(exactStock(a.entity, query))
          );
        }
      } catch {
        if (!current) return;
        setFailed(true);
      }
      if (!current) return;
      setItems(matches);
      setActive(0);
      setOpen(true);
    }, 200);
    return () => {
      current = false;
      clearTimeout(t);
    };
  }, [q, editing, kindFilter, protocol, searchObservation]);
  const pick = ({ entity, protocol: selected }: Match) => {
    onPick(entity, selected);
    setEditing(false);
    setQ(entity.name);
    setOpen(false);
  };
  return (
    <div className="combo">
      <input
        role="combobox"
        aria-expanded={expanded}
        aria-controls={id}
        aria-activedescendant={expanded && items[active] ? id + '-' + active : undefined}
        aria-autocomplete="list"
        aria-label="搜索研究对象"
        placeholder={placeholder ?? '输入公司名称、代码或行业'}
        value={q}
        onChange={(e) => {
          onEdit?.();
          setEditing(true);
          setQ(e.target.value);
          setOpen(false);
          setFailed(false);
        }}
        onBlur={() => {
          setOpen(false);
          setEditing(false);
        }}
        onKeyDown={(e) => {
          if (expanded && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
            e.preventDefault();
            const step = e.key === 'ArrowDown' ? 1 : -1;
            setActive((a) => Math.min(Math.max(0, a + step), items.length - 1));
          }
          if (expanded && e.key === 'Enter' && items[active]) {
            e.preventDefault();
            pick(items[active]);
          }
          if (e.key === 'Escape') {
            setOpen(false);
            setEditing(false);
          }
        }}
      />
      {items.length > 0 && (
        <ul id={id} role="listbox" className="options" inert={!expanded}>
          {items.map((match, i) => (
            <li
              key={match.entity.id}
              id={id + '-' + i}
              role="option"
              aria-selected={i === active}
              className={i === active ? 'active' : ''}
              onMouseDown={() => pick(match)}
            >
              <strong>{match.entity.name}</strong>{' '}
              <span className="muted">
                {match.entity.symbol ??
                  ({ industry: '行业', market: '市场', stock: '' } as Record<string, string>)[
                    match.entity.kind
                  ]}
              </span>
            </li>
          ))}
        </ul>
      )}
      {open && q && (failed || items.length === 0) && (
        <p className="muted small">
          {failed
            ? items.length
              ? '部分证券搜索暂时不可用，请稍后重试'
              : '证券搜索暂时不可用，请稍后重试'
            : '未找到匹配的对象'}
        </p>
      )}
    </div>
  );
}
