"use client";

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";
import { NewCampaignDialog } from "@/components/new-campaign-dialog";
import { CampaignDetailSheet } from "@/components/campaign-detail-sheet";

interface Campaign {
  id: number;
  state: "draft" | "running" | "paused" | "completed" | "failed";
  total: number;
  sent: number;
  failed: number;
  created_at: string;
}

const STATE: Record<Campaign["state"], { tone: StatusTone; label: string }> = {
  draft: { tone: "neutral", label: "草稿" },
  running: { tone: "positive", label: "进行中" },
  paused: { tone: "warning", label: "已暂停" },
  completed: { tone: "positive", label: "已完成" },
  failed: { tone: "negative", label: "失败" },
};

const nf = new Intl.NumberFormat("en-US");
const fmtTime = (s: string) => (s ? s.slice(0, 19).replace("T", " ") : "—");

export function CustomerCampaigns() {
  const [rows, setRows] = useState<Campaign[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [detailId, setDetailId] = useState<number | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await api.get<Campaign[]>("/campaigns"));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  return (
    <>
      <div className="flex items-end justify-between gap-4">
        <div>
          <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
            Campaigns
          </div>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">群发任务</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            点击任意任务查看逐号触达明细与转化漏斗。
          </p>
        </div>
        <NewCampaignDialog onDone={load} />
      </div>

      {error ? (
        <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>
      ) : !rows ? (
        <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
      ) : (
        <Card className="overflow-hidden p-0">
          <Table>
            <TableHeader>
              <TableRow className="bg-muted/40">
                <TableHead>任务</TableHead>
                <TableHead>状态</TableHead>
                <TableHead className="text-right">总数</TableHead>
                <TableHead className="text-right">已发</TableHead>
                <TableHead className="text-right">失败</TableHead>
                <TableHead className="text-right">创建时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.length === 0 ? (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={6} className="py-12 text-center text-sm text-muted-foreground">
                    还没有群发任务。点击右上角「新建群发」开始。
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((c) => {
                  const st = STATE[c.state];
                  return (
                    <TableRow key={c.id} className="cursor-pointer" onClick={() => setDetailId(c.id)}>
                      <TableCell className="font-mono text-sm">#{c.id}</TableCell>
                      <TableCell>
                        <StatusBadge tone={st.tone}>{st.label}</StatusBadge>
                      </TableCell>
                      <TableCell className="text-right font-mono tabular-nums text-sm">{nf.format(c.total)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums text-sm">{nf.format(c.sent)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums text-sm text-muted-foreground">
                        {nf.format(c.failed)}
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs text-muted-foreground">
                        {fmtTime(c.created_at)}
                      </TableCell>
                    </TableRow>
                  );
                })
              )}
            </TableBody>
          </Table>
        </Card>
      )}

      <CampaignDetailSheet
        campaignId={detailId}
        apiBase="/campaigns"
        onClose={() => setDetailId(null)}
      />
    </>
  );
}
