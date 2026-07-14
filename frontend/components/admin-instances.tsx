"use client";

import { useCallback, useEffect, useState } from "react";
import {
  MoreHorizontal,
  RotateCw,
  LogOut,
  Trash2,
  QrCode,
  Wifi,
  Unplug,
  Smartphone,
} from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";
import { StatCard, MetricCardGroup, type Accent } from "@/components/admin/stat-card";
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
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { useT } from "@/components/locale-provider";
import { AdminInstanceWizard } from "@/components/admin-instance-wizard";

type InstanceState = "created" | "qr" | "connected" | "disconnected" | "loggedOut";

interface InstanceRow {
  instance_name: string;
  jid: string | null;
  tenant_id: number;
  tenant_name: string;
  evo_node: string;
  proxy_id: number | null;
  state: InstanceState;
  updated_at: string;
}

interface NodeRow {
  node: string;
  count: number;
  cap: number;
  pct: number;
}

type InstanceStats = Partial<Record<InstanceState, number>>;

const PAGE_SIZE = 20;

const STATE_TABS: { key: "" | InstanceState; labelKey: string }[] = [
  { key: "", labelKey: "admin.instances.state.all" },
  { key: "created", labelKey: "admin.instances.state.created" },
  { key: "qr", labelKey: "admin.instances.state.qr" },
  { key: "connected", labelKey: "admin.instances.state.connected" },
  { key: "disconnected", labelKey: "admin.instances.state.disconnected" },
  { key: "loggedOut", labelKey: "admin.instances.state.loggedOut" },
];

const STATE_TONE: Record<InstanceState, StatusTone> = {
  created: "neutral",
  qr: "warning",
  connected: "positive",
  disconnected: "negative",
  loggedOut: "neutral",
};
const STATE_ACCENT: Record<InstanceState, Accent> = {
  created: "neutral",
  qr: "amber",
  connected: "emerald",
  disconnected: "rose",
  loggedOut: "neutral",
};
const STATE_LABEL_KEY: Record<InstanceState, string> = {
  created: "admin.instances.state.created",
  qr: "admin.instances.state.qr",
  connected: "admin.instances.state.connected",
  disconnected: "admin.instances.state.disconnected",
  loggedOut: "admin.instances.state.loggedOut",
};

type BulkKind = "logout" | "delete";
interface BulkTarget {
  kind: BulkKind;
  keys: Array<string | number>;
}

