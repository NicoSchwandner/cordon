import { getWorkspace, listWorkspaces } from "$lib/shared/api/client";
import type { PageLoad } from "./$types";

export const load: PageLoad = async ({ params }) => {
  const workspace = await getWorkspace(params.id).catch(() => null);
  // For investigation workspaces, load spawned children
  let spawnedWorkspaces: Awaited<ReturnType<typeof listWorkspaces>> = [];
  if (workspace?.mode === "investigation") {
    const all = await listWorkspaces().catch(() => []);
    spawnedWorkspaces = all.filter((ws) => ws.spawned_from === params.id);
  }
  return { workspace, workspaceId: params.id, spawnedWorkspaces };
};
