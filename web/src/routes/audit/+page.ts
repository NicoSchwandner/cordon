import { getAuditEntries } from "$lib/shared/api/client";
import type { PageLoad } from "./$types";

export const load: PageLoad = async () => {
  const entries = await getAuditEntries({ limit: 50 }).catch(() => []);
  return { entries };
};
