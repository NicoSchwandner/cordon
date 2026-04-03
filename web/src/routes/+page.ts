import {
  getHealth,
  getAuditEntries,
  listWorkspaces,
} from "$lib/shared/api/client";
import type { AuditEntry, WorkspaceResponse } from "$lib/shared/api/types";
import type { PageLoad } from "./$types";

export const load: PageLoad = async () => {
  const [health, workspaces, recentAudit] = await Promise.allSettled([
    getHealth(),
    listWorkspaces(),
    getAuditEntries({ limit: 10 }),
  ]);

  return {
    healthy: health.status === "fulfilled",
    workspaces:
      workspaces.status === "fulfilled"
        ? (workspaces.value as WorkspaceResponse[])
        : [],
    recentAudit:
      recentAudit.status === "fulfilled"
        ? (recentAudit.value as AuditEntry[])
        : [],
  };
};
