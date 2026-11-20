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
        <option value="observation.v1">Choice + TuShare 双来源行情研究</option>
        {snapshot && <option value="v0.4">Choice 最近可用行情</option>}
      </select>
    </label>
  );
}
