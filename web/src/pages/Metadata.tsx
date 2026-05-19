import { FormEvent, ReactNode, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  AppConfig,
  getConfig,
  metadataBackfill,
  metadataQueue,
  updateConfig,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type ConfigForm = {
  metadata_sources_enabled: string;
  metadata_default_region: string;
  metadata_cache_ttl_days: number;
  metadata_rate_limit_rps: number;
  scan_inline_enrich: boolean;
  metadata_scan_source: string;
  googlebooks_api_key: string;
  isbndb_api_key: string;
  hardcover_api_key: string;
};

function toForm(cfg: AppConfig): ConfigForm {
  return {
    metadata_sources_enabled: cfg.metadata_sources_enabled.join(", "),
    metadata_default_region: cfg.metadata_default_region,
    metadata_cache_ttl_days: cfg.metadata_cache_ttl_days,
    metadata_rate_limit_rps: cfg.metadata_rate_limit_rps,
    scan_inline_enrich: cfg.scan_inline_enrich,
    metadata_scan_source: cfg.metadata_scan_source,
    googlebooks_api_key: cfg.googlebooks_api_key,
    isbndb_api_key: cfg.isbndb_api_key,
    hardcover_api_key: cfg.hardcover_api_key,
  };
}

function fromForm(form: ConfigForm): AppConfig {
  return {
    ...form,
    metadata_sources_enabled: form.metadata_sources_enabled
      .split(",")
      .map((x) => x.trim())
      .filter(Boolean),
    metadata_cache_ttl_days: Number(form.metadata_cache_ttl_days),
    metadata_rate_limit_rps: Number(form.metadata_rate_limit_rps),
  };
}

export default function Metadata() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["meta-queue"], queryFn: metadataQueue });
  const cfg = useQuery({ queryKey: ["app-config"], queryFn: getConfig });
  const [form, setForm] = useState<ConfigForm | null>(null);
  useEffect(() => {
    if (cfg.data) setForm(toForm(cfg.data));
  }, [cfg.data]);
  const backfill = useMutation({
    mutationFn: metadataBackfill,
    onSuccess: (r) => {
      toast.success(`Queued ${r.queued}`);
      qc.invalidateQueries({ queryKey: ["meta-queue"] });
    },
    onError: (e: Error) => toast.error(e.message),
  });
  const save = useMutation({
    mutationFn: (body: AppConfig) => updateConfig(body),
    onSuccess: (next) => {
      toast.success("Settings saved");
      setForm(toForm(next));
      qc.invalidateQueries({ queryKey: ["app-config"] });
      qc.invalidateQueries({ queryKey: ["diagnostics"] });
    },
    onError: (e: Error) => toast.error(e.message),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (form) save.mutate(fromForm(form));
  };
  if (q.error || cfg.error)
    return (
      <p className="text-sm text-destructive">
        {((q.error || cfg.error) as Error).message}
      </p>
    );
  return (
    <div className="space-y-4">
      <section className="rounded-md border p-4">
        <div className="mb-3 flex items-center justify-between gap-3">
          <h2 className="text-lg font-semibold">Metadata settings</h2>
          <Button
            form="metadata-settings"
            type="submit"
            disabled={!form || save.isPending}
          >
            Save settings
          </Button>
        </div>
        {form && (
          <form
            id="metadata-settings"
            className="grid gap-4 md:grid-cols-2"
            onSubmit={submit}
          >
            <Field label="Enabled sources">
              <Input
                value={form.metadata_sources_enabled}
                onChange={(e) =>
                  setForm({ ...form, metadata_sources_enabled: e.target.value })
                }
              />
            </Field>
            <Field label="Scan source">
              <Input
                value={form.metadata_scan_source}
                onChange={(e) =>
                  setForm({ ...form, metadata_scan_source: e.target.value })
                }
              />
            </Field>
            <Field label="Default region">
              <Input
                value={form.metadata_default_region}
                onChange={(e) =>
                  setForm({ ...form, metadata_default_region: e.target.value })
                }
              />
            </Field>
            <Field label="Cache TTL days">
              <Input
                type="number"
                min={1}
                value={form.metadata_cache_ttl_days}
                onChange={(e) =>
                  setForm({
                    ...form,
                    metadata_cache_ttl_days: Number(e.target.value),
                  })
                }
              />
            </Field>
            <Field label="Rate limit RPS">
              <Input
                type="number"
                min={1}
                value={form.metadata_rate_limit_rps}
                onChange={(e) =>
                  setForm({
                    ...form,
                    metadata_rate_limit_rps: Number(e.target.value),
                  })
                }
              />
            </Field>
            <label className="flex items-center gap-2 pt-6 text-sm font-medium">
              <input
                type="checkbox"
                checked={form.scan_inline_enrich}
                onChange={(e) =>
                  setForm({ ...form, scan_inline_enrich: e.target.checked })
                }
              />
              Inline enrichment on scan
            </label>
            <Field label="Google Books API key">
              <Input
                type="password"
                value={form.googlebooks_api_key}
                onChange={(e) =>
                  setForm({ ...form, googlebooks_api_key: e.target.value })
                }
              />
            </Field>
            <Field label="ISBNdb API key">
              <Input
                type="password"
                value={form.isbndb_api_key}
                onChange={(e) =>
                  setForm({ ...form, isbndb_api_key: e.target.value })
                }
              />
            </Field>
            <Field label="Hardcover API key">
              <Input
                type="password"
                value={form.hardcover_api_key}
                onChange={(e) =>
                  setForm({ ...form, hardcover_api_key: e.target.value })
                }
              />
            </Field>
          </form>
        )}
      </section>
      <section className="space-y-3 rounded-md border p-4">
        <h2 className="text-lg font-semibold">Queue</h2>
        <pre className="rounded-md border bg-muted/30 p-3 text-xs">
          {JSON.stringify(q.data ?? {}, null, 2)}
        </pre>
        <Button onClick={() => backfill.mutate()} disabled={backfill.isPending}>
          Backfill all
        </Button>
      </section>
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-2">
      <Label>{label}</Label>
      {children}
    </div>
  );
}
