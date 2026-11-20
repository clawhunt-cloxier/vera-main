import type { ResearchProtocol } from '../api/client';

export function ResearchSourceField({
  value,
  onChange,
  snapshot,
}: {
  value: ResearchProtocol;
  onChange: (value: ResearchProtocol) => void;
  snapshot: boolean;
}) {
  return (
    <label>
      研究数据
      <select value={value} onChange={(event) => onChange(event.target.value as ResearchProtocol)}>
        <option value="legacy">TuShare 收盘研究</option>
        <option value="observation.v1">
          日成交简报（Choice 成交数据，已登记股票可补充 TuShare 数据）
        </option>
        {snapshot && <option value="v0.4">Choice 行情快照</option>}
      </select>
    </label>
  );
}
