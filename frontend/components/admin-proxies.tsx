"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Plus, Globe, Wifi, WifiOff, Activity, MoreHorizontal, Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { BulkActionDialog } from "@/components/admin/bulk-action-dialog";
import { StatusBadge } from "@/components/admin/status-badge";
import { MetricCardGroup, StatCard } from "@/components/admin/stat-card";
import { RowAvatar } from "@/components/admin/row-avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { useT } from "@/components/locale-provider";

interface Proxy {
  id: number;
  proxy_url: string;
  proxy_type: string;
  country_code: string;
  is_alive: boolean;
  current_bindings: number;
  max_bindings: number;
  failure_count: number;
}

// Parse "IP:Port:User:Pass" (user:pass optional) into the API's proxy objects.
function parseProxies(raw: string, type: string, country: string) {
  return raw
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .map((line) => {
      const [ip, port, user, pass] = line.split(":");
      if (!ip || !port) return null;
      const auth = user && pass ? `${user}:${pass}@` : "";
      return { url: `${type}://${auth}${ip}:${port}`, type, country: country.toUpperCase() };
    })
    .filter((p): p is { url: string; type: string; country: string } => p !== null);
}

const PAGE_SIZE = 20;

interface ProxyStats { total: number; alive: number; dead: number }

export function AdminProxies() {
  const t = useT();
  const [rows, setRows] = useState<Proxy[] | null>(null);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<ProxyStats | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [alive, setAlive] = useState<"all" | "true" | "false">("all");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [editTarget, setEditTarget] = useState<Proxy | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Proxy | null>(null);
  const [sel, setSel] = useState<Set<string | number>>(new Set());
  const [bulkDeleteKeys, setBulkDeleteKeys] = useState<Array<string | number> | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (alive !== "all") params.set("alive", alive);
    try {
      const d = await api.get<{ rows: Proxy[]; total: number; stats: ProxyStats }>(
        `/admin/resources/proxies?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setStats(d.stats);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.proxies.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, alive, t]);

  useEffect(() => {
    load();
  }, [load]);

  // Bulk delete loops the same single-item DELETE the row action uses; a
  // proxy still bound to devices can fail server-side, so each item's
  // outcome is reported honestly rather than pre-filtered client-side.
  async function submitBulkDelete() {
    if (!bulkDeleteKeys) return;
    let okCount = 0;
    let failCount = 0;
    for (const k of bulkDeleteKeys) {
      try {
        await api.delete(`/admin/resources/proxies/${k}`);
        okCount++;
      } catch {
        failCount++;
      }
    }
    const desc = t("admin.proxies.bulk.resultDesc")
      .replace("{ok}", () => String(okCount))
      .replace("{fail}", () => String(failCount));
    if (failCount === 0) {
      toast.success(t("admin.proxies.bulk.resultTitle"), { description: desc });
    } else {
      toast.error(t("admin.proxies.bulk.resultTitle"), { description: desc });
    }
    setBulkDeleteKeys(null);
    setSel(new Set());
    // Server-paged: if this wiped the whole current page, step back one page so
    // the admin doesn't land on a now-empty tail page. Changing `page` re-fires
    // the load effect on its own, so don't also call load() here (double fetch).
    // If rows remain on this page, stay put and reload.
    const removedWholePage = bulkDeleteKeys.length >= (rows?.length ?? 0) && page > 0;
    if (removedWholePage) {
      setPage((p) => Math.max(0, p - 1));
    } else {
      load();
    }
  }

  const columns: Column<Proxy>[] = [
    {
      key: "url",
      header: t("admin.proxies.col.address"),
      cell: (p) => (
        <div className="flex items-center gap-2.5">
          <RowAvatar icon={Globe} accent={p.is_alive ? "emerald" : "rose"} />
          <span className="font-mono text-xs">{p.proxy_url}</span>
        </div>
      ),
    },
    {
      key: "type",
      header: t("admin.proxies.col.type"),
      cell: (p) => <span className="font-mono text-xs uppercase">{p.proxy_type}</span>,
    },
    {
      key: "country",
      header: t("admin.proxies.col.country"),
      cell: (p) => <span className="font-mono text-xs">{p.country_code}</span>,
    },
    {
      key: "status",
      header: t("admin.proxies.col.status"),
      cell: (p) => (
        <StatusBadge tone={p.is_alive ? "positive" : "negative"}>
          {p.is_alive ? t("admin.proxies.status.online") : t("admin.proxies.status.invalid")}
        </StatusBadge>
      ),
    },
    {
      key: "bindings",
      header: t("admin.proxies.col.bindings"),
      align: "right",
      cell: (p) => (
        <span className="font-mono text-sm tabular-nums">
          {p.current_bindings}/{p.max_bindings}
        </span>
      ),
    },
    {
      key: "failures",
      header: t("admin.proxies.col.failures"),
      align: "right",
      cell: (p) => (
        <span className="font-mono text-sm tabular-nums text-muted-foreground">
          {p.failure_count}
        </span>
      ),
    },
  ];

  return (
    <>
      {stats && (
        <MetricCardGroup className="mb-6">
          <StatCard
            accent="brand"
            label={t("admin.proxies.stat.totalLabel")}
            value={String(stats.total)}
            sub={t("admin.proxies.stat.totalSub")}
            icon={Globe}
          />
          <StatCard
            accent="emerald"
            label={t("admin.proxies.status.online")}
            value={String(stats.alive)}
            sub={t("admin.proxies.stat.onlineSub")}
            icon={Wifi}
          />
          <StatCard
            accent="rose"
            label={t("admin.proxies.stat.offlineLabel")}
            value={String(stats.dead)}
            sub={t("admin.proxies.stat.offlineSub")}
            icon={WifiOff}
          />
          <StatCard
            accent="amber"
            label={t("admin.proxies.stat.currentPageLabel")}
            value={String(total)}
            sub={t("admin.proxies.stat.currentPageSub")}
            icon={Activity}
          />
        </MetricCardGroup>
      )}
      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(p) => p.id}
        emptyState={t("admin.proxies.emptyState")}
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query,
          onQueryChange: (q) => { setQuery(q); setPage(0); },
          loading,
        }}
        search={{ placeholder: t("admin.proxies.searchPlaceholder"), accessor: () => "" }}
        selection={{
          selected: sel,
          onChange: setSel,
          actions: (keys) => (
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5 text-destructive hover:text-destructive"
              onClick={() => setBulkDeleteKeys(keys)}
            >
              <Trash2 className="size-3.5" />
              {t("admin.proxies.bulk.delete")}
            </Button>
          ),
        }}
        toolbar={
          <div className="flex items-center gap-2">
            <select
              value={alive}
              onChange={(e) => { setAlive(e.target.value as "all" | "true" | "false"); setPage(0); }}
              className="h-8 rounded-md border bg-transparent px-2 text-sm"
              aria-label={t("admin.proxies.filterByStatusAria")}
            >
              <option value="all">{t("admin.proxies.filter.allStatuses")}</option>
              <option value="true">{t("admin.proxies.status.online")}</option>
              <option value="false">{t("admin.proxies.status.invalid")}</option>
            </select>
            <ImportProxiesDialog onDone={load} />
          </div>
        }
        rowActions={(p) => (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={<Button variant="ghost" size="icon-sm" aria-label={t("admin.proxies.actionsAria")} />}
            >
              <MoreHorizontal className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => setEditTarget(p)}>
                <Pencil className="size-4" />
                {t("admin.proxies.action.edit")}
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onClick={() => setDeleteTarget(p)}>
                <Trash2 className="size-4" />
                {t("admin.proxies.action.delete")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      />

      <EditProxyDialog target={editTarget} onClose={() => setEditTarget(null)} onDone={load} />
      <DeleteProxyDialog target={deleteTarget} onClose={() => setDeleteTarget(null)} onDone={load} />
      <BulkActionDialog
        open={bulkDeleteKeys != null}
        onOpenChange={(o) => !o && setBulkDeleteKeys(null)}
        title={t("admin.proxies.bulkDeleteDialog.title")}
        description={t("admin.proxies.bulkDeleteDialog.desc").replace("{n}", () => String(bulkDeleteKeys?.length ?? 0))}
        confirmLabel={t("admin.proxies.bulk.confirm")}
        destructive
        onConfirm={submitBulkDelete}
      />
    </>
  );
}

function ImportProxiesDialog({ onDone }: { onDone: () => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [raw, setRaw] = useState("");
  const [type, setType] = useState("socks5");
  const [country, setCountry] = useState("US");
  const [busy, setBusy] = useState(false);

  const parsed = useMemo(() => parseProxies(raw, type, country), [raw, type, country]);
  const valid = parsed.length > 0 && country.trim().length === 2 && !busy;

  async function submit() {
    setBusy(true);
    try {
      const res = await api.post<{ imported: number; skipped: number }>(
        "/admin/resources/proxies",
        { proxies: parsed },
      );
      toast.success(t("admin.proxies.import.successTitle"), {
        description: t("admin.proxies.import.successDesc")
          .replace("{added}", () => String(res.imported))
          .replace("{skipped}", () => String(res.skipped)),
      });
      setOpen(false);
      setRaw("");
      onDone();
    } catch (e) {
      toast.error(t("admin.proxies.import.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.proxies.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button className="gap-2" />}>
        <Plus className="size-4" />
        {t("admin.proxies.import.title")}
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("admin.proxies.import.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.proxies.import.descPrefix")}
            <span className="font-mono">IP:Port:User:Pass</span>
            {t("admin.proxies.import.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="flex gap-3">
            <div className="space-y-2">
              <label htmlFor="px-type" className="text-sm font-medium">{t("admin.proxies.typeLabel")}</label>
              <select
                id="px-type"
                value={type}
                onChange={(e) => setType(e.target.value)}
                className="h-9 rounded-md border bg-transparent px-3 font-mono text-sm"
              >
                <option value="socks5">socks5</option>
                <option value="http">http</option>
              </select>
            </div>
            <div className="space-y-2">
              <label htmlFor="px-country" className="text-sm font-medium">
                {t("admin.proxies.countryIsoLabel")}
              </label>
              <Input
                id="px-country"
                value={country}
                onChange={(e) => setCountry(e.target.value)}
                maxLength={2}
                className="w-24 font-mono uppercase"
              />
            </div>
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <label htmlFor="px-raw" className="text-sm font-medium">{t("admin.proxies.listLabel")}</label>
              <span className="font-mono text-xs tabular-nums text-muted-foreground">
                {t("admin.proxies.parsedCount").replace("{n}", () => String(parsed.length))}
              </span>
            </div>
            <Textarea
              id="px-raw"
              value={raw}
              onChange={(e) => setRaw(e.target.value)}
              placeholder={"203.0.113.5:1080:user:pass\n198.51.100.9:1080"}
              className="h-40 resize-none font-mono text-sm"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.proxies.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy
              ? t("admin.proxies.import.importing")
              : t("admin.proxies.import.confirm").replace("{n}", () => String(parsed.length))}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function EditProxyDialog({
  target,
  onClose,
  onDone,
}: {
  target: Proxy | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [type, setType] = useState("socks5");
  const [country, setCountry] = useState("");
  const [maxBindings, setMaxBindings] = useState("1");
  const [busy, setBusy] = useState(false);

  // Reset fields whenever a new target opens the dialog.
  useEffect(() => {
    if (target) {
      setType(target.proxy_type);
      setCountry(target.country_code);
      setMaxBindings(String(target.max_bindings));
    }
  }, [target]);

  const maxBindingsNum = Number(maxBindings);
  const valid =
    country.trim().length === 2 &&
    Number.isInteger(maxBindingsNum) &&
    maxBindingsNum >= 1 &&
    !busy;

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.put(`/admin/resources/proxies/${target.id}`, {
        proxy_type: type,
        country_code: country.trim().toUpperCase(),
        max_bindings: maxBindingsNum,
      });
      toast.success(t("admin.proxies.edit.successTitle"), { description: target.proxy_url });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.proxies.edit.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.proxies.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.proxies.edit.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.proxies.edit.descPrefix")}
            <span className="font-mono text-xs">{target?.proxy_url}</span>
            {t("admin.proxies.edit.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="flex gap-3">
            <div className="space-y-2">
              <label htmlFor="edit-px-type" className="text-sm font-medium">{t("admin.proxies.typeLabel")}</label>
              <select
                id="edit-px-type"
                value={type}
                onChange={(e) => setType(e.target.value)}
                className="h-9 rounded-md border bg-transparent px-3 font-mono text-sm"
              >
                <option value="socks5">socks5</option>
                <option value="http">http</option>
                <option value="https">https</option>
              </select>
            </div>
            <div className="space-y-2">
              <label htmlFor="edit-px-country" className="text-sm font-medium">
                {t("admin.proxies.countryIsoLabel")}
              </label>
              <Input
                id="edit-px-country"
                value={country}
                onChange={(e) => setCountry(e.target.value)}
                maxLength={2}
                className="w-24 font-mono uppercase"
              />
            </div>
          </div>
          <div className="space-y-2">
            <label htmlFor="edit-px-max" className="text-sm font-medium">
              {t("admin.proxies.maxBindingsLabel")}
            </label>
            <Input
              id="edit-px-max"
              type="number"
              min={1}
              value={maxBindings}
              onChange={(e) => setMaxBindings(e.target.value)}
              className="w-32 font-mono"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.proxies.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("admin.proxies.edit.saving") : t("admin.proxies.edit.confirmSave")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeleteProxyDialog({
  target,
  onClose,
  onDone,
}: {
  target: Proxy | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.delete(`/admin/resources/proxies/${target.id}`);
      toast.success(t("admin.proxies.delete.successTitle"), { description: target.proxy_url });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.proxies.delete.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.proxies.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.proxies.delete.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.proxies.delete.descPrefix")}
            <span className="font-mono text-xs">{target?.proxy_url}</span>
            {t("admin.proxies.delete.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.proxies.cancel")}</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? t("admin.proxies.delete.deleting") : t("admin.proxies.delete.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
