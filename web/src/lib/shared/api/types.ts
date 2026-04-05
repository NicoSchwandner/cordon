export interface ProxyResult {
  allowed: boolean;
  tier: number;
  decision: string;
  error?: string;
  upstream_status?: number;
  upstream_headers?: Record<string, string>;
  upstream_body?: string;
}

export interface SQLProxyRequest {
  query: string;
  target?: string;
  caller: string;
}

export interface HTTPProxyRequest {
  method: string;
  url: string;
  host: string;
  caller: string;
}

export interface AuditEntry {
  id: string;
  tenant_id: string;
  workspace_id: string;
  timestamp: string;
  tier: number;
  tier_name: string;
  operation: string;
  target: string;
  caller: string;
  decision: string;
  duration_ms: number;
  detail: string;
}

export interface AuditFilter {
  workspace_id?: string;
  tier_min?: number;
  decision?: string;
  since?: string;
  until?: string;
  limit?: number;
  offset?: number;
}

export interface ApprovalRequest {
  id: string;
  tenant_id: string;
  workspace_id: string;
  tier: number;
  operation: string;
  target: string;
  caller: string;
  requested_at: string;
  expires_at: string;
  status: string;
}

export interface ApprovalDecisionRequest {
  scope: "one_time" | "session" | "pattern";
  pattern?: string;
  duration?: string;
}

export interface RepoConfig {
  url: string;
  branch?: string;
  base_branch?: string;
  devcontainer_path?: string;
  primary?: boolean;
  service_container?: boolean;
}

export interface InvestigationState {
  catalog_org: string;
  shallow_repos: string[];
  activated_repos: string[];
}

export interface WorkspaceResponse {
  id: string;
  tenant_id: string;
  name: string;
  status: string;
  mode?: string;
  spawned_from?: string;
  repos: RepoConfig[];
  investigation?: InvestigationState;
  created_at: string;
  expires_at: string;
  idle_timeout?: string;
  max_lifetime?: string;
}

export interface CreateWorkspaceRequest {
  name: string;
  mode?: "dev" | "investigation";
  org?: string;
  repos?: RepoConfig[];
  devcontainer_path?: string;
  cpu?: number;
  memory_mb?: number;
  max_lifetime?: string;
}

export interface OrgRepo {
  name: string;
  full_name: string;
  default_branch: string;
  description: string;
  language: string;
  archived: boolean;
  private: boolean;
  updated_at: string;
  html_url: string;
  clone_url: string;
}

export interface GitBranch {
  name: string;
}

export interface SecretRef {
  name: string;
  placeholder: string;
  masked_value: string;
}

export interface SetSecretRequest {
  name: string;
  placeholder?: string;
  value: string;
}

export interface ProgressEvent {
  step: string;
  message: string;
  done: boolean;
  error?: string;
  estimated_secs?: number;
}

export interface ProblemDetails {
  type: string;
  title: string;
  status: number;
  detail: string;
  instance?: string;
  code: string;
}

export const TIER_NAMES: Record<number, string> = {
  1: "Read",
  2: "Safe Write",
  3: "Destructive",
  4: "Forbidden",
};

export const TIER_COLORS: Record<number, { bg: string; text: string }> = {
  1: { bg: "bg-success-badge-bg", text: "text-success-text" },
  2: { bg: "bg-warning-badge-bg", text: "text-warning-text" },
  3: { bg: "bg-danger-bg", text: "text-danger-text" },
  4: { bg: "bg-danger-solid", text: "text-on-danger" },
};

export const DECISION_COLORS: Record<string, { bg: string; text: string }> = {
  allowed: { bg: "bg-success-badge-bg", text: "text-success-text" },
  denied: { bg: "bg-danger-bg", text: "text-danger-text" },
  blocked: { bg: "bg-danger-solid", text: "text-on-danger" },
  pending_approval: { bg: "bg-warning-badge-bg", text: "text-warning-text" },
};
