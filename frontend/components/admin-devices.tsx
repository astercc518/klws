"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Plus,
  QrCode,
  Smartphone,
  Wifi,
  ShieldAlert,
  LogOut,
  MoreHorizontal,
  Network,
  ChevronDown,
  Unplug,
  Pencil,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { BulkActionDialog } from "@/components/admin/bulk-action-dialog";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";
import { MetricCardGroup, StatCard, type Accent } from "@/components/admin/stat-card";
import { RowAvatar } from "@/components/admin/row-avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { useT } from "@/components/locale-provider";

// StatusTone → avatar accent hue, so a device's monogram tint matches its badge.
const TONE_ACCENT: Record<StatusTone, Accent> = {
  positive: "emerald",
  negative: "rose",
  warning: "amber",
  neutral: "neutral",
};
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

interface Device {
  id: number;
  tenant_id: number;
  account_jid: string;
  phone_number: string;
  ban_status: string;
  owner_node: string | null;
  last_connected_at: string | null;
  tags: string[];
  proxy_id: number | null;
  proxy_url: string | null;
}

interface Proxy {
  id: number;
  proxy_url: string;
  proxy_type: string;
  country_code: string;
  is_alive: boolean;
  current_bindings: number;
  max_bindings: number;
  failure_count: number;
  bound_devices: number;
}

// Deterministic tint per tag string, so "US-Marketing" always reads the same
// hue across rows. A small fixed palette keeps the table calm but multi-color.
const TAG_PALETTE = [
  "bg-sky-500/10 text-sky-700 ring-sky-600/20 dark:text-sky-400",
  "bg-violet-500/10 text-violet-700 ring-violet-600/20 dark:text-violet-400",
  "bg-emerald-500/10 text-emerald-700 ring-emerald-600/20 dark:text-emerald-400",
  "bg-amber-500/10 text-amber-700 ring-amber-600/20 dark:text-amber-400",
  "bg-rose-500/10 text-rose-700 ring-rose-600/20 dark:text-rose-400",
  "bg-teal-500/10 text-teal-700 ring-teal-600/20 dark:text-teal-400",
  "bg-fuchsia-500/10 text-fuchsia-700 ring-fuchsia-600/20 dark:text-fuchsia-400",
  "bg-indigo-500/10 text-indigo-700 ring-indigo-600/20 dark:text-indigo-400",
];
function tagColor(tag: string): string {
  let h = 0;
  for (let i = 0; i < tag.length; i++) h = (h * 31 + tag.charCodeAt(i)) >>> 0;
  return TAG_PALETTE[h % TAG_PALETTE.length];
}

function TagBadges({ tags }: { tags: string[] }) {
  if (!tags || tags.length === 0) {
    return <span className="font-mono text-xs text-muted-foreground">—</span>;
  }
  return (
    <div className="flex flex-wrap gap-1">
      {tags.map((t) => (
        <span
          key={t}
          className={cn(
            "inline-flex h-5 items-center rounded-full px-2 text-[11px] font-medium ring-1 ring-inset",
            tagColor(t),
          )}
        >
          {t}
        </span>
      ))}
    </div>
  );
}

// Status → tone. Online = active and currently owned by a node. Banned reads
// red, flagged reads amber (a softer warning), the rest stay neutral gray.
// Returns a dict key (not the label text) so this stays a plain module-level
// function; callers resolve the key via t() at render time.
function statusBadge(d: Device): { key: string; tone: StatusTone } {
  if (d.ban_status === "banned") return { key: "admin.devices.status.banned", tone: "negative" };
  if (d.ban_status === "flagged") return { key: "admin.devices.status.flagged", tone: "warning" };
  if (d.ban_status === "active" && d.owner_node)
    return { key: "admin.devices.status.online", tone: "positive" };
  if (d.ban_status === "logged_out") return { key: "admin.devices.status.loggedOut", tone: "neutral" };
  return {
    key: d.owner_node ? "admin.devices.status.online" : "admin.devices.status.offline",
    tone: d.owner_node ? "positive" : "neutral",
  };
}

