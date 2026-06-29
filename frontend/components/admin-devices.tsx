"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Plus, QrCode, Smartphone, Wifi, ShieldAlert, LogOut } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";
import { MetricCardGroup, StatCard, type Accent } from "@/components/admin/stat-card";
import { RowAvatar } from "@/components/admin/row-avatar";

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
}

// Status → tone. Online = active and currently owned by a node. Banned reads
// red, flagged reads amber (a softer warning), the rest stay neutral gray.
function statusBadge(d: Device): { label: string; tone: StatusTone } {
  if (d.ban_status === "banned") return { label: "封禁", tone: "negative" };
  if (d.ban_status === "flagged") return { label: "标记", tone: "warning" };
  if (d.ban_status === "active" && d.owner_node) return { label: "在线", tone: "positive" };
  if (d.ban_status === "logged_out") return { label: "已登出", tone: "neutral" };
  return { label: d.owner_node ? "在线" : "离线", tone: d.owner_node ? "positive" : "neutral" };
}

// Parse "tenant_id:account_jid:phone" per line.
function parseDevices(raw: string) {
  return raw
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .map((line) => {
      const [tid, jid, phone] = line.split(":");
      const tenant_id = Number(tid);
      if (!tenant_id || !jid || !phone) return null;
      return { tenant_id, account_jid: jid, phone };
    })
    .filter((d): d is { tenant_id: number; account_jid: string; phone: string } => d !== null);
}

export function AdminDevices() {
  const [rows, setRows] = useState<Device[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await api.get<Device[]>("/admin/resources/devices"));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const summary = useMemo(() => {
    if (!rows) return null;
    let online = 0;
    let risky = 0;
    let loggedOut = 0;
    for (const d of rows) {
      if (d.ban_status === "banned" || d.ban_status === "flagged") risky++;
      else if (d.ban_status === "logged_out") loggedOut++;
      else if (d.owner_node) online++;
    }
    return { total: rows.length, online, risky, loggedOut };
  }, [rows]);

  const columns: Column<Device>[] = [
    {
      key: "jid",
      header: "账号 JID",
      cell: (d) => (
        <div className="flex items-center gap-2.5">
          <RowAvatar icon={Smartphone} accent={TONE_ACCENT[statusBadge(d).tone]} />
          <span className="font-mono text-xs">{d.account_jid}</span>
        </div>
      ),
    },
    {
      key: "phone",
      header: "手机号",
      cell: (d) => <span className="font-mono text-xs">{d.phone_number}</span>,
    },
    {
      key: "tenant",
      header: "租户",
      cell: (d) => <span className="font-mono text-xs text-muted-foreground">#{d.tenant_id}</span>,
    },
    {
      key: "status",
      header: "状态",
      cell: (d) => {
        const s = statusBadge(d);
        return <StatusBadge tone={s.tone}>{s.label}</StatusBadge>;
      },
    },
    {
      key: "node",
      header: "所属节点",
      cell: (d) => (
        <span className="font-mono text-xs text-muted-foreground">{d.owner_node ?? "—"}</span>
      ),
    },
  ];

  return (
    <>
      {summary && (
        <MetricCardGroup className="mb-6">
          <StatCard accent="brand" label="设备总数" value={String(summary.total)} sub="全网 WA 账号" icon={Smartphone} />
          <StatCard accent="emerald" label="在线" value={String(summary.online)} sub="已接管并可发送" icon={Wifi} />
          <StatCard accent="rose" label="封禁 / 标记" value={String(summary.risky)} sub="被风控命中" icon={ShieldAlert} />
          <StatCard accent="amber" label="已登出" value={String(summary.loggedOut)} sub="需重新扫码接入" icon={LogOut} />
        </MetricCardGroup>
      )}
      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(d) => d.id}
        search={{
          placeholder: "搜索 JID 或手机号…",
          accessor: (d) => `${d.account_jid} ${d.phone_number}`,
        }}
        emptyState="设备池为空。可批量录入元数据,或在节点侧扫码接入。"
        toolbar={
          <>
            <ConnectDeviceDialog />
            <ImportDevicesDialog onDone={load} />
          </>
        }
      />
    </>
  );
}

// Live QR pairing is a node-side (cmd/wadist) whatsmeow flow — there is no HTTP
// streaming endpoint for it yet, and adding one would touch the cluster engine
// (off-limits). This dialog reserves the UX and is honest about that.
function ConnectDeviceDialog() {
  return (
    <Dialog>
      <DialogTrigger render={<Button variant="outline" className="gap-2" />}>
        <QrCode className="size-4" />
        接入新账号
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>扫码接入 WhatsApp 账号</DialogTitle>
          <DialogDescription>用 WhatsApp → 已关联的设备 → 关联设备,扫描下方二维码。</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col items-center gap-3 py-4">
          <div className="flex size-52 items-center justify-center rounded-lg border border-dashed bg-muted/40">
            <QrCode className="size-16 text-muted-foreground/40" strokeWidth={1} />
          </div>
          <p className="max-w-xs text-center font-mono text-[11px] leading-relaxed text-muted-foreground">
            实时二维码由发信节点(cmd/wadist)的 whatsmeow 配对流程产生。该集群流式端点尚未对外开放,此处为预留位 —— 不触碰底层防封/集群逻辑。
          </p>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>关闭</DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ImportDevicesDialog({ onDone }: { onDone: () => void }) {
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
      toast.success("设备录入完成", { description: `新增 ${res.imported} · 跳过 ${res.skipped}` });
      setOpen(false);
      setRaw("");
      onDone();
    } catch (e) {
      toast.error("录入失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button className="gap-2" />}>
        <Plus className="size-4" />
        批量录入设备
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>批量录入设备元数据</DialogTitle>
          <DialogDescription>
            每行一个,格式 <span className="font-mono">tenant_id:account_jid:phone</span>。仅写入元数据,实际登录仍走节点扫码。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <div className="flex items-center justify-between">
            <label htmlFor="dv-raw" className="text-sm font-medium">设备列表</label>
            <span className="font-mono text-xs tabular-nums text-muted-foreground">解析到 {parsed.length} 条</span>
          </div>
          <Textarea
            id="dv-raw"
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            placeholder={"1:8613800000000@s.whatsapp.net:+8613800000000\n1:8613900000000@s.whatsapp.net:+8613900000000"}
            className="h-40 resize-none font-mono text-sm"
          />
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "录入中…" : `确认录入 ${parsed.length} 条`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
