import { api, qs, type CatalogSource, type ModrinthVersion } from "./api";
import { useMeta } from "./queries";

/**
 * The catalogs content is searched in: Modrinth, and CurseForge where the
 * panel has a key for it. The panel answers for both in Modrinth's shape.
 */
export const sourceNames: Record<CatalogSource, string> = { modrinth: "Modrinth", curseforge: "CurseForge" };

/**
 * The catalogs offered, Modrinth first. CurseForge is offered without a key
 * too, to say how it is turned on.
 */
export const sources: CatalogSource[] = ["modrinth", "curseforge"];

/**
 * Whether a catalog can be searched, and what is in it looked up, here:
 * CurseForge only with a key. Undefined until that is known.
 */
export function useSearchable(): (source: CatalogSource) => boolean | undefined {
  const meta = useMeta().data;
  return (source) => (source === "modrinth" ? true : meta && !!meta.curseforge);
}

/** Whether a source is a catalog, rather than a URL or an upload. */
export const isCatalog = (source: string | undefined): source is CatalogSource => source === "modrinth" || source === "curseforge";

/** What a project's versions are kept under, so that one list is shared by every page that asks. */
export const versionsKey = (source: CatalogSource, project: string, filters: { loader?: string; gameVersion?: string }) => [`${source}-versions`, project, filters] as const;

/** A project's versions that fit, newest first. */
export const fetchVersions = (source: CatalogSource, project: string, filters: { loader?: string; gameVersion?: string }) =>
  api.get<ModrinthVersion[]>(`/catalog/${source}/projects/${encodeURIComponent(project)}/versions?${qs(filters)}`);

/**
 * A project's page: on Modrinth by its slug; on CurseForge through the
 * panel, which knows where its page is from its ID.
 */
export const projectPage = (source: CatalogSource, project: string, kind = "project") =>
  source === "curseforge" ? `/api/v1/catalog/curseforge/projects/${encodeURIComponent(project)}/page` : `https://modrinth.com/${kind}/${project}`;
