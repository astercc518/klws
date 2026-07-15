"use client";

import { useCallback, useEffect, useState } from "react";
import {
  MoreHorizontal,
  Pause,
  Play,
  ArrowUpCircle,
  ArrowDownCircle,
  Route,
  Sprout,
  Flame,
  BadgeCheck,
  PauseCircle,
  Settings2,
} from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";
import { StatCard, MetricCardGroup, type Accent } from "@/components/admin/stat-card";
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
import { useT } from "@/components/locale-provider";
import { PoliciesDialog } from "@/components/admin-warmup-policies";

export type Lane = "FAST" | "STANDARD";
type Stage = "NEW" | "WARMING" | "MATURE";

interface WarmupRow {
  account_jid: string;
  tenant_id: number;
  lane: Lane;
  stage: Stage;
  warmup_messages_sent: number;
  replies_received: number;
  online_hours: number;
  health: number;
  registered_at: string | null;
  business_quota_remaining: number;
  paused: boolean;
}

interface Overview {
  new: number;
  warming: number;
  mature: number;
  paused: number;
}

const PAGE_SIZE = 20;
export const LANES: Lane[] = ["FAST", "STANDARD"];

const STAGE_TABS: { key: "" | Stage; labelKey: string }[] = [
  { key: "", labelKey: "admin.warmup.stage.all" },
  { key: "NEW", labelKey: "admin.warmup.stage.new" },
  { key: "WARMING", labelKey: "admin.warmup.stage.warming" },
  { key: "MATURE", labelKey: "admin.warmup.stage.mature" },
];

const STAGE_TONE: Record<Stage, StatusTone> = {
  NEW: "neutral",
  WARMING: "warning",
  MATURE: "positive",
};
const STAGE_LABEL_KEY: Record<Stage, string> = {
  NEW: "admin.warmup.stage.new",
  WARMING: "admin.warmup.stage.warming",
  MATURE: "admin.warmup.stage.mature",
};
export const LANE_LABEL_KEY: Record<Lane, string> = {
  FAST: "admin.warmup.lane.fast",
  STANDARD: "admin.warmup.lane.standard",
};

function ageDays(registeredAt: string | null): number | null {
  if (!registeredAt) return null;
  const then = new Date(registeredAt).getTime();
  if (Number.isNaN(then)) return null;
  return Math.max(0, Math.floor((Date.now() - then) / 86400000));
}

