import type { AuditLog } from "./api";

const errors: Record<string, string> = {
  invalid_model_response: "模型返回不符合审核格式要求",
  upstream_timeout: "模型调用超时",
  audit_timeout: "审核已取消或总调用时限耗尽",
  upstream_unavailable: "上游模型服务暂时不可用",
  upstream_rate_limited: "上游模型请求限流",
  upstream_auth_failed: "上游密钥或权限无效",
  upstream_config_invalid: "上游模型名称或参数不受支持",
  no_available_channel: "没有可用的模型通道",
  capacity_exceeded: "审核并发已满",
  credential_unavailable: "模型连接密钥不可用",
  pricing_unavailable: "预算设置下没有价格可估算的模型",
  budget_exceeded: "剩余预算不足",
  budget_pending: "存在待核对费用",
  cost_record_unavailable: "费用结算或汇总失败",
  cache_unavailable: "结果缓存暂时不可用",
  policy_unavailable: "审核策略已停用",
  internal_error: "服务内部错误",
};
export function auditError(code: string, message?: string): string {
  return message || errors[code] || code;
}
export function formatModelOutput(output: string): string {
  try {
    return JSON.stringify(JSON.parse(output), null, 2);
  } catch {
    return output;
  }
}
export function lastModelOutput(log: AuditLog): string {
  // Older failed requests only stored output on individual attempts.
  return (
    log.model_output ||
    log.attempts?.[log.attempts.length - 1]?.model_output ||
    ""
  );
}
