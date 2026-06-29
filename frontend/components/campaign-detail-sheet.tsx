"use client";

import { useCallback, useEffect, useState } from "react";
import { ChevronLeft, ChevronRight, ArrowRight, Send, CheckCheck, Eye, Users } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { cn } from "@/lib/utils";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

interface RecipientDetail {
  id: number;
  phone: string;
  status: "queued" | "sent" | "delivered" | "read" | "failed" | "skipped";
  error_reason: string | null;
  error_detail: string | null;
  sent_at: string | null;
  delivered_at: string | null;
  read_at: string | null;
  updated_at: string;
}

interface RecipientsResponse {
  campaign_id: number;
  total: number;
  counts: Record<string, number>;
  page: number;
  page_size: number;
  recipients: RecipientDetail[];
}

const STATUS: Record<RecipientDetail["status"], { tone: StatusTone; label: string }> = {
  queued: { tone: "neutral", label: "排队中" },
  sent: { tone: "positive", label: "已发送" },
  delivered: { tone: "positive", label: "已送达" },
  read: { tone: "positive", label: "已读" },
  failed: { tone: "negative", label: "失败" },
  skipped: { tone: "warning", label: "已跳过" },
};

const REASON_LABEL: Record<string, string> = {
  device_banned: "设备被封",
  number_invalid: "号码无效",
  billing_error: "计费失败",
  rate_limited: "限速拦截",
  media_error: "媒体错误",
  unknown: "未知错误",
};

const PAGE_SIZE = 50;
const pct = (n: number, total: number) => (total > 0 ? `${((n / total) * 100).toFixed(1)}%` : "—");
const fmtTime = (s: string | null) => (s ? s.slice(0, 19).replace("T", " ") : "—");

/** A reusable drill-through drawer. `apiBase` is "/campaigns" (customer) or
 *  "/admin/campaigns" (admin) — the only difference between the two surfaces. */
