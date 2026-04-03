import type {
  AuditEntry,
  AuditFilter,
  ApprovalDecisionRequest,
  HTTPProxyRequest,
  ProblemDetails,
  ProxyResult,
  SQLProxyRequest,
  SecretRef,
  SetSecretRequest,
  WorkspaceResponse,
  CreateWorkspaceRequest,
} from "./types";

class ApiError extends Error {
  constructor(public problem: ProblemDetails) {
    super(problem.detail);
    this.name = "ApiError";
  }
}

async function fetchJSON<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const opts: RequestInit = {
    method,
    headers: { "Content-Type": "application/json" },
  };
  if (body) opts.body = JSON.stringify(body);

  const resp = await fetch(path, opts);
  const contentType = resp.headers.get("content-type") ?? "";

  if (contentType.includes("application/problem+json")) {
    const problem: ProblemDetails = await resp.json();
    throw new ApiError(problem);
  }
  if (!resp.ok) {
    throw new Error(`HTTP ${resp.status}: ${await resp.text()}`);
  }
  return resp.json();
}

export function proxySQL(req: SQLProxyRequest): Promise<ProxyResult> {
  return fetchJSON("POST", "/api/proxy/sql", req);
}

export function proxyHTTP(req: HTTPProxyRequest): Promise<ProxyResult> {
  return fetchJSON("POST", "/api/proxy/http", req);
}

export function getAuditEntries(
  filter: AuditFilter = {},
): Promise<AuditEntry[]> {
  const params = new URLSearchParams();
  if (filter.workspace_id) params.set("workspace_id", filter.workspace_id);
  if (filter.tier_min) params.set("tier_min", String(filter.tier_min));
  if (filter.decision) params.set("decision", filter.decision);
  if (filter.since) params.set("since", filter.since);
  if (filter.until) params.set("until", filter.until);
  if (filter.limit) params.set("limit", String(filter.limit));
  if (filter.offset) params.set("offset", String(filter.offset));
  const qs = params.toString();
  return fetchJSON("GET", `/api/audit${qs ? "?" + qs : ""}`);
}

export function decideApproval(
  id: string,
  req: ApprovalDecisionRequest,
): Promise<void> {
  return fetchJSON("POST", `/api/approvals/${id}/decide`, req);
}

export function listWorkspaces(): Promise<WorkspaceResponse[]> {
  return fetchJSON("GET", "/api/workspaces");
}

export function createWorkspace(
  req: CreateWorkspaceRequest,
): Promise<WorkspaceResponse> {
  return fetchJSON("POST", "/api/workspaces", req);
}

export function getWorkspace(id: string): Promise<WorkspaceResponse> {
  return fetchJSON("GET", `/api/workspaces/${id}`);
}

export function deleteWorkspace(id: string): Promise<void> {
  return fetchJSON("DELETE", `/api/workspaces/${id}`);
}

export function workspaceAction(
  id: string,
  action: "suspend" | "resume",
): Promise<void> {
  return fetchJSON("POST", `/api/workspaces/${id}/${action}`);
}

export function getHealth(): Promise<{ status: string }> {
  return fetchJSON("GET", "/health");
}

export function listSecrets(): Promise<SecretRef[]> {
  return fetchJSON("GET", "/api/secrets");
}

export function setSecret(req: SetSecretRequest): Promise<SecretRef> {
  return fetchJSON("POST", "/api/secrets", req);
}

export function updateSecret(req: SetSecretRequest): Promise<SecretRef> {
  return fetchJSON("PUT", "/api/secrets", req);
}

export function deleteSecret(name: string): Promise<void> {
  return fetchJSON("DELETE", `/api/secrets/${encodeURIComponent(name)}`);
}

export { ApiError };
