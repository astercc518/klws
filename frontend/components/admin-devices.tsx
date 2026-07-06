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
function statusBadge(d: Device): { label: string; tone: StatusTone } {
  if (d.ban_status === "banned") return { label: "封禁", tone: "negative" };
  if (d.ban_status === "flagged") return { label: "标记", tone: "warning" };
  if (d.ban_status === "active" && d.owner_node) return { label: "在线", tone: "positive" };
  if (d.ban_status === "logged_out") return { label: "已登出", tone: "neutral" };
  return { label: d.owner_node ? "在线" : "离线", tone: d.owner_node ? "positive" : "neutral" };
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
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, banStatus, online]);
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
      key: "tags",
      header: "标签",
      cell: (d) => <TagBadges tags={d.tags} />,
    },
    {
      key: "proxy",
      header: "网络环境",
      cell: (d) =>
        d.proxy_url ? (
          <span className="inline-flex items-center gap-1.5 font-mono text-xs">
            <Network className="size-3.5 text-muted-foreground" />
            {d.proxy_url}
          </span>
        ) : (
          <StatusBadge tone="warning">未绑定</StatusBadge>
        ),
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
      {stats && (
        <MetricCardGroup className="mb-6">
          <StatCard accent="brand" label="设备总数" value={String(stats.total)} sub="全网 WA 账号" icon={Smartphone} />
          <StatCard accent="emerald" label="在线" value={String(stats.online)} sub="已接管并可发送" icon={Wifi} />
          <StatCard accent="rose" label="封禁 / 标记" value={String(stats.banned + stats.flagged)} sub="被风控命中" icon={ShieldAlert} />
          <StatCard accent="amber" label="已登出" value={String(stats.logged_out)} sub="需重新扫码接入" icon={LogOut} />
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
        search={{ placeholder: "搜索 JID / 手机号 / 标签…", accessor: () => "" }}
        emptyState="设备池为空。可批量录入元数据,或在节点侧扫码接入。"
        toolbar={
          <div className="flex items-center gap-2">
            <select
              value={banStatus}
              onChange={(e) => {
                setBanStatus(e.target.value as "all" | "active" | "banned" | "flagged" | "logged_out");
                setPage(0);
              }}
              className="h-8 rounded-md border bg-transparent px-2 text-sm"
              aria-label="按状态筛选"
            >
              <option value="all">全部状态</option>
              <option value="active">活跃</option>
              <option value="banned">封禁</option>
              <option value="flagged">标记</option>
              <option value="logged_out">已登出</option>
            </select>
            <select
              value={online}
              onChange={(e) => {
                setOnline(e.target.value as "all" | "true" | "false");
                setPage(0);
              }}
              className="h-8 rounded-md border bg-transparent px-2 text-sm"
              aria-label="按在线状态筛选"
            >
              <option value="all">全部</option>
              <option value="true">在线</option>
              <option value="false">离线</option>
            </select>
            <ConnectDeviceDialog />
            <ImportDevicesDialog onDone={load} />
          </div>
        }
        rowActions={(d) => (
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label="操作" />}>
              <MoreHorizontal className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => setProxyTarget(d)}>
                <Network className="size-4" />
                配置网络
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setEditTarget(d)}>
                <Pencil className="size-4" />
                编辑 Edit
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onClick={() => setDeleteTarget(d)}>
                <Trash2 className="size-4" />
                删除 Delete
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
            每行一个,格式{" "}
            <span className="font-mono">tenant_id:account_jid:phone:tag1,tag2</span>。标签段可省略,多个标签用逗号分隔。仅写入元数据,实际登录仍走节点扫码。
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
            placeholder={"1:8613800000000@s.whatsapp.net:+8613800000000:US-Marketing,Tier1\n1:8613900000000@s.whatsapp.net:+8613900000000"}
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
      toast.success("代理已绑定", { description: selectedProxy?.proxy_url });
      onClose();
      onDone();
    } catch (e) {
      toast.error("绑定失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  async function unbind() {
    if (!target) return;
    setBusy(true);
    try {
      await api.delete(`/admin/resources/devices/${target.id}/proxy`);
      toast.success("已解绑代理");
      onClose();
      onDone();
    } catch (e) {
      toast.error("解绑失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>配置网络环境</DialogTitle>
          <DialogDescription>
            为 <span className="font-mono text-xs">{target?.account_jid}</span>{" "}
            绑定独享静态代理 IP,实现账号间网络隔离、防止关联封号。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-1.5">
            <span className="text-sm font-medium">当前绑定</span>
            <div>
              {target?.proxy_url ? (
                <span className="inline-flex items-center gap-1.5 font-mono text-xs">
                  <Network className="size-3.5 text-muted-foreground" />
                  {target.proxy_url}
                </span>
              ) : (
                <StatusBadge tone="warning">未绑定</StatusBadge>
              )}
            </div>
          </div>
          <div className="space-y-1.5">
            <span className="text-sm font-medium">选择代理 IP</span>
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="outline" className="w-full justify-between gap-2 font-mono text-xs" />}
              >
                <span className="truncate">
                  {selectedProxy
                    ? `${selectedProxy.proxy_url} · ${selectedProxy.country_code}`
                    : "选择一个可用代理…"}
                </span>
                <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="max-h-64 w-72 overflow-y-auto">
                {selectable.length === 0 ? (
                  <DropdownMenuItem disabled>无可用代理</DropdownMenuItem>
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
              解绑
            </Button>
          ) : (
            <span />
          )}
          <div className="flex items-center gap-2">
            <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
            <Button
              onClick={bind}
              disabled={busy || selected == null || selected === target?.proxy_id}
            >
              {busy ? "提交中…" : "确认绑定"}
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
      toast.success("设备已更新", { description: target.account_jid });
      onClose();
      onDone();
    } catch (e) {
      toast.error("更新失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>编辑设备</DialogTitle>
          <DialogDescription>
            修改 <span className="font-mono text-xs">{target?.account_jid}</span> 的归属租户、手机号与标签。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="flex gap-3">
            <div className="space-y-2">
              <label htmlFor="edit-dv-tenant" className="text-sm font-medium">租户 ID</label>
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
              <label htmlFor="edit-dv-phone" className="text-sm font-medium">手机号</label>
              <Input
                id="edit-dv-phone"
                value={phone}
                onChange={(e) => setPhone(e.target.value)}
                className="font-mono"
              />
            </div>
          </div>
          <div className="space-y-2">
            <label htmlFor="edit-dv-tags" className="text-sm font-medium">标签</label>
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
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>{busy ? "保存中…" : "确认保存"}</Button>
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
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.delete(`/admin/resources/devices/${target.id}`);
      toast.success("设备已删除", { description: target.account_jid });
      onClose();
      onDone();
    } catch (e) {
      toast.error("删除失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>删除设备</DialogTitle>
          <DialogDescription>
            确认删除设备 <span className="font-mono text-xs">{target?.account_jid}</span>
            ?此操作不可撤销,若已绑定代理会自动释放。
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? "删除中…" : "确认删除"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