export function AdminInstances() {
  const t = useT();
  const [rows, setRows] = useState<InstanceRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<InstanceStats | null>(null);
  const [nodes, setNodes] = useState<NodeRow[] | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [state, setState] = useState<"" | InstanceState>("");
  const [tenantId, setTenantId] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<Set<string | number>>(new Set());

  const [logoutTarget, setLogoutTarget] = useState<InstanceRow | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<InstanceRow | null>(null);
  const [bulkTarget, setBulkTarget] = useState<BulkTarget | null>(null);
  const [wizardOpen, setWizardOpen] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (state) params.set("state", state);
    if (tenantId.trim()) params.set("tenant_id", tenantId.trim());
    try {
      const d = await api.get<{ rows: InstanceRow[]; total: number; stats: InstanceStats }>(
        `/admin/instances?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setStats(d.stats);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.instances.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, state, tenantId, t]);

  const loadNodes = useCallback(async () => {
    try {
      const d = await api.get<NodeRow[]>("/admin/nodes");
      setNodes(d);
    } catch {
      // 节点容量卡是辅助信息,加载失败不影响主列表——静默跳过。
    }
  }, []);

  // Any mutation (reconnect/logout/delete, single or bulk) can move an
  // instance's node/state, so refresh both the list and the capacity cards.
  const refresh = useCallback(() => {
    load();
    loadNodes();
  }, [load, loadNodes]);

  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    loadNodes();
  }, [loadNodes]);

  async function handleReconnect(r: InstanceRow) {
    try {
      // QR 展示留给 T7 扫码向导;这里只触发重连请求并刷新状态。
      await api.post(`/admin/instances/${r.instance_name}/reconnect`);
      toast.success(t("admin.instances.reconnect.successTitle"), { description: r.jid ?? r.instance_name });
      refresh();
    } catch (e) {
      toast.error(t("admin.instances.reconnect.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.instances.retry"),
      });
    }
  }

  const tenantLabel = (r: InstanceRow) => r.tenant_name || `#${r.tenant_id}`;
  const s = (k: InstanceState) => stats?.[k] ?? 0;
  const statsTotal = stats ? Object.values(stats).reduce((a, b) => a + (b ?? 0), 0) : 0;

  const columns: Column<InstanceRow>[] = [
    {
      key: "number",
      header: t("admin.instances.col.number"),
      cell: (r) => (
        <div className="flex items-center gap-2.5">
          <RowAvatar icon={Smartphone} accent={STATE_ACCENT[r.state]} />
          <div className="flex flex-col">
            <span className="font-mono text-xs">{r.jid ?? "—"}</span>
            <span className="font-mono text-[10px] text-muted-foreground">{r.instance_name}</span>
          </div>
        </div>
      ),
    },
    {
      key: "tenant",
      header: t("admin.instances.col.tenant"),
      cell: (r) => <span className="text-sm text-muted-foreground">{tenantLabel(r)}</span>,
    },
    {
      key: "node",
      header: t("admin.instances.col.node"),
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.evo_node}</span>,
    },
    {
      key: "proxy",
      header: t("admin.instances.col.proxy"),
      cell: (r) => (
        <span className="font-mono text-xs text-muted-foreground">{r.proxy_id != null ? `#${r.proxy_id}` : "—"}</span>
      ),
    },
    {
      key: "state",
      header: t("admin.instances.col.state"),
      cell: (r) => <StatusBadge tone={STATE_TONE[r.state]}>{t(STATE_LABEL_KEY[r.state])}</StatusBadge>,
    },
    {
      key: "updated_at",
      header: t("admin.instances.col.updatedAt"),
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.updated_at}</span>,
    },
  ];

  return (
    <div className="space-y-6">
      <NodeCapacityRow nodes={nodes} />

      {stats && (
        <MetricCardGroup columns={3}>
          <StatCard
            accent="brand"
            label={t("admin.instances.stat.totalLabel")}
            value={String(statsTotal)}
            sub={t("admin.instances.stat.totalSub")}
            icon={Smartphone}
          />
          <StatCard
            accent="emerald"
            label={t(STATE_LABEL_KEY.connected)}
            value={String(s("connected"))}
            sub={t("admin.instances.stat.connectedSub")}
            icon={Wifi}
          />
          <StatCard
            accent="amber"
            label={t(STATE_LABEL_KEY.qr)}
            value={String(s("qr"))}
            sub={t("admin.instances.stat.qrSub")}
            icon={QrCode}
          />
          <StatCard
            accent="rose"
            label={t(STATE_LABEL_KEY.disconnected)}
            value={String(s("disconnected"))}
            sub={t("admin.instances.stat.disconnectedSub")}
            icon={Unplug}
          />
          <StatCard
            accent="neutral"
            label={t(STATE_LABEL_KEY.loggedOut)}
            value={String(s("loggedOut"))}
            sub={t("admin.instances.stat.loggedOutSub")}
            icon={LogOut}
          />
          <StatCard
            accent="neutral"
            label={t(STATE_LABEL_KEY.created)}
            value={String(s("created"))}
            sub={t("admin.instances.stat.createdSub")}
            icon={Smartphone}
          />
        </MetricCardGroup>
      )}

      <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
        {STATE_TABS.map((tab) => (
          <button
            key={tab.key || "all"}
            onClick={() => {
              setState(tab.key);
              setPage(0);
            }}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (state === tab.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
            }
          >
            {t(tab.labelKey)}
          </button>
        ))}
      </div>

      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(r) => r.instance_name}
        emptyState={t("admin.instances.emptyState")}
        search={{ placeholder: t("admin.instances.searchPlaceholder"), accessor: () => "" }}
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query,
          onQueryChange: (q) => {
            setQuery(q);
            setPage(0);
          },
          loading,
        }}
        toolbar={
          <div className="flex items-center gap-2">
            <Input
              value={tenantId}
              onChange={(e) => {
                setTenantId(e.target.value);
                setPage(0);
              }}
              placeholder={t("admin.instances.tenantIdPlaceholder")}
              className="h-8 w-24 font-mono text-sm"
              aria-label={t("admin.instances.filterByTenantAria")}
            />
            <Button className="gap-2" onClick={() => setWizardOpen(true)}>
              <QrCode className="size-4" />
              {t("admin.instances.connectNew")}
            </Button>
          </div>
        }
        selection={{
          selected,
          onChange: setSelected,
          actions: (keys) => (
            <>
              <Button
                variant="outline"
                size="sm"
                className="gap-1.5"
                onClick={() => setBulkTarget({ kind: "logout", keys })}
              >
                <LogOut className="size-3.5" />
                {t("admin.instances.bulk.logout")}
              </Button>
              <Button
                variant="destructive"
                size="sm"
                className="gap-1.5"
                onClick={() => setBulkTarget({ kind: "delete", keys })}
              >
                <Trash2 className="size-3.5" />
                {t("admin.instances.bulk.delete")}
              </Button>
            </>
          ),
        }}
        rowActions={(r) => (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={<Button variant="ghost" size="icon-sm" aria-label={t("admin.instances.actionsAria")} />}
            >
              <MoreHorizontal className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => handleReconnect(r)}>
                <RotateCw className="size-4" />
                {t("admin.instances.action.reconnect")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setLogoutTarget(r)}>
                <LogOut className="size-4" />
                {t("admin.instances.action.logout")}
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onClick={() => setDeleteTarget(r)}>
                <Trash2 className="size-4" />
                {t("admin.instances.action.delete")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      />

      <AdminInstanceWizard open={wizardOpen} onOpenChange={setWizardOpen} onSuccess={refresh} />
      <LogoutInstanceDialog target={logoutTarget} onClose={() => setLogoutTarget(null)} onDone={refresh} />
      <DeleteInstanceDialog target={deleteTarget} onClose={() => setDeleteTarget(null)} onDone={refresh} />
      <BulkConfirmDialog
        target={bulkTarget}
        onClose={() => setBulkTarget(null)}
        onDone={() => {
          setSelected(new Set());
          refresh();
        }}
      />
    </div>
  );
}

