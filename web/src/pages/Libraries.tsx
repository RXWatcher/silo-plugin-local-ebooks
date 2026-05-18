import { FormEvent, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  browseFilesystem,
  createLibrary,
  deleteLibrary,
  listLibraries,
  scanLibrary,
  updateLibrary,
  MEDIA_TYPES,
  type FilesystemBrowseResponse,
  type Library,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

export default function Libraries() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["libraries"], queryFn: listLibraries });
  const [form, setForm] = useState({ path: "", name: "", media_type: "book", enabled: true });
  const [browserPath, setBrowserPath] = useState("/");
  const [browserOpen, setBrowserOpen] = useState(false);

  const invalidate = () => qc.invalidateQueries({ queryKey: ["libraries"] });
  const browser = useQuery({
    queryKey: ["filesystem-browser", browserPath],
    queryFn: () => browseFilesystem(browserPath),
    enabled: browserOpen,
  });
  const openBrowser = () => {
    setBrowserPath(form.path || "/");
    setBrowserOpen(true);
  };
  const applyBrowserPath = (path: string) => {
    setForm((current) => ({ ...current, path }));
    setBrowserOpen(false);
  };
  const create = useMutation({
    mutationFn: () => createLibrary(form),
    onSuccess: () => { toast.success("Library created"); setForm({ path: "", name: "", media_type: "book", enabled: true }); invalidate(); },
    onError: (e: Error) => toast.error(e.message),
  });
  const patch = useMutation({
    mutationFn: (l: Library) => updateLibrary(l.ID, { name: l.Name, media_type: l.MediaType, enabled: l.Enabled }),
    onSuccess: () => { toast.success("Saved"); invalidate(); },
    onError: (e: Error) => toast.error(e.message),
  });
  const remove = useMutation({
    mutationFn: (id: number) => deleteLibrary(id),
    onSuccess: () => { toast.success("Library removed"); invalidate(); },
    onError: (e: Error) => toast.error(e.message),
  });
  const scan = useMutation({
    mutationFn: (id: number) => scanLibrary(id),
    onSuccess: () => toast.success("Scan started"),
    onError: (e: Error) => toast.error(e.message),
  });

  if (q.isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (q.error) return <p className="text-sm text-destructive">{(q.error as Error).message}</p>;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-2 rounded-md border p-3">
        <Input placeholder="/srv/comics" value={form.path}
          onChange={(e) => setForm({ ...form, path: e.target.value })} className="w-64" />
        <Button type="button" variant="outline" onClick={openBrowser}>Browse</Button>
        <Input placeholder="Name" value={form.name}
          onChange={(e) => setForm({ ...form, name: e.target.value })} className="w-40" />
        <select className="h-9 rounded-md border px-2 text-sm" value={form.media_type}
          onChange={(e) => setForm({ ...form, media_type: e.target.value })}>
          {MEDIA_TYPES.map((m) => <option key={m} value={m}>{m}</option>)}
        </select>
        <label className="flex items-center gap-1 text-sm">
          <input type="checkbox" checked={form.enabled}
            onChange={(e) => setForm({ ...form, enabled: e.target.checked })} /> enabled
        </label>
        <Button onClick={() => create.mutate()} disabled={create.isPending}>Add library</Button>
      </div>

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Path</TableHead><TableHead>Name</TableHead>
            <TableHead>Media type</TableHead><TableHead>Enabled</TableHead>
            <TableHead>Last scanned</TableHead><TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(q.data?.items ?? []).map((l) => (
            <TableRow key={l.ID}>
              <TableCell className="font-mono text-xs">{l.Path}</TableCell>
              <TableCell>
                <Input defaultValue={l.Name} className="h-8"
                  onBlur={(e) => e.target.value !== l.Name && patch.mutate({ ...l, Name: e.target.value })} />
              </TableCell>
              <TableCell>
                <select className="h-8 rounded-md border px-2 text-sm" value={l.MediaType}
                  onChange={(e) => patch.mutate({ ...l, MediaType: e.target.value })}>
                  {MEDIA_TYPES.map((m) => <option key={m} value={m}>{m}</option>)}
                </select>
              </TableCell>
              <TableCell>
                <input type="checkbox" checked={l.Enabled}
                  onChange={(e) => patch.mutate({ ...l, Enabled: e.target.checked })} />
              </TableCell>
              <TableCell className="text-xs">{l.LastScannedAt ?? "never"}</TableCell>
              <TableCell className="flex gap-1">
                <Button size="sm" variant="outline" onClick={() => scan.mutate(l.ID)}>Scan</Button>
                <Button size="sm" variant="destructive"
                  onClick={() => {
                    if (confirm(`Remove "${l.Name}"? This deletes its catalog entries (files on disk are untouched).`))
                      remove.mutate(l.ID);
                  }}>Remove</Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>

      {browserOpen ? (
        <PathBrowser
          data={browser.data}
          error={browser.error as Error | null}
          loading={browser.isLoading}
          path={browserPath}
          onPathChange={setBrowserPath}
          onClose={() => setBrowserOpen(false)}
          onSelect={applyBrowserPath}
        />
      ) : null}
    </div>
  );
}

function PathBrowser({
  data,
  error,
  loading,
  path,
  onPathChange,
  onClose,
  onSelect,
}: {
  data?: FilesystemBrowseResponse;
  error: Error | null;
  loading: boolean;
  path: string;
  onPathChange: (path: string) => void;
  onClose: () => void;
  onSelect: (path: string) => void;
}) {
  const [draftPath, setDraftPath] = useState(path);

  useEffect(() => {
    setDraftPath(path);
  }, [path]);

  const openDraftPath = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    onPathChange(draftPath);
  };

  return (
    <div className="fixed inset-0 z-50 bg-background/80 p-4 backdrop-blur-sm">
      <div className="mx-auto flex max-h-[85vh] max-w-3xl flex-col gap-3 overflow-hidden rounded-md border bg-card p-4 shadow-lg">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="text-lg font-semibold">Browse container paths</h2>
            <p className="text-sm text-muted-foreground">Only directories are shown. Pick a folder mounted inside the plugin container.</p>
          </div>
          <Button type="button" variant="outline" onClick={onClose}>Close</Button>
        </div>
        <form className="flex gap-2" onSubmit={openDraftPath}>
          <Input value={draftPath} onChange={(event) => setDraftPath(event.target.value)} />
          <Button type="submit" variant="outline">Open</Button>
        </form>
        {error ? <p className="text-sm text-destructive">{error.message}</p> : null}
        {loading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
        {data ? (
          <>
            <div className="flex items-center gap-2 rounded-md border bg-surface px-3 py-2 text-sm">
              <span className="min-w-0 flex-1 truncate font-mono">{data.path}</span>
              <Button type="button" size="sm" onClick={() => onSelect(data.path)}>Use this folder</Button>
            </div>
            <div className="overflow-auto rounded-md border">
              <button
                type="button"
                className="block w-full border-b px-3 py-2 text-left text-sm hover:bg-surface-hover"
                onClick={() => onPathChange(data.parent)}
                disabled={data.parent === data.path}
              >
                ..
              </button>
              {data.entries.length === 0 ? (
                <p className="px-3 py-6 text-center text-sm text-muted-foreground">No child directories.</p>
              ) : (
                data.entries.map((entry) => (
                  <button
                    key={entry.path}
                    type="button"
                    className="block w-full border-b px-3 py-2 text-left text-sm hover:bg-surface-hover"
                    onClick={() => onPathChange(entry.path)}
                  >
                    <span className="font-medium">{entry.name}</span>
                    <span className="ml-2 font-mono text-xs text-muted-foreground">{entry.path}</span>
                  </button>
                ))
              )}
            </div>
          </>
        ) : null}
      </div>
    </div>
  );
}
