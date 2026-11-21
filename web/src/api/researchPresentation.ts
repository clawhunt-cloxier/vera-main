import { ApiError } from './client';

// Apply only to the research workflow. Report text, references and evidence
// keep their original provider attribution.
export function researchText(text: string): string {
  const known: Record<string, string> = {
    '仅接入 Choice 时只支持个股行情分析': '当前仅支持个股行情分析',
    '采用计划时已入库的最近一条 TuShare 收盘价，两个来源的交易日期分别标注':
      '采用制定计划时最近可用的收盘价，报告会列明相应交易日期',
  };
  return (
    known[text] ??
    text.replace(/\s*(?:东方财富\s*)?(?:Choice|TuShare(?: Pro)?|EmQuant)\s*/gi, '').trim()
  );
}

const serviceMessages: Record<string, string> = {
  PREPARATION_UNAVAILABLE: '暂时无法准备研究资料，请稍后重试。',
  PREPARATION_QUOTA: '当前研究请求较多，请稍后重试。',
  SOURCE_UNAVAILABLE: '所需研究资料暂时无法读取，请稍后重试。',
  MIXED_SOURCE_UNAVAILABLE: '所需研究资料暂时无法读取，请稍后重试。',
  REPRESENTATION_UNAVAILABLE: '请刷新页面后重试。',
  PLAN_VERSION_MISMATCH: '当前计划的版本无法确认，请返回工作台重新发起研究。',
};

export function researchError(error: unknown): Error {
  if (error instanceof ApiError) {
    return new ApiError(
      error.status,
      error.code,
      serviceMessages[error.code] ?? researchText(error.message),
      error.requestId,
      error.retryable,
      error.details,
      error.retryAfter
    );
  }
  if (error instanceof TypeError) return new Error('无法连接到研究服务，请检查网络后重试。');
  return new Error(researchText(error instanceof Error ? error.message : String(error)));
}

// The preparation endpoint currently classifies failures in a message rather
// than a reason code. Display the useful category without upstream diagnostics.
export function preparationFailure(message?: string): string {
  if (message?.includes('未提供') || message?.includes('覆盖范围')) {
    return '这只股票缺少本次研究所需的数据，可以稍后重试或选择其他研究对象。';
  }
  if (message && /授权|权限/.test(message)) {
    return '当前缺少使用这项研究数据的权限，请联系管理员。';
  }
  if (message && /限频|频率限制/.test(message)) {
    return '数据请求过于频繁，请稍后重新发起研究。';
  }
  return '未能准备好研究资料，请稍后重试。';
}