export function CampaignDetailSheet({
  campaignId,
  apiBase,
  onClose,
}: {
  campaignId: number | null;
  apiBase: string;
  onClose: () => void;
}) {
  const [data, setData] = useState<RecipientsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [page, setPage] = useState(1);

  // Reset to page 1 whenever a different campaign is opened.
  useEffect(() => {
    setPage(1);
    setData(null);
    setError(null);
  }, [campaignId]);

  const load = useCallback(async () => {
    if (campaignId == null) return;
    setLoading(true);
    try {
      const res = await api.get<RecipientsResponse>(
        `${apiBase}/${campaignId}/recipients?page=${page}&page_size=${PAGE_SIZE}`,
      );
      setData(res);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    } finally {
      setLoading(false);
    }
  }, [apiBase, campaignId, page]);
  useEffect(() => {
    load();
  }, [load]);

  const total = data?.total ?? 0;
  const counts = data?.counts ?? {};
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <Sheet open={campaignId != null} onOpenChange={(o) => !o && onClose()}>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>任务 #{campaignId} · 触达明细</SheetTitle>
          <SheetDescription>
            逐号触达穿透。「已发送 → 已送达 → 已读」为递进里程碑;送达 / 已读需节点开启
            WhatsApp 回执采集后填充,未开启时这两级保持为 0。
          </SheetDescription>
        </SheetHeader>

        {/* Funnel: submitted → sent → delivered → read */}
        <div className="flex flex-wrap items-stretch gap-2">
          <FunnelTile accent="brand" icon={Users} label="提交总数" value={total} sub="recipients" />
          <FunnelArrow />
          <FunnelTile
            accent="emerald"
            icon={Send}
            label="已发送"
            value={counts.sent ?? 0}
            sub={pct(counts.sent ?? 0, total)}
          />
          <FunnelArrow />
          <FunnelTile
            accent="blue"
            icon={CheckCheck}
            label="已送达"
            value={counts.delivered ?? 0}
            sub={pct(counts.delivered ?? 0, total)}
          />
          <FunnelArrow />
          <FunnelTile
            accent="violet"
            icon={Eye}
            label="已读"
            value={counts.read ?? 0}
            sub={pct(counts.read ?? 0, total)}
          />
        </div>
        <div className="-mt-2 flex flex-wrap gap-4 font-mono text-xs text-muted-foreground">
          <span className="text-rose-600 dark:text-rose-400">失败 {counts.failed ?? 0}</span>
          <span>排队中 {counts.queued ?? 0}</span>
          <span>已跳过 {counts.skipped ?? 0}</span>
        </div>

        {/* Detail table */}
        <div className="min-h-0 flex-1 overflow-y-auto rounded-lg border">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-muted/60 backdrop-blur">
              <TableRow>
                <TableHead className="h-9">手机号</TableHead>
                <TableHead className="h-9">状态</TableHead>
                <TableHead className="h-9">失败原因</TableHead>
                <TableHead className="h-9 text-right">处理时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {error ? (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={4} className="py-12 text-center text-sm text-muted-foreground">
                    加载失败:{error}
                  </TableCell>
                </TableRow>
              ) : loading && !data ? (
                Array.from({ length: 8 }).map((_, i) => (
                  <TableRow key={i} className="hover:bg-transparent">
                    <TableCell colSpan={4} className="py-2">
                      <div className="h-5 animate-pulse rounded bg-muted motion-reduce:animate-none" />
                    </TableCell>
                  </TableRow>
                ))
              ) : !data || data.recipients.length === 0 ? (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={4} className="py-12 text-center text-sm text-muted-foreground">
                    该任务暂无接收者记录。
                  </TableCell>
                </TableRow>
              ) : (
                data.recipients.map((r) => {
                  const st = STATUS[r.status];
                  return (
                    <TableRow key={r.id}>
                      <TableCell className="font-mono text-xs">{r.phone}</TableCell>
                      <TableCell>
                        <StatusBadge tone={st.tone}>{st.label}</StatusBadge>
                      </TableCell>
                      <TableCell>
                        {r.error_reason ? (
                          <span
                            className="font-mono text-xs text-rose-600 dark:text-rose-400"
                            title={r.error_detail ?? undefined}
                          >
                            {REASON_LABEL[r.error_reason] ?? r.error_reason}
                          </span>
                        ) : (
                          <span className="text-xs text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs text-muted-foreground">
                        {fmtTime(r.read_at ?? r.delivered_at ?? r.sent_at ?? r.updated_at)}
                      </TableCell>
                    </TableRow>
                  );
                })
              )}
            </TableBody>
          </Table>
        </div>

        {/* Pagination footer */}
        <div className="flex shrink-0 items-center justify-between gap-3">
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {total === 0
              ? "0 条"
              : `${(page - 1) * PAGE_SIZE + 1}–${Math.min(page * PAGE_SIZE, total)} / ${total} 条`}
          </span>
          <div className="flex items-center gap-1">
            <button
              type="button"
              aria-label="上一页"
              disabled={page <= 1 || loading}
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              className="flex size-8 items-center justify-center rounded-md border text-muted-foreground transition-colors hover:bg-muted disabled:opacity-40"
            >
              <ChevronLeft className="size-4" />
            </button>
            <span className="px-1 font-mono text-xs tabular-nums text-muted-foreground">
              {page} / {pageCount}
            </span>
            <button
              type="button"
              aria-label="下一页"
              disabled={page >= pageCount || loading}
              onClick={() => setPage((p) => Math.min(pageCount, p + 1))}
              className="flex size-8 items-center justify-center rounded-md border text-muted-foreground transition-colors hover:bg-muted disabled:opacity-40"
            >
              <ChevronRight className="size-4" />
            </button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}

const TILE_ACCENT: Record<string, string> = {
  brand: "bg-brand-500/10 text-brand-600 dark:text-brand-400",
  emerald: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
  blue: "bg-blue-500/10 text-blue-600 dark:text-blue-400",
  violet: "bg-violet-500/10 text-violet-600 dark:text-violet-400",
  rose: "bg-rose-500/10 text-rose-600 dark:text-rose-400",
};

function FunnelTile({
  accent,
  icon: Icon,
  label,
  value,
  sub,
}: {
  accent: "brand" | "emerald" | "blue" | "violet" | "rose";
  icon: typeof Users;
  label: string;
  value: number;
  sub: string;
}) {
  return (
    <div className="flex min-w-[8rem] flex-1 items-center gap-3 rounded-xl border bg-card p-3">
      <span className={cn("flex size-9 shrink-0 items-center justify-center rounded-lg", TILE_ACCENT[accent])}>
        <Icon className="size-4" strokeWidth={1.9} />
      </span>
      <div className="min-w-0">
        <div className="font-mono text-xl font-semibold tabular-nums leading-none">{value.toLocaleString()}</div>
        <div className="mt-1 truncate text-xs text-muted-foreground">
          {label} · <span className="font-mono">{sub}</span>
        </div>
      </div>
    </div>
  );
}

function FunnelArrow() {
  return (
    <div className="flex items-center self-center text-muted-foreground/40">
      <ArrowRight className="size-4" />
    </div>
  );
}
