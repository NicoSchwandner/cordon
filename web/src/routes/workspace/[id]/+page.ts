import { getWorkspace } from "$lib/shared/api/client";
import type { PageLoad } from "./$types";

export const load: PageLoad = async ({ params }) => {
  const workspace = await getWorkspace(params.id).catch(() => null);
  return { workspace, workspaceId: params.id };
};
