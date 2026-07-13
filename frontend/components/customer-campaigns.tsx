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
import { useT } from "@/components/locale-provider";

interface Campaign {
  id: number;
  state: "draft" | "running" | "paused" | "completed" | "failed";
  total: number;
  sent: number;
  failed: number;
  created_at: string;
}

const STATE: Record<Campaign["state"], { tone: StatusTone; key: string }> = {
  draft: { tone: "neutral", key: "dash.campaigns.state.draft" },
  running: { tone: "positive", key: "dash.campaigns.state.running" },
  paused: { tone: "warning", key: "dash.campaigns.state.paused" },
  completed: { tone: "positive", key: "dash.campaigns.state.completed" },
  failed: { tone: "negative", key: "dash.campaigns.state.failed" },
};

const nf = new Intl.NumberFormat("en-US");
const fmtTime = (s: string) => (s ? s.slice(0, 19).replace("T", " ") : "—");

export function CustomerCampaigns() {
  const t = useT();
  const [rows, setRows] = useState<Campaign[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [detailId, setDetailId] = useState<number | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await api.get<Campaign[]>("/campaigns"));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("dash.campaigns.loadFailed"));
      }
    }
  }, [t]);
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
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">{t("dash.campaigns.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("dash.campaigns.subtitle")}
          </p>
        </div>
        <NewCampaignDialog onDone={load} />
      </div>

      {error ? (
        <Card className="p-5 text-sm text-muted-foreground">{t("table.loadFailed")}{error}</Card>
      ) : !rows ? (
        <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
      ) : (
        <Card className="overflow-hidden p-0">
          <Table>
            <TableHeader>
              <TableRow className="bg-muted/40">
                <TableHead>{t("dash.campaigns.col.task")}</TableHead>
                <TableHead>{t("dash.campaigns.col.status")}</TableHead>
                <TableHead className="text-right">{t("dash.campaigns.col.total")}</TableHead>
                <TableHead className="text-right">{t("dash.campaigns.col.sent")}</TableHead>
                <TableHead className="text-right">{t("dash.campaigns.col.failed")}</TableHead>
                <TableHead className="text-right">{t("dash.campaigns.col.createdAt")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.length === 0 ? (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={6} className="py-12 text-center text-sm text-muted-foreground">
                    {t("dash.campaigns.empty")}
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((c) => {
                  const st = STATE[c.state];
                  return (
                    <TableRow key={c.id} className="cursor-pointer" onClick={() => setDetailId(c.id)}>
                      <TableCell className="font-mono text-sm">#{c.id}</TableCell>
                      <TableCell>
                        <StatusBadge tone={st.tone}>{t(st.key)}</StatusBadge>
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
