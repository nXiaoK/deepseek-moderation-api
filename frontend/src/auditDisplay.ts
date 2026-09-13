import type { AuditLog } from "./api";

const errors: Record<string, string> = {
  invalid_api_key: "调用密钥无效或已撤销",
  rate_limited: "调用方请求限流",
  body_too_large: "请求体超过大小上限",
  invalid_json: "请求 JSON 或字段不符合接口要求",
  invalid_input: "输入格式不受支持，仅支持文本审核",
  unsupported_input: "当前策略只支持文本审核",
  empty_input: "审核文本不能为空",
  input_too_large: "审核文本超过长度上限",
  policy_forbidden: "调用密钥无权使用此策略",
  record_unavailable: "审核记录暂时无法保存",
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
export function auditStage(stage: string): string {
  return (
    (
      {
        authentication: "密钥验证",
        rate_limit: "请求限流",
        request_validation: "请求格式校验",
        policy_check: "策略检查",
        input_validation: "输入校验",
        audit: "模型审核",
        recording: "记录保存",
        completed: "审核完成",
      } as Record<string, string>
    )[stage] || stage
  );
}
export function auditResult(log: AuditLog): string {
  if (!log.error_code) return log.flagged ? "命中" : "未命中";
  return log.request &&
    log.request.stage !== "audit" &&
    log.request.http_status < 500
    ? "请求被拒绝"
    : "审核失败";
}
export function auditInput(log: AuditLog): string {
  const request = log.request;
  if (!request?.input_type) return "未读取或无法解析输入";
  const label =
    (
      {
        text: "文本",
        content_blocks: "内容块",
        image: "含图片",
        invalid: "无效输入",
        unsupported: "不支持的输入类型",
      } as Record<string, string>
    )[request.input_type] || request.input_type;
  const scope = request.text_only_fallback ? " · 图片已跳过，仅审核文本" : "";
  return `${label} · ${request.text_chars} 字 · ${request.image_count} 张图片${scope}`;
}
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