type ParsedDevice = {
  tenant_id: number;
  account_jid: string;
  phone: string;
  tags: string[];
};

// Parse "tenant_id:account_jid:phone[:tag1,tag2,...]" per line. The 4th
// colon-field is optional and holds comma-separated tags.
function parseDevices(raw: string): ParsedDevice[] {
  return raw
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .map((line): ParsedDevice | null => {
      const [tid, jid, phone, tagsRaw] = line.split(":");
      const tenant_id = Number(tid);
      if (!tenant_id || !jid || !phone) return null;
      const tags = (tagsRaw ?? "")
        .split(",")
        .map((t) => t.trim())
        .filter(Boolean);
      return { tenant_id, account_jid: jid, phone, tags };
    })
    .filter((d): d is ParsedDevice => d !== null);
}

const PAGE_SIZE = 20;

interface DeviceStats {
  total: number;
  online: number;
  banned: number;
  flagged: number;
  logged_out: number;
}

export function AdminDevices() {
  const t = useT();
  const [rows, setRows] = useState<Device[] | null>(null);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<DeviceStats | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [banStatus, setBanStatus] = useState<"all" | "active" | "banned" | "flagged" | "logged_out">("all");
  const [online, setOnline] = useState<"all" | "true" | "false">("all");
  const [loading, setLoading] = useState(false);
  const [proxies, setProxies] = useState<Proxy[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [proxyTarget, setProxyTarget] = useState<Device | null>(null);
  const [editTarget, setEditTarget] = useState<Device | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Device | null>(null);
  const [sel, setSel] = useState<Set<string | number>>(new Set());
  const [bulkDeleteKeys, setBulkDeleteKeys] = useState<Array<string | number> | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (banStatus !== "all") params.set("ban_status", banStatus);
    if (online !== "all") params.set("online", online);
    try {
      const d = await api.get<{ rows: Device[]; total: number; stats: DeviceStats }>(
        `/admin/resources/devices?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setStats(d.stats);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.devices.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, banStatus, online, t]);
  const loadProxies = useCallback(async () => {
    try {
      const d = await api.get<{ rows: Proxy[] }>("/admin/resources/proxies?alive=true&limit=200");
      setProxies(d.rows);
    } catch {
      // proxy pool is non-critical for the table; the bind dialog will simply
      // show "无可用代理" if this fails.
    }
  }, []);
  // Refresh both after a bind/unbind so the device row's IP and the proxy
  // pool's binding counts stay in sync.
  const refresh = useCallback(() => {
    load();
    loadProxies();
  }, [load, loadProxies]);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    loadProxies();
  }, [loadProxies]);

  // Bulk delete loops the same single-item DELETE the row action uses; a
  // device tied to an active proxy binding or campaign traffic can still
  // fail server-side, so each item's outcome is reported honestly rather
  // than pre-filtered client-side.
  async function submitBulkDelete() {
    if (!bulkDeleteKeys) return;
    let okCount = 0;
    let failCount = 0;
    for (const k of bulkDeleteKeys) {
      try {
        await api.delete(`/admin/resources/devices/${k}`);
        okCount++;
      } catch {
        failCount++;
      }
    }
    const desc = t("admin.devices.bulk.resultDesc")
      .replace("{ok}", () => String(okCount))
      .replace("{fail}", () => String(failCount));
    if (failCount === 0) {
      toast.success(t("admin.devices.bulk.resultTitle"), { description: desc });
    } else {
      toast.error(t("admin.devices.bulk.resultTitle"), { description: desc });
    }
    setBulkDeleteKeys(null);
    setSel(new Set());
    refresh();
  }

  const columns: Column<Device>[] = [
    {
      key: "jid",
      header: t("admin.devices.col.jid"),
      cell: (d) => (
        <div className="flex items-center gap-2.5">
          <RowAvatar icon={Smartphone} accent={TONE_ACCENT[statusBadge(d).tone]} />
          <span className="font-mono text-xs">{d.account_jid}</span>
        </div>
      ),
    },
    {
      key: "phone",
      header: t("admin.devices.col.phone"),
      cell: (d) => <span className="font-mono text-xs">{d.phone_number}</span>,
    },
    {
      key: "tenant",
      header: t("admin.devices.col.tenant"),
      cell: (d) => <span className="font-mono text-xs text-muted-foreground">#{d.tenant_id}</span>,
    },
    {
      key: "tags",
      header: t("admin.devices.col.tags"),
      cell: (d) => <TagBadges tags={d.tags} />,
    },
    {
      key: "proxy",
      header: t("admin.devices.col.network"),
      cell: (d) =>
        d.proxy_url ? (
          <span className="inline-flex items-center gap-1.5 font-mono text-xs">
            <Network className="size-3.5 text-muted-foreground" />
            {d.proxy_url}
          </span>
        ) : (
          <StatusBadge tone="warning">{t("admin.devices.notBound")}</StatusBadge>
        ),
    },
    {
      key: "status",
      header: t("admin.devices.col.status"),
      cell: (d) => {
        const s = statusBadge(d);
        return <StatusBadge tone={s.tone}>{t(s.key)}</StatusBadge>;
      },
    },
    {
      key: "node",
      header: t("admin.devices.col.node"),
      cell: (d) => (
        <span className="font-mono text-xs text-muted-foreground">{d.owner_node ?? "—"}</span>
      ),
    },
  ];

  return (
    <>
      {stats && (
        <MetricCardGroup className="mb-6">
          <StatCard
            accent="brand"
            label={t("admin.devices.stat.totalLabel")}
            value={String(stats.total)}
            sub={t("admin.devices.stat.totalSub")}
            icon={Smartphone}
          />
          <StatCard
            accent="emerald"
            label={t("admin.devices.status.online")}
            value={String(stats.online)}
            sub={t("admin.devices.stat.onlineSub")}
            icon={Wifi}
          />
          <StatCard
            accent="rose"
            label={t("admin.devices.stat.riskyLabel")}
            value={String(stats.banned + stats.flagged)}
            sub={t("admin.devices.stat.riskySub")}
            icon={ShieldAlert}
          />
          <StatCard
            accent="amber"
            label={t("admin.devices.status.loggedOut")}
            value={String(stats.logged_out)}
            sub={t("admin.devices.stat.loggedOutSub")}
            icon={LogOut}
          />
        </MetricCardGroup>
      )}
      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(d) => d.id}
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query,
          onQueryChange: (q) => { setQuery(q); setPage(0); },
          loading,
        }}
        search={{ placeholder: t("admin.devices.searchPlaceholder"), accessor: () => "" }}
        emptyState={t("admin.devices.emptyState")}
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
              {t("admin.devices.bulk.delete")}
            </Button>
          ),
        }}
        toolbar={
          <div className="flex items-center gap-2">
            <select
              value={banStatus}
              onChange={(e) => {
                setBanStatus(e.target.value as "all" | "active" | "banned" | "flagged" | "logged_out");
                setPage(0);
              }}
              className="h-8 rounded-md border bg-transparent px-2 text-sm"
              aria-label={t("admin.devices.filterByStatusAria")}
            >
              <option value="all">{t("admin.devices.filter.allStatuses")}</option>
              <option value="active">{t("admin.devices.filter.active")}</option>
              <option value="banned">{t("admin.devices.status.banned")}</option>
              <option value="flagged">{t("admin.devices.status.flagged")}</option>
              <option value="logged_out">{t("admin.devices.status.loggedOut")}</option>
            </select>
            <select
              value={online}
              onChange={(e) => {
                setOnline(e.target.value as "all" | "true" | "false");
                setPage(0);
              }}
              className="h-8 rounded-md border bg-transparent px-2 text-sm"
              aria-label={t("admin.devices.filterByOnlineAria")}
            >
              <option value="all">{t("admin.devices.filter.all")}</option>
              <option value="true">{t("admin.devices.status.online")}</option>
              <option value="false">{t("admin.devices.status.offline")}</option>
            </select>
            <ConnectDeviceDialog />
            <ImportDevicesDialog onDone={load} />
          </div>
        }
        rowActions={(d) => (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={<Button variant="ghost" size="icon-sm" aria-label={t("admin.devices.actionsAria")} />}
            >
              <MoreHorizontal className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => setProxyTarget(d)}>
                <Network className="size-4" />
                {t("admin.devices.action.configureNetwork")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setEditTarget(d)}>
                <Pencil className="size-4" />
                {t("admin.devices.action.edit")}
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onClick={() => setDeleteTarget(d)}>
                <Trash2 className="size-4" />
                {t("admin.devices.action.delete")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      />

      <ProxyDialog
        target={proxyTarget}
        proxies={proxies}
        onClose={() => setProxyTarget(null)}
        onDone={refresh}
      />
      <EditDeviceDialog target={editTarget} onClose={() => setEditTarget(null)} onDone={load} />
      <DeleteDeviceDialog target={deleteTarget} onClose={() => setDeleteTarget(null)} onDone={load} />
      <BulkActionDialog
        open={bulkDeleteKeys != null}
        onOpenChange={(o) => !o && setBulkDeleteKeys(null)}
        title={t("admin.devices.bulkDeleteDialog.title")}
        description={t("admin.devices.bulkDeleteDialog.desc").replace("{n}", () => String(bulkDeleteKeys?.length ?? 0))}
        confirmLabel={t("admin.devices.bulk.confirm")}
        destructive
        onConfirm={submitBulkDelete}
      />
    </>
  );
}

// Live QR pairing is a node-side (cmd/wadist) whatsmeow flow — there is no HTTP
// streaming endpoint for it yet, and adding one would touch the cluster engine
// (off-limits). This dialog reserves the UX and is honest about that.
function ConnectDeviceDialog() {
  const t = useT();
  return (
    <Dialog>
      <DialogTrigger render={<Button variant="outline" className="gap-2" />}>
        <QrCode className="size-4" />
        {t("admin.devices.connect.trigger")}
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.devices.connect.title")}</DialogTitle>
          <DialogDescription>{t("admin.devices.connect.desc")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col items-center gap-3 py-4">
          <div className="flex size-52 items-center justify-center rounded-lg border border-dashed bg-muted/40">
            <QrCode className="size-16 text-muted-foreground/40" strokeWidth={1} />
          </div>
          <p className="max-w-xs text-center font-mono text-[11px] leading-relaxed text-muted-foreground">
            {t("admin.devices.connect.note")}
          </p>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.devices.close")}</DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ImportDevicesDialog({ onDone }: { onDone: () => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [raw, setRaw] = useState("");
  const [busy, setBusy] = useState(false);

  const parsed = useMemo(() => parseDevices(raw), [raw]);
  const valid = parsed.length > 0 && !busy;

  async function submit() {
    setBusy(true);
    try {
      const res = await api.post<{ imported: number; skipped: number }>(
        "/admin/resources/devices",
        { devices: parsed },
      );
      toast.success(t("admin.devices.import.successTitle"), {
        description: t("admin.devices.import.successDesc")
          .replace("{added}", () => String(res.imported))
          .replace("{skipped}", () => String(res.skipped)),
      });
      setOpen(false);
      setRaw("");
      onDone();
    } catch (e) {
      toast.error(t("admin.devices.import.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.devices.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button className="gap-2" />}>
        <Plus className="size-4" />
        {t("admin.devices.import.trigger")}
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("admin.devices.import.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.devices.import.descPrefix")}{" "}
            <span className="font-mono">tenant_id:account_jid:phone:tag1,tag2</span>
            {t("admin.devices.import.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <div className="flex items-center justify-between">
            <label htmlFor="dv-raw" className="text-sm font-medium">{t("admin.devices.import.listLabel")}</label>
            <span className="font-mono text-xs tabular-nums text-muted-foreground">
              {t("admin.devices.import.parsedCount").replace("{n}", () => String(parsed.length))}
            </span>
          </div>
          <Textarea
            id="dv-raw"
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            placeholder={"1:8613800000000@s.whatsapp.net:+8613800000000:US-Marketing,Tier1\n1:8613900000000@s.whatsapp.net:+8613900000000"}
            className="h-40 resize-none font-mono text-sm"
          />
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.devices.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy
              ? t("admin.devices.import.importing")
              : t("admin.devices.import.confirm").replace("{n}", () => String(parsed.length))}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ProxyDialog binds/unbinds a static proxy IP for one WS account, for
// per-account network isolation (防关联). Available proxies are those alive and
// under capacity; the currently-bound proxy is always listed even if full.
function ProxyDialog({
  target,
  proxies,
  onClose,
  onDone,
}: {
  target: Device | null;
  proxies: Proxy[];
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [selected, setSelected] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setSelected(target?.proxy_id ?? null);
  }, [target]);

  const selectable = useMemo(() => {
    const available = proxies.filter((p) => p.is_alive && p.current_bindings < p.max_bindings);
    const cur = target?.proxy_id;
    // Keep the current proxy visible even if it is now at capacity.
    if (cur != null && !available.some((p) => p.id === cur)) {
      const bound = proxies.find((p) => p.id === cur);
      if (bound) return [bound, ...available];
    }
    return available;
  }, [proxies, target]);

  const selectedProxy = proxies.find((p) => p.id === selected) ?? null;

  async function bind() {
    if (!target || selected == null) return;
    setBusy(true);
    try {
      await api.post(`/admin/resources/devices/${target.id}/proxy`, { proxy_id: selected });
      toast.success(t("admin.devices.proxyDialog.boundTitle"), { description: selectedProxy?.proxy_url });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.devices.proxyDialog.bindFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.devices.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  async function unbind() {
    if (!target) return;
    setBusy(true);
    try {
      await api.delete(`/admin/resources/devices/${target.id}/proxy`);
      toast.success(t("admin.devices.proxyDialog.unboundTitle"));
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.devices.proxyDialog.unbindFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.devices.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.devices.proxyDialog.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.devices.proxyDialog.descPrefix")}
            <span className="font-mono text-xs">{target?.account_jid}</span>
            {t("admin.devices.proxyDialog.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-1.5">
            <span className="text-sm font-medium">{t("admin.devices.proxyDialog.currentBinding")}</span>
            <div>
              {target?.proxy_url ? (
                <span className="inline-flex items-center gap-1.5 font-mono text-xs">
                  <Network className="size-3.5 text-muted-foreground" />
                  {target.proxy_url}
                </span>
              ) : (
                <StatusBadge tone="warning">{t("admin.devices.notBound")}</StatusBadge>
              )}
            </div>
          </div>
          <div className="space-y-1.5">
            <span className="text-sm font-medium">{t("admin.devices.proxyDialog.selectLabel")}</span>
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="outline" className="w-full justify-between gap-2 font-mono text-xs" />}
              >
                <span className="truncate">
                  {selectedProxy
                    ? `${selectedProxy.proxy_url} · ${selectedProxy.country_code}`
                    : t("admin.devices.proxyDialog.selectPlaceholder")}
                </span>
                <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="max-h-64 w-72 overflow-y-auto">
                {selectable.length === 0 ? (
                  <DropdownMenuItem disabled>{t("admin.devices.proxyDialog.noneAvailable")}</DropdownMenuItem>
                ) : (
                  selectable.map((p) => (
                    <DropdownMenuItem key={p.id} onClick={() => setSelected(p.id)}>
                      <span className="truncate font-mono text-xs">{p.proxy_url}</span>
                      <span className="ml-auto shrink-0 font-mono text-[10px] text-muted-foreground">
                        {p.country_code} · {p.current_bindings}/{p.max_bindings}
                      </span>
                    </DropdownMenuItem>
                  ))
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
        <DialogFooter className="sm:justify-between">
          {target?.proxy_id != null ? (
            <Button variant="ghost" className="gap-2 text-destructive" onClick={unbind} disabled={busy}>
              <Unplug className="size-4" />
              {t("admin.devices.proxyDialog.unbind")}
            </Button>
          ) : (
            <span />
          )}
          <div className="flex items-center gap-2">
            <DialogClose render={<Button variant="ghost" />}>{t("admin.devices.cancel")}</DialogClose>
            <Button
              onClick={bind}
              disabled={busy || selected == null || selected === target?.proxy_id}
            >
              {busy ? t("admin.devices.proxyDialog.submitting") : t("admin.devices.proxyDialog.confirmBind")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function EditDeviceDialog({
  target,
  onClose,
  onDone,
}: {
  target: Device | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [tenantId, setTenantId] = useState("");
  const [phone, setPhone] = useState("");
  const [tags, setTags] = useState("");
  const [busy, setBusy] = useState(false);

  // Reset fields whenever a new target opens the dialog.
  useEffect(() => {
    if (target) {
      setTenantId(String(target.tenant_id));
      setPhone(target.phone_number);
      setTags(target.tags.join(", "));
    }
  }, [target]);

  const tenantIdNum = Number(tenantId);
  const valid = tenantId.trim().length > 0 && Number.isInteger(tenantIdNum) && tenantIdNum > 0 && phone.trim().length > 0 && !busy;

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      const parsedTags = tags
        .split(/[,\s]+/)
        .map((t) => t.trim())
        .filter(Boolean);
      await api.put(`/admin/resources/devices/${target.id}`, {
        tenant_id: tenantIdNum,
        phone_number: phone.trim(),
        tags: parsedTags,
      });
      toast.success(t("admin.devices.edit.successTitle"), { description: target.account_jid });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.devices.edit.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.devices.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.devices.edit.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.devices.edit.descPrefix")}
            <span className="font-mono text-xs">{target?.account_jid}</span>
            {t("admin.devices.edit.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="flex gap-3">
            <div className="space-y-2">
              <label htmlFor="edit-dv-tenant" className="text-sm font-medium">
                {t("admin.devices.edit.tenantIdLabel")}
              </label>
              <Input
                id="edit-dv-tenant"
                type="number"
                min={1}
                value={tenantId}
                onChange={(e) => setTenantId(e.target.value)}
                className="w-28 font-mono"
              />
            </div>
            <div className="flex-1 space-y-2">
              <label htmlFor="edit-dv-phone" className="text-sm font-medium">{t("admin.devices.col.phone")}</label>
              <Input
                id="edit-dv-phone"
                value={phone}
                onChange={(e) => setPhone(e.target.value)}
                className="font-mono"
              />
            </div>
          </div>
          <div className="space-y-2">
            <label htmlFor="edit-dv-tags" className="text-sm font-medium">{t("admin.devices.col.tags")}</label>
            <Input
              id="edit-dv-tags"
              value={tags}
              onChange={(e) => setTags(e.target.value)}
              placeholder="US-Marketing, Tier1"
              className="font-mono"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.devices.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("admin.devices.edit.saving") : t("admin.devices.edit.confirmSave")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeleteDeviceDialog({
  target,
  onClose,
  onDone,
}: {
  target: Device | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.delete(`/admin/resources/devices/${target.id}`);
      toast.success(t("admin.devices.delete.successTitle"), { description: target.account_jid });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.devices.delete.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.devices.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.devices.delete.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.devices.delete.descPrefix")}
            <span className="font-mono text-xs">{target?.account_jid}</span>
            {t("admin.devices.delete.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.devices.cancel")}</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? t("admin.devices.delete.deleting") : t("admin.devices.delete.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
