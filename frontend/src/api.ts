export type Provider = "deepseek" | "grok_via_sub2api";
export interface Config {
  provider?: Provider;
  connection_revision?: string;
  result_cache_ttl_seconds: number;
  prompt: string;
  threshold: number;
  model: string;
  base_url: string;
  credential_id: string;
  timeout_ms: number;
  max_tokens: number;
  store_input: boolean;
  retention_days: number;
}
export interface Policy {
  id: string;
  name: string;
  alias: string;
  enabled: boolean;
  draft_revision: number;
  active_version: number;
  draft: Config;
  active?: Config;
}
export interface Version {
  version: number;
  config: Config;
  created_at: string;
  author: string;
}
export interface Credential {
  provider: Provider;
  base_url: string;
  id: string;
  name: string;
  masked: string;
  active: boolean;
}
export interface ClientKey {
  id: string;
  name: string;
  prefix: string;
  policy_ids: string[];
  rpm: number;
  active: boolean;
  created_at: string;
}
export interface Metadata {
  schema_version: number;
  policy_id: string;
  policy_version: number;
  confidence: number;
  threshold: number;
  reason: string;
}
export interface AuditResponse {
  provider?: Provider;
  id: string;
  model: string;
  latency_ms: number;
  usage: Usage;
  cost?: CostView;
  cache_hit?: boolean;
  results: {
    flagged: boolean;
    category_scores: Record<string, number>;
    audit: Metadata;
  }[];
}
export interface AuditLog {
  provider?: Provider;
  cost?: CostView;
  cache_hit?: boolean;
  id: string;
  kind: string;
  policy_id: string;
  policy_version: number;
  client_id: string;
  model: string;
  flagged: boolean;
  confidence: number | null;
  threshold: number;
  reason: string;
  error_code: string;
  latency_ms: number;
  usage: AuditResponse["usage"];
  input_stored: boolean;
  input?: string;
  created_at: string;
}
let csrf = "";
export function setCSRF(token: string) {
  csrf = token;
}
export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
): Promise<T> {
  const response = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await response
    .json()
    .catch(() => ({ error: { message: "服务返回了无效响应" } }));
  if (!response.ok)
    throw new APIError(response.status, data.error?.message || "请求失败");
  return data as T;
}

export interface Usage {
  actual_model?: string;
  upstream_request_id?: string;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  prompt_cache_hit_tokens?: number;
  prompt_cache_miss_tokens?: number;
  reasoning_tokens?: number;
  reported: boolean;
}
export interface CostView {
  status: string;
  amount_cny: string | null;
  reserved_cny: string;
  price_id?: number;
  period: string;
  note: string;
}
