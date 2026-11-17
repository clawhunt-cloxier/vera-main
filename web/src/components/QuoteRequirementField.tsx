import { quoteRequirementLabel, type QuoteRequirement } from '../api/client';

export function QuoteRequirementField({
  value,
  onChange,
}: {
  value: QuoteRequirement | '';
  onChange: (value: QuoteRequirement) => void;
}) {
  return (
    <fieldset className="sections">
      <legend>行情要求（必选）</legend>
      <label>
        <input
          type="radio"
          name="quote_requirement"
          value="realtime_trade"
          checked={value === 'realtime_trade'}
          onChange={() => onChange('realtime_trade')}
          required
        />
        {quoteRequirementLabel.realtime_trade}
      </label>
      <p className="muted">
        仅接受经过核实的交易中实时成交行情。休市、停牌、延迟或无法核实时，任务将弃权。
      </p>
      <label>
        <input
          type="radio"
          name="quote_requirement"
          value="latest_available"
          checked={value === 'latest_available'}
          onChange={() => onChange('latest_available')}
          required
        />
        {quoteRequirementLabel.latest_available}
      </label>
      <p className="muted">接受符合数据策略的最近行情，报告会标明实际时点、延迟性质和交易状态。</p>
    </fieldset>
  );
}
