"use client";

import { useCallback, useEffect, useState } from "react";
import { Ban, Play, ShieldAlert, Radio, Pause, Zap, ListChecks } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { BulkActionDialog } from "@/components/admin/bulk-action-dialog";
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
import { useT } from "@/components/locale-provider";

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

// Filter tab keys → dict label key. Resolved via t() at render time so the
// module-level array itself carries no locale-specific text.
const STATE_TABS: { key: string; labelKey: string }[] = [
  { key: "", labelKey: "admin.campaigns.stateTab.all" },
  { key: "running", labelKey: "admin.campaigns.stateTab.running" },
  { key: "paused", labelKey: "admin.campaigns.stateTab.paused" },
  { key: "completed", labelKey: "admin.campaigns.stateTab.completed" },
  { key: "failed", labelKey: "admin.campaigns.stateTab.failed" },
];

export function AdminCampaigns() {
  const t = useT();
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
  const [sel, setSel] = useState<Set<string | number>>(new Set());
  const [bulkStopKeys, setBulkStopKeys] = useState<Array<string | number> | null>(null);
  const [bulkResumeKeys, setBulkResumeKeys] = useState<Array<string | number> | null>(null);

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
        setError(e instanceof ApiError ? e.message : t("admin.campaigns.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, state, t]);

  useEffect(() => {
    load();
  }, [load]);

  async function confirmKill() {
    if (!killTarget) return;
    setBusy(true);
    try {
      await api.post(`/admin/campaigns/${killTarget.id}/stop`);
      toast.success(t("admin.campaigns.pausedToastTitle"), {
        description: t("admin.campaigns.pausedToastDesc").replace("{id}", () => String(killTarget.id)),
      });
      setKillTarget(null);
      load();
    } catch (e) {
      toast.error(t("admin.campaigns.opFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.campaigns.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  async function resume(c: Campaign) {
    setResumingId(c.id);
    try {
      await api.post(`/admin/campaigns/${c.id}/resume`);
      toast.success(t("admin.campaigns.resumedToastTitle"), {
        description: t("admin.campaigns.resumedToastDesc").replace("{id}", () => String(c.id)),
      });
      load();
    } catch (e) {
      toast.error(t("admin.campaigns.resumeFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.campaigns.retry"),
      });
    } finally {
      setResumingId(null);
    }
  }

  // Bulk stop/resume loop the same single-item endpoints the row actions use.
  // No client-side state prefilter: the table is server-paginated, so `rows`
  // only ever holds the current page and can't tell whether a selection made
  // on an earlier page is still 'running'/'paused'. The server already
  // enforces state (409 on a no-op stop/resume) and each item's outcome is
  // reported honestly in the aggregated toast, so a stale selection just
  // shows up as a failure rather than silently doing nothing.
  async function submitBulkStop() {
    if (!bulkStopKeys) return;
    let okCount = 0;
    let failCount = 0;
    for (const k of bulkStopKeys) {
      try {
        await api.post(`/admin/campaigns/${k}/stop`);
        okCount++;
      } catch {
        failCount++;
      }
    }
    const desc = t("admin.campaigns.bulk.resultDesc")
      .replace("{ok}", () => String(okCount))
      .replace("{fail}", () => String(failCount));
    if (failCount === 0) {
      toast.success(t("admin.campaigns.bulk.resultTitle"), { description: desc });
    } else {
      toast.error(t("admin.campaigns.bulk.resultTitle"), { description: desc });
    }
    setBulkStopKeys(null);
    setSel(new Set());
    load();
  }

  async function submitBulkResume() {
    if (!bulkResumeKeys) return;
    let okCount = 0;
    let failCount = 0;
    for (const k of bulkResumeKeys) {
      try {
        await api.post(`/admin/campaigns/${k}/resume`);
        okCount++;
      } catch {
        failCount++;
      }
    }
    const desc = t("admin.campaigns.bulk.resultDesc")
      .replace("{ok}", () => String(okCount))
      .replace("{fail}", () => String(failCount));
    if (failCount === 0) {
      toast.success(t("admin.campaigns.bulk.resultTitle"), { description: desc });
    } else {
      toast.error(t("admin.campaigns.bulk.resultTitle"), { description: desc });
    }
    setBulkResumeKeys(null);
    setSel(new Set());
    load();
  }

  const tenantLabel = (c: Campaign) => c.tenant_name ?? `#${c.tenant_id}`;

  const columns: Column<Campaign>[] = [
    {
      key: "id",
      header: t("admin.campaigns.col.task"),
      title: t("admin.campaigns.col.task"),
      hideable: false,
      cell: (c) => <span className="font-mono text-sm">#{c.id}</span>,
    },
    {
      key: "tenant",
      header: t("admin.campaigns.col.tenant"),
      title: t("admin.campaigns.col.tenant"),
      cell: (c) => <span className="text-sm text-muted-foreground">{tenantLabel(c)}</span>,
    },
    {
      key: "state",
      header: t("admin.campaigns.col.state"),
      title: t("admin.campaigns.col.state"),
      cell: (c) => (
        <div className="flex items-center gap-1.5">
          <Badge variant={stateVariant[c.state]}>{c.state}</Badge>
          {c.auto_tripped && (
            <Badge variant="destructive" className="gap-1" title={t("admin.campaigns.autoTrippedTooltip")}>
              <ShieldAlert className="size-3" />
              {t("admin.campaigns.autoTrippedBadge")}
            </Badge>
          )}
        </div>
      ),
    },
    {
      key: "total",
      header: t("admin.campaigns.col.total"),
      title: t("admin.campaigns.col.total"),
      align: "right",
      cell: (c) => <span className="font-mono tabular-nums text-sm">{nf.format(c.total)}</span>,
    },
    {
      key: "sent",
      header: t("admin.campaigns.col.sent"),
      title: t("admin.campaigns.col.sent"),
      align: "right",
      cell: (c) => <span className="font-mono tabular-nums text-sm">{nf.format(c.sent)}</span>,
    },
    {
      key: "failed",
      header: t("admin.campaigns.col.failed"),
      title: t("admin.campaigns.col.failed"),
      align: "right",
      cell: (c) => <span className="font-mono tabular-nums text-sm text-muted-foreground">{nf.format(c.failed)}</span>,
    },
  ];

  return (
    <div className="space-y-6">
      {stats && (
        <MetricCardGroup>
          <StatCard
            accent="brand"
            label={t("admin.campaigns.stat.totalLabel")}
            value={String(stats.total)}
            sub={t("admin.campaigns.stat.totalSub")}
            icon={ListChecks}
          />
          <StatCard
            accent="emerald"
            label={t("admin.campaigns.stat.runningLabel")}
            value={String(stats.running)}
            sub={t("admin.campaigns.stat.runningSub")}
            icon={Radio}
          />
          <StatCard
            accent="amber"
            label={t("admin.campaigns.stat.pausedLabel")}
            value={String(stats.paused)}
            sub={t("admin.campaigns.stat.pausedSub")}
            icon={Pause}
          />
          <StatCard
            accent="rose"
            label={t("admin.campaigns.stat.trippedLabel")}
            value={String(stats.tripped)}
            sub={t("admin.campaigns.stat.trippedSub")}
            icon={Zap}
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
        getRowKey={(c) => c.id}
        storageKey="admin-campaigns"
        onRowClick={(c) => setDetailId(c.id)}
        emptyState={t("admin.campaigns.emptyState")}
        search={{ placeholder: t("admin.campaigns.searchPlaceholder"), accessor: () => "" }}
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
        selection={{
          selected: sel,
          onChange: setSel,
          actions: (keys) => (
            <>
              <Button variant="outline" size="sm" className="gap-1.5" onClick={() => setBulkResumeKeys(keys)}>
                <Play className="size-3.5" />
                {t("admin.campaigns.bulk.resume")}
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="gap-1.5 text-destructive hover:text-destructive"
                onClick={() => setBulkStopKeys(keys)}
              >
                <Ban className="size-3.5" />
                {t("admin.campaigns.bulk.stop")}
              </Button>
            </>
          ),
        }}
        rowActions={(c) =>
          c.state === "paused" ? (
            <Button variant="ghost" size="sm" className="gap-1.5" disabled={resumingId === c.id} onClick={() => resume(c)}>
              <Play className="size-3.5" />
              {resumingId === c.id ? t("admin.campaigns.resumingButton") : t("admin.campaigns.resumeButton")}
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
              {t("admin.campaigns.forceStopButton")}
            </Button>
          )
        }
      />

      <Dialog open={killTarget != null} onOpenChange={(o) => !o && setKillTarget(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("admin.campaigns.killDialog.title")}</DialogTitle>
            <DialogDescription>
              {t("admin.campaigns.killDialog.descPrefix")}
              <span className="font-mono">#{killTarget?.id}</span>
              {t("admin.campaigns.killDialog.descMiddle")}
              {killTarget ? tenantLabel(killTarget) : ""}
              {t("admin.campaigns.killDialog.descSuffix")}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose render={<Button variant="ghost" />}>{t("admin.campaigns.cancel")}</DialogClose>
            <Button variant="destructive" onClick={confirmKill} disabled={busy}>
              {busy ? t("admin.campaigns.processingButton") : t("admin.campaigns.confirmStopButton")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <CampaignDetailSheet campaignId={detailId} apiBase="/admin/campaigns" onClose={() => setDetailId(null)} />

      <BulkActionDialog
        open={bulkStopKeys != null}
        onOpenChange={(o) => !o && setBulkStopKeys(null)}
        title={t("admin.campaigns.bulkStopDialog.title")}
        description={t("admin.campaigns.bulkStopDialog.desc").replace("{n}", () => String(bulkStopKeys?.length ?? 0))}
        confirmLabel={t("admin.campaigns.bulk.confirm")}
        destructive
        onConfirm={submitBulkStop}
      />
      <BulkActionDialog
        open={bulkResumeKeys != null}
        onOpenChange={(o) => !o && setBulkResumeKeys(null)}
        title={t("admin.campaigns.bulkResumeDialog.title")}
        description={t("admin.campaigns.bulkResumeDialog.desc").replace("{n}", () => String(bulkResumeKeys?.length ?? 0))}
        confirmLabel={t("admin.campaigns.bulk.confirm")}
        onConfirm={submitBulkResume}
      />
    </div>
  );
}