export function AdminWarmup() {
  const t = useT();
  const [rows, setRows] = useState<WarmupRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [overview, setOverview] = useState<Overview | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [stage, setStage] = useState<"" | Stage>("");
  const [lane, setLane] = useState<"" | Lane>("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const [laneTarget, setLaneTarget] = useState<WarmupRow | null>(null);
  const [demoteTarget, setDemoteTarget] = useState<WarmupRow | null>(null);
  const [policiesOpen, setPoliciesOpen] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (stage) params.set("stage", stage);
    if (lane) params.set("lane", lane);
    try {
      const d = await api.get<{ rows: WarmupRow[]; total: number }>(`/admin/warmup?${params.toString()}`);
      setRows(d.rows ?? []);
      setTotal(d.total);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.warmup.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, stage, lane, t]);

  const loadOverview = useCallback(async () => {
    try {
      const d = await api.get<Overview>("/admin/warmup/overview");
      setOverview(d);
    } catch {
      // 总览卡是辅助信息,加载失败不影响主列表——静默跳过。
    }
  }, []);

  const refresh = useCallback(() => {
    load();
    loadOverview();
  }, [load, loadOverview]);

  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    loadOverview();
  }, [loadOverview]);

  async function runAction(r: WarmupRow, action: "pause" | "resume" | "promote" | "demote", laneValue?: Lane) {
    try {
      await api.post(`/admin/warmup/action?jid=${encodeURIComponent(r.account_jid)}`, {
        action,
        lane: laneValue,
      });
      toast.success(t(`admin.warmup.action.${action}SuccessTitle`), { description: r.account_jid });
      refresh();
    } catch (e) {
      toast.error(t(`admin.warmup.action.${action}FailedTitle`), {
        description: e instanceof ApiError ? e.message : t("admin.warmup.retry"),
      });
    }
  }

  async function runLaneChange(r: WarmupRow, laneValue: Lane) {
    try {
      await api.post(`/admin/warmup/action?jid=${encodeURIComponent(r.account_jid)}`, {
        action: "lane",
        lane: laneValue,
      });
      toast.success(t("admin.warmup.action.laneSuccessTitle"), { description: r.account_jid });
      setLaneTarget(null);
      refresh();
    } catch (e) {
      toast.error(t("admin.warmup.action.laneFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.warmup.retry"),
      });
    }
  }

  const columns: Column<WarmupRow>[] = [
    {
      key: "jid",
      header: t("admin.warmup.col.jid"),
      title: t("admin.warmup.col.jid"),
      hideable: false,
      cell: (r) => (
        <div className="flex flex-col">
          <span className="font-mono text-xs">{r.account_jid}</span>
          <span className="font-mono text-[10px] text-muted-foreground">#{r.tenant_id}</span>
        </div>
      ),
    },
    {
      key: "lane",
      header: t("admin.warmup.col.lane"),
      title: t("admin.warmup.col.lane"),
      cell: (r) => (
        <span className="inline-flex items-center gap-1 font-mono text-xs text-muted-foreground">
          {r.lane === "FAST" ? <Flame className="size-3.5" /> : <Sprout className="size-3.5" />}
          {t(LANE_LABEL_KEY[r.lane] ?? "")}
        </span>
      ),
    },
    {
      key: "stage",
      header: t("admin.warmup.col.stage"),
      title: t("admin.warmup.col.stage"),
      cell: (r) => {
        const labelKey = STAGE_LABEL_KEY[r.stage];
        return <StatusBadge tone={STAGE_TONE[r.stage] ?? "neutral"}>{labelKey ? t(labelKey) : r.stage}</StatusBadge>;
      },
    },
    {
      key: "age",
      header: t("admin.warmup.col.age"),
      title: t("admin.warmup.col.age"),
      cell: (r) => {
        const d = ageDays(r.registered_at);
        return (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {d == null ? "—" : t("admin.warmup.col.ageValue").replace("{n}", String(d))}
          </span>
        );
      },
    },
    {
      key: "health",
      header: t("admin.warmup.col.health"),
      title: t("admin.warmup.col.health"),
      cell: (r) => <span className="font-mono text-xs tabular-nums">{r.health}</span>,
    },
    {
      key: "progress",
      header: t("admin.warmup.col.progress"),
      title: t("admin.warmup.col.progress"),
      cell: (r) => (
        <span className="font-mono text-xs tabular-nums text-muted-foreground">
          {r.warmup_messages_sent}
          {t("admin.warmup.col.progressMsgsUnit")} · {r.replies_received}
          {t("admin.warmup.col.progressRepliesUnit")} · {r.online_hours.toFixed(1)}
          {t("admin.warmup.col.progressHoursUnit")}
        </span>
      ),
    },
    {
      key: "quota",
      header: t("admin.warmup.col.quota"),
      title: t("admin.warmup.col.quota"),
      cell: (r) => <span className="font-mono text-xs tabular-nums">{r.business_quota_remaining}</span>,
    },
    {
      key: "paused",
      header: t("admin.warmup.col.paused"),
      title: t("admin.warmup.col.paused"),
      cell: (r) =>
        r.paused ? (
          <StatusBadge tone="negative">{t("admin.warmup.col.pausedYes")}</StatusBadge>
        ) : (
          <StatusBadge tone="positive">{t("admin.warmup.col.pausedNo")}</StatusBadge>
        ),
    },
  ];

  return (
    <div className="space-y-6">
      {overview && (
        <MetricCardGroup columns={4}>
          <StatCard
            accent={"neutral" as Accent}
            label={t("admin.warmup.overview.new")}
            value={String(overview.new)}
            sub={t("admin.warmup.overview.newSub")}
            icon={Sprout}
          />
          <StatCard
            accent={"amber" as Accent}
            label={t("admin.warmup.overview.warming")}
            value={String(overview.warming)}
            sub={t("admin.warmup.overview.warmingSub")}
            icon={Flame}
          />
          <StatCard
            accent={"emerald" as Accent}
            label={t("admin.warmup.overview.mature")}
            value={String(overview.mature)}
            sub={t("admin.warmup.overview.matureSub")}
            icon={BadgeCheck}
          />
          <StatCard
            accent={"rose" as Accent}
            label={t("admin.warmup.overview.paused")}
            value={String(overview.paused)}
            sub={t("admin.warmup.overview.pausedSub")}
            icon={PauseCircle}
          />
        </MetricCardGroup>
      )}

      <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
        {STAGE_TABS.map((tab) => (
          <button
            key={tab.key || "all"}
            onClick={() => {
              setStage(tab.key);
              setPage(0);
            }}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (stage === tab.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
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
        getRowKey={(r) => r.account_jid}
        storageKey="admin-warmup"
        emptyState={t("admin.warmup.emptyState")}
        search={{ placeholder: t("admin.warmup.searchPlaceholder"), accessor: () => "" }}
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
            <select
              value={lane}
              onChange={(e) => {
                setLane(e.target.value as "" | Lane);
                setPage(0);
              }}
              className="h-8 rounded-md border bg-transparent px-2 text-sm"
              aria-label={t("admin.warmup.filterByLaneAria")}
            >
              <option value="">{t("admin.warmup.lane.all")}</option>
              <option value="FAST">{t("admin.warmup.lane.fast")}</option>
              <option value="STANDARD">{t("admin.warmup.lane.standard")}</option>
            </select>
            <Button variant="outline" className="gap-2" onClick={() => setPoliciesOpen(true)}>
              <Settings2 className="size-4" />
              {t("admin.warmup.policy.trigger")}
            </Button>
          </div>
        }
        rowActions={(r) => (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={<Button variant="ghost" size="icon-sm" aria-label={t("admin.warmup.actionsAria")} />}
            >
              <MoreHorizontal className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {r.paused ? (
                <DropdownMenuItem onClick={() => runAction(r, "resume")}>
                  <Play className="size-4" />
                  {t("admin.warmup.action.resume")}
                </DropdownMenuItem>
              ) : (
                <DropdownMenuItem onClick={() => runAction(r, "pause")}>
                  <Pause className="size-4" />
                  {t("admin.warmup.action.pause")}
                </DropdownMenuItem>
              )}
              <DropdownMenuItem onClick={() => runAction(r, "promote")}>
                <ArrowUpCircle className="size-4" />
                {t("admin.warmup.action.promote")}
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onClick={() => setDemoteTarget(r)}>
                <ArrowDownCircle className="size-4" />
                {t("admin.warmup.action.demote")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setLaneTarget(r)}>
                <Route className="size-4" />
                {t("admin.warmup.action.lane")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      />

      <LaneDialog target={laneTarget} onClose={() => setLaneTarget(null)} onPick={runLaneChange} />
      <DemoteDialog
        target={demoteTarget}
        onClose={() => setDemoteTarget(null)}
        onDone={() => {
          setDemoteTarget(null);
          refresh();
        }}
      />
      <PoliciesDialog open={policiesOpen} onOpenChange={setPoliciesOpen} />
    </div>
  );
}

function LaneDialog({
  target,
  onClose,
  onPick,
}: {
  target: WarmupRow | null;
  onClose: () => void;
  onPick: (r: WarmupRow, lane: Lane) => void;
}) {
  const t = useT();
  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.warmup.laneDialog.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.warmup.laneDialog.descPrefix")}
            <span className="font-mono text-xs">{target?.account_jid}</span>
          </DialogDescription>
        </DialogHeader>
        <div className="flex gap-2">
          {LANES.map((l) => (
            <Button
              key={l}
              variant={target?.lane === l ? "default" : "outline"}
              className="flex-1"
              onClick={() => target && onPick(target, l)}
            >
              {t(LANE_LABEL_KEY[l])}
            </Button>
          ))}
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.warmup.cancel")}</DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DemoteDialog({
  target,
  onClose,
  onDone,
}: {
  target: WarmupRow | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.post(`/admin/warmup/action?jid=${encodeURIComponent(target.account_jid)}`, { action: "demote" });
      toast.success(t("admin.warmup.action.demoteSuccessTitle"), { description: target.account_jid });
      onDone();
    } catch (e) {
      toast.error(t("admin.warmup.action.demoteFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.warmup.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.warmup.demoteDialog.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.warmup.demoteDialog.descPrefix")}
            <span className="font-mono text-xs">{target?.account_jid}</span>
            {t("admin.warmup.demoteDialog.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.warmup.cancel")}</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? t("admin.warmup.demoteDialog.demoting") : t("admin.warmup.demoteDialog.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
