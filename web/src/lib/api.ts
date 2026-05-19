// Admin API base: routes are mounted at /admin on the plugin proxy path
// /api/v1/plugins/{installId}. Detect that prefix at runtime.
function base(): string {
  const m = window.location.pathname.match(/^(\/api\/v1\/plugins\/\d+)/);
  return (m ? m[1] : "") + "/admin";
}

let token: string | null = null;
let theme: string | null = null;
(function capture() {
  const p = new URLSearchParams(window.location.search);
  const t = p.get("token");
  const th = p.get("theme") ?? sessionStorage.getItem("continuum-theme");
  if (th) theme = th;
  if (t) {
    token = t;
    p.delete("token");
    window.history.replaceState(
      null,
      "",
      window.location.pathname +
        (p.toString() ? "?" + p.toString() : "") +
        window.location.hash,
    );
  }
})();

// getCachedTheme is consumed by components/ui/sonner.tsx.
export function getCachedTheme(): string | null {
  return theme;
}

async function call<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const res = await fetch(`${base()}${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "include",
  });
  if (!res.ok) {
    const e = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(
      e.error?.message ?? e.error ?? `Request failed (${res.status})`,
    );
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export type Library = {
  ID: number;
  Path: string;
  Name: string;
  MediaType: string;
  Enabled: boolean;
  LastScannedAt?: string | null;
};
export type ScanEvent = {
  id: number;
  library_name?: string;
  started_at: string;
  finished_at?: string | null;
  books_added: number;
  books_changed: number;
  books_deleted: number;
  error_text?: string;
};
export type FilesystemEntry = {
  name: string;
  path: string;
};
export type FilesystemBrowseResponse = {
  path: string;
  parent: string;
  entries: FilesystemEntry[];
};
export type AppConfig = {
  metadata_sources_enabled: string[];
  metadata_default_region: string;
  metadata_cache_ttl_days: number;
  metadata_rate_limit_rps: number;
  scan_inline_enrich: boolean;
  metadata_scan_source: string;
  googlebooks_api_key: string;
  isbndb_api_key: string;
  hardcover_api_key: string;
};

export const listLibraries = () =>
  call<{ items: Library[] }>("GET", "/libraries");
export const createLibrary = (b: {
  path: string;
  name: string;
  media_type: string;
  enabled: boolean;
}) => call<{ id: number }>("POST", "/libraries", b);
export const updateLibrary = (
  id: number,
  b: { name: string; media_type: string; enabled: boolean },
) => call("PATCH", `/libraries/${id}`, b);
export const deleteLibrary = (id: number) => call("DELETE", `/libraries/${id}`);
export const scanLibrary = (id: number) =>
  call<{ scan_event_id: number }>("POST", `/libraries/${id}/scan`);
export const browseFilesystem = (path: string) =>
  call<FilesystemBrowseResponse>(
    "GET",
    `/filesystem/browse?${new URLSearchParams({ path }).toString()}`,
  );
export const scanAll = () => call<{ scan_event_id: number }>("POST", "/scan");
export const listScans = () => call<{ items: ScanEvent[] }>("GET", "/scans");
export const metadataQueue = () =>
  call<Record<string, number>>("GET", "/metadata/queue");
export const metadataBackfill = () =>
  call<{ queued: number }>("POST", "/metadata/backfill");
export const diagnostics = () =>
  call<Record<string, unknown>>("GET", "/diagnostics");
export const getConfig = () => call<AppConfig>("GET", "/config");
export const updateConfig = (body: AppConfig) =>
  call<AppConfig>("PUT", "/config", body);

export const MEDIA_TYPES = ["book", "comics", "manga", "documents"] as const;