function NodeCapacityRow({ nodes }: { nodes: NodeRow[] | null }) {
  const t = useT();
  if (!nodes || nodes.length === 0) return null;
  return (
    <div>
      <div className="mb-2 font-mono text-[11px] uppercase tracking-wider text-muted-foreground">
        {t("admin.instances.nodes.title")}
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {nodes.map((n) => (
          <NodeCapacityCard key={n.node} n={n} />
        ))}
      </div>
    </div>
  );
}

function NodeCapacityCard({ n }: { n: NodeRow }) {
  const hot = n.pct >= 90;
  const barWidth = Math.min(100, Math.max(0, n.pct));
  return (
    <div className="rounded-2xl border border-border/70 bg-card p-4 shadow-sm ring-1 ring-foreground/5">
      <div className="flex items-center justify-between gap-2">
        <span className="truncate font-mono text-sm font-medium">{n.node}</span>
        <span
          className={cn(
            "shrink-0 font-mono text-xs tabular-nums",
            hot ? "text-destructive" : "text-muted-foreground",
          )}
        >
          {n.count}/{n.cap > 0 ? n.cap : "∞"}
        </span>
      </div>
      <div className="mt-2.5 h-1.5 w-full overflow-hidden rounded-full bg-muted">
        <div
          className={cn("h-full rounded-full transition-all", hot ? "bg-destructive" : "bg-brand-500")}
          style={{ width: `${barWidth}%` }}
        />
      </div>
      <div className={cn("mt-1 font-mono text-[11px] tabular-nums", hot ? "text-destructive" : "text-muted-foreground")}>
        {n.pct.toFixed(1)}%
      </div>
    </div>
  );
}

function LogoutInstanceDialog({
  target,
  onClose,
  onDone,
}: {
  target: InstanceRow | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.post(`/admin/instances/${target.instance_name}/logout`);
      toast.success(t("admin.instances.logout.successTitle"), { description: target.jid ?? target.instance_name });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.instances.logout.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.instances.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.instances.logoutDialog.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.instances.logoutDialog.descPrefix")}
            <span className="font-mono text-xs">{target?.jid ?? target?.instance_name}</span>
            {t("admin.instances.logoutDialog.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.instances.cancel")}</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? t("admin.instances.logoutDialog.loggingOut") : t("admin.instances.logoutDialog.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeleteInstanceDialog({
  target,
  onClose,
  onDone,
}: {
  target: InstanceRow | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.delete(`/admin/instances/${target.instance_name}`);
      toast.success(t("admin.instances.delete.successTitle"), { description: target.jid ?? target.instance_name });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.instances.delete.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.instances.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.instances.deleteDialog.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.instances.deleteDialog.descPrefix")}
            <span className="font-mono text-xs">{target?.jid ?? target?.instance_name}</span>
            {t("admin.instances.deleteDialog.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.instances.cancel")}</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? t("admin.instances.deleteDialog.deleting") : t("admin.instances.deleteDialog.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// BulkConfirmDialog drives both bulk logout and bulk delete: it calls the
// matching endpoint once per selected instance_name (sequentially — these are
// destructive/session-ending calls, not worth parallelizing against the
// Evolution cluster), then reports one aggregated toast instead of N.
function BulkConfirmDialog({
  target,
  onClose,
  onDone,
}: {
  target: BulkTarget | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target || target.keys.length === 0) return;
    setBusy(true);
    let okCount = 0;
    let failCount = 0;
    for (const key of target.keys) {
      try {
        if (target.kind === "logout") {
          await api.post(`/admin/instances/${key}/logout`);
        } else {
          await api.delete(`/admin/instances/${key}`);
        }
        okCount++;
      } catch {
        failCount++;
      }
    }
    setBusy(false);
    const desc = t("admin.instances.bulk.resultDesc")
      .replace("{ok}", () => String(okCount))
      .replace("{fail}", () => String(failCount));
    if (failCount === 0) {
      toast.success(t("admin.instances.bulk.resultTitle"), { description: desc });
    } else {
      toast.error(t("admin.instances.bulk.resultTitle"), { description: desc });
    }
    onClose();
    onDone();
  }

  const open = target != null && target.keys.length > 0;
  const titleKey =
    target?.kind === "logout" ? "admin.instances.bulkLogoutDialog.title" : "admin.instances.bulkDeleteDialog.title";
  const descKey =
    target?.kind === "logout" ? "admin.instances.bulkLogoutDialog.desc" : "admin.instances.bulkDeleteDialog.desc";

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(titleKey)}</DialogTitle>
          <DialogDescription>
            {t(descKey).replace("{n}", () => String(target?.keys.length ?? 0))}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.instances.cancel")}</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? t("admin.instances.bulk.processing") : t("admin.instances.bulk.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
