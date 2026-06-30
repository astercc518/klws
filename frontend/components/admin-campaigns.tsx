"use client";

import { useCallback, useEffect, useState } from "react";
import { Ban, Play, ShieldAlert } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
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
  state: "draft" | "running" | "paused" | "completed" | "failed";
  total: number;
  sent: number;
  failed: number;
  created_at: string;
  auto_tripped: boolean;
}

const stateVariant: Record<Campaign["state"], "default" | "secondary" | "outline" | "destructive"> = {
  running: "secondary",
  paused: "outline",
  draft: "outline",
  completed: "default",
  failed: "destructive",
};
const nf = new Intl.NumberFormat("en-US");

export function AdminCampaigns() {
  const [rows, setRows] = useState<Campaign[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [killTarget, setKillTarget] = useState<Campaign | null>(null);
  const [busy, setBusy] = useState(false);
  const [detailId, setDetailId] = useState<number | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await api.get<Campaign[]>("/admin/campaigns"));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);
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

  const [resumingId, setResumingId] = useState<number | null>(null);
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

  if (error) return <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>;
  if (!rows) return <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />;

  return (
    <>
      <Card className="overflow-hidden p-0">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/40">
              <TableHead>任务</TableHead>
              <TableHead>租户</TableHead>
              <TableHead>状态</TableHead>
              <TableHead className="text-right">总数</TableHead>
              <TableHead className="text-right">已发</TableHead>
              <TableHead className="text-right">失败</TableHead>
              <TableHead className="text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} className="py-10 text-center text-sm text-muted-foreground">
                  当前没有任务。
                </TableCell>
              </TableRow>
            ) : (
              rows.map((c) => (
                <TableRow
                  key={c.id}
                  className="cursor-pointer"
                  onClick={() => setDetailId(c.id)}
                >
                  <TableCell className="font-mono text-sm">#{c.id}</TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">#{c.tenant_id}</TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      <Badge variant={stateVariant[c.state]}>{c.state}</Badge>
                      {c.auto_tripped && (
                        <Badge
                          variant="destructive"
                          className="gap-1"
                          title="风控熔断器因封号率超阈值自动挂起了此任务"
                        >
                          <ShieldAlert className="size-3" />
                          自动熔断
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell className="text-right font-mono tabular-nums text-sm">{nf.format(c.total)}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums text-sm">{nf.format(c.sent)}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums text-sm text-muted-foreground">
                    {nf.format(c.failed)}
                  </TableCell>
                  <TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
                    {c.state === "paused" ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="gap-1.5"
                        disabled={resumingId === c.id}
                        onClick={() => resume(c)}
                      >
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
                    )}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>

      <Dialog open={killTarget != null} onOpenChange={(o) => !o && setKillTarget(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>强制终止任务</DialogTitle>
            <DialogDescription>
              将任务 <span className="font-mono">#{killTarget?.id}</span>(租户 #{killTarget?.tenant_id})置为 paused,
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

      <CampaignDetailSheet
        campaignId={detailId}
        apiBase="/admin/campaigns"
        onClose={() => setDetailId(null)}
      />
    </>
  );
}
