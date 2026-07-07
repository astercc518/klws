"use client";

import { useCallback, useEffect, useState } from "react";
import { Ban, Play, ShieldAlert, Radio, Pause, Zap, ListChecks } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatCard, MetricCardGroup } from "@/components/admin/stat-card";
import { CampaignDetailSheet } from "@/components/campaign-detail-sheet";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

interface Campaign {
  id: number;
  tenant_id: number;
  tenant_name: string | null;
  state: "draft" | "running" | "paused" | "completed" | "failed";
  total: number;
  sent: number;
  failed: number;
  created_at: string;
  auto_tripped: boolean;
}
interface CampaignStats {
  running: number;
  paused: number;
  tripped: number;
  total: number;
}

const stateVariant: Record<Campaign["state"], "default" | "secondary" | "outline" | "destructive"> = {
  running: "secondary",
  paused: "outline",
  draft: "outline",
  completed: "default",
  failed: "destructive",
};
const nf = new Intl.NumberFormat("en-US");
const PAGE_SIZE = 10;

const STATE_TABS: { key: string; label: string }[] = [
  { key: "", label: "全部" },
  { key: "running", label: "运行中" },
  { key: "paused", label: "已暂停" },
  { key: "completed", label: "已完成" },
  { key: "failed", label: "失败" },
];

export function AdminCampaigns() {
  const [rows, setRows] = useState<Campaign[] | null>(null);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<CampaignStats | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [state, setState] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [killTarget, setKillTarget] = useState<Campaign | null>(null);
  const [busy, setBusy] = useState(false);
  const [resumingId, setResumingId] = useState<number | null>(null);
  const [detailId, setDetailId] = useState<number | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (state) params.set("state", state);
    try {
      const d = await api.get<{ rows: Campaign[]; total: number; stats: CampaignStats }>(
        `/admin/campaigns?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setStats(d.stats);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, state]);

  useEffect(() => {
    load();
  }, [load]);

  async function confirmKill() {
    if (!killTarget) return;
    setBusy(true);
    try {
      await api.post(`/admin/campaigns/${killTarget.id}/stop`);
      toast.success("已强制暂停", { description: `任务 #${killTarget.id} → paused` });
      setKillTarget(null);
      load();
    } catch (e) {
      toast.error("操作失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  async function resume(c: Campaign) {
    setResumingId(c.id);
    try {
      await api.post(`/admin/campaigns/${c.id}/resume`);
      toast.success("已恢复任务", { description: `任务 #${c.id} → running` });
      load();
    } catch (e) {
      toast.error("恢复失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setResumingId(null);
    }
  }

  const tenantLabel = (c: Campaign) => c.tenant_name ?? `#${c.tenant_id}`;

  const columns: Column<Campaign>[] = [
    { key: "id", header: "任务", cell: (c) => <span className="font-mono text-sm">#{c.id}</span> },
    { key: "tenant", header: "租户", cell: (c) => <span className="text-sm text-muted-foreground">{tenantLabel(c)}</span> },
    {
      key: "state",
      header: "状态",
      cell: (c) => (
        <div className="flex items-center gap-1.5">
          <Badge variant={stateVariant[c.state]}>{c.state}</Badge>
          {c.auto_tripped && (
            <Badge variant="destructive" className="gap-1" title="风控熔断器因封号率超阈值自动挂起了此任务">
              <ShieldAlert className="size-3" />
              自动熔断
            </Badge>
          )}
        </div>
      ),
    },
    { key: "total", header: "总数", align: "right", cell: (c) => <span className="font-mono tabular-nums text-sm">{nf.format(c.total)}</span> },
    { key: "sent", header: "已发", align: "right", cell: (c) => <span className="font-mono tabular-nums text-sm">{nf.format(c.sent)}</span> },
    { key: "failed", header: "失败", align: "right", cell: (c) => <span className="font-mono tabular-nums text-sm text-muted-foreground">{nf.format(c.failed)}</span> },
  ];

  return (
    <div className="space-y-6">
      {stats && (
        <MetricCardGroup>
          <StatCard accent="brand" label="任务总数" value={String(stats.total)} sub="全平台" icon={ListChecks} />
          <StatCard accent="emerald" label="运行中" value={String(stats.running)} sub="正在发送" icon={Radio} />
          <StatCard accent="amber" label="已暂停" value={String(stats.paused)} sub="含手动/熔断" icon={Pause} />
          <StatCard accent="rose" label="熔断挂起" value={String(stats.tripped)} sub="风控自动触发" icon={Zap} />
        </MetricCardGroup>
      )}

      <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
        {STATE_TABS.map((t) => (
          <button
            key={t.key || "all"}
            onClick={() => {
              setState(t.key);
              setPage(0);
            }}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (state === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
            }
          >
            {t.label}
          </button>
        ))}
      </div>

      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(c) => c.id}
        onRowClick={(c) => setDetailId(c.id)}
        emptyState="当前没有任务。"
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
        rowActions={(c) =>
          c.state === "paused" ? (
            <Button variant="ghost" size="sm" className="gap-1.5" disabled={resumingId === c.id} onClick={() => resume(c)}>
              <Play className="size-3.5" />
              {resumingId === c.id ? "恢复中…" : "恢复"}
            </Button>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              className="gap-1.5 text-destructive hover:text-destructive"
              disabled={c.state !== "running"}
              onClick={() => setKillTarget(c)}
            >
              <Ban className="size-3.5" />
              强制终止
            </Button>
          )
        }
      />

      <Dialog open={killTarget != null} onOpenChange={(o) => !o && setKillTarget(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>强制终止任务</DialogTitle>
            <DialogDescription>
              将任务 <span className="font-mono">#{killTarget?.id}</span>(租户 {killTarget ? tenantLabel(killTarget) : ""})置为 paused,
              调度器会立即停止继续发送。此操作不可在界面撤销。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
            <Button variant="destructive" onClick={confirmKill} disabled={busy}>
              {busy ? "处理中…" : "确认终止"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <CampaignDetailSheet campaignId={detailId} apiBase="/admin/campaigns" onClose={() => setDetailId(null)} />
    </div>
  );
}
