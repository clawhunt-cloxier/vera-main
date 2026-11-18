import { useEffect, useId, useState } from 'react';
import { get, type Entity, type ResearchProtocol } from '../api/client';

export function EntitySearch({
  kinds,
  onPick,
  placeholder,
  protocol,
}: {
  kinds?: string[];
  onPick: (e: Entity) => void;
  placeholder?: string;
  protocol?: ResearchProtocol;
}) {
  const [q, setQ] = useState('');
  const [items, setItems] = useState<Entity[]>([]);
  const [open, setOpen] = useState(false);
  const [failed, setFailed] = useState(false);
  const [active, setActive] = useState(0);
  const id = useId();
  useEffect(() => {
    setFailed(false);
    if (q.trim().length === 0) {
      setItems([]);
      return;
    }
    let current = true;
    const t = setTimeout(async () => {
      try {
        const r = await get<{ items: Entity[] }>(
          '/api/v1/entities?q=' + encodeURIComponent(q.trim()),
          { protocol }
        );
        if (!current) return;
        setItems(r.items.filter((e) => !kinds || kinds.includes(e.kind)));
        setActive(0);
        setOpen(true);
      } catch {
        if (!current) return;
        setItems([]);
        setFailed(true);
        setOpen(true);
      }
    }, 200);
    return () => {
      current = false;
      clearTimeout(t);
    };
  }, [q, kinds, protocol]);
  const pick = (e: Entity) => {
    onPick(e);
    setQ(e.name);
    setOpen(false);
  };
  return (
    <div className="combo">
      <input
        role="combobox"
        aria-expanded={open}
        aria-controls={id}
        aria-autocomplete="list"
        aria-label="搜索研究对象"
        placeholder={placeholder ?? '输入公司名称、代码或行业'}
        value={q}
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown') setActive((a) => Math.min(a + 1, items.length - 1));
          if (e.key === 'ArrowUp') setActive((a) => Math.max(a - 1, 0));
          if (e.key === 'Enter' && items[active]) {
            e.preventDefault();
            pick(items[active]);
          }
          if (e.key === 'Escape') setOpen(false);
        }}
      />
      {open && items.length > 0 && (
        <ul id={id} role="listbox" className="options">
          {items.map((e, i) => (
            <li
              key={e.id}
              role="option"
              aria-selected={i === active}
              className={i === active ? 'active' : ''}
              onMouseDown={() => pick(e)}
            >
              <strong>{e.name}</strong>{' '}
              <span className="muted">
                {e.symbol ??
                  ({ industry: '行业', market: '市场', stock: '' } as Record<string, string>)[
                    e.kind
                  ]}
              </span>
            </li>
          ))}
        </ul>
      )}
      {open && q && items.length === 0 && (
        <p className="muted small">
          {failed ? '证券搜索暂时不可用，请稍后重试' : '未找到匹配的对象'}
        </p>
      )}
    </div>
  );
}
