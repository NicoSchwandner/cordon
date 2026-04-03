import { getAuditEntries, listWorkspaces } from "$lib/shared/api/client";
import type { AuditEntry, WorkspaceResponse } from "$lib/shared/api/types";
import type { PageLoad } from "./$types";

export const load: PageLoad = async () => {
  const [workspaces, recentAudit] = await Promise.allSettled([
    listWorkspaces(),
    getAuditEntries({ limit: 10 }),
  ]);

  return {
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
