"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Plus, Globe, Wifi, WifiOff, Activity } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatusBadge } from "@/components/admin/status-badge";
import { MetricCardGroup, StatCard } from "@/components/admin/stat-card";
import { RowAvatar } from "@/components/admin/row-avatar";
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

interface Proxy {
  id: number;
  proxy_url: string;
  proxy_type: string;
  country_code: string;
  is_alive: boolean;
  current_bindings: number;
  max_bindings: number;
  failure_count: number;
}

// Parse "IP:Port:User:Pass" (user:pass optional) into the API's proxy objects.
function parseProxies(raw: string, type: string, country: string) {
  return raw
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .map((line) => {
      const [ip, port, user, pass] = line.split(":");
      if (!ip || !port) return null;
      const auth = user && pass ? `${user}:${pass}@` : "";
      return { url: `${type}://${auth}${ip}:${port}`, type, country: country.toUpperCase() };
    })
    .filter((p): p is { url: string; type: string; country: string } => p !== null);
}

const PAGE_SIZE = 20;

interface ProxyStats { total: number; alive: number; dead: number }

export function AdminProxies() {
  const [rows, setRows] = useState<Proxy[] | null>(null);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<ProxyStats | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [alive, setAlive] = useState<"all" | "true" | "false">("all");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (alive !== "all") params.set("alive", alive);
    try {
      const d = await api.get<{ rows: Proxy[]; total: number; stats: ProxyStats }>(
        `/admin/resources/proxies?${params.toString()}`,
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
  }, [page, query, alive]);

  useEffect(() => {
    load();
  }, [load]);

  const columns: Column<Proxy>[] = [
    {
      key: "url",
      header: "代理地址",
      cell: (p) => (
        <div className="flex items-center gap-2.5">
          <RowAvatar icon={Globe} accent={p.is_alive ? "emerald" : "rose"} />
          <span className="font-mono text-xs">{p.proxy_url}</span>
        </div>
      ),
    },
    {
      key: "type",
      header: "类型",
      cell: (p) => <span className="font-mono text-xs uppercase">{p.proxy_type}</span>,
    },
    {
      key: "country",
      header: "国家",
      cell: (p) => <span className="font-mono text-xs">{p.country_code}</span>,
    },
    {
      key: "status",
      header: "状态",
      cell: (p) => (
        <StatusBadge tone={p.is_alive ? "positive" : "negative"}>
          {p.is_alive ? "在线" : "失效"}
        </StatusBadge>
      ),
    },
    {
      key: "bindings",
      header: "绑定",
      align: "right",
      cell: (p) => (
        <span className="font-mono text-sm tabular-nums">
          {p.current_bindings}/{p.max_bindings}
        </span>
      ),
    },
    {
      key: "failures",
      header: "失败",
      align: "right",
      cell: (p) => (
        <span className="font-mono text-sm tabular-nums text-muted-foreground">
          {p.failure_count}
        </span>
      ),
    },
  ];

  return (
    <>
      {stats && (
        <MetricCardGroup className="mb-6">
          <StatCard accent="brand" label="代理总数" value={String(stats.total)} sub="全平台出口" icon={Globe} />
          <StatCard accent="emerald" label="在线" value={String(stats.alive)} sub="健康可调度" icon={Wifi} />
          <StatCard accent="rose" label="离线 / 失效" value={String(stats.dead)} sub="需排查或剔除" icon={WifiOff} />
          <StatCard accent="amber" label="当前页" value={String(total)} sub="匹配筛选的总数" icon={Activity} />
        </MetricCardGroup>
      )}
      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(p) => p.id}
        emptyState="代理池为空,点击右上角批量导入。"
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query,
          onQueryChange: (q) => { setQuery(q); setPage(0); },
          loading,
        }}
        search={{ placeholder: "搜索地址或国家…", accessor: () => "" }}
        toolbar={
          <div className="flex items-center gap-2">
            <select
              value={alive}
              onChange={(e) => { setAlive(e.target.value as "all" | "true" | "false"); setPage(0); }}
              className="h-8 rounded-md border bg-transparent px-2 text-sm"
              aria-label="按状态筛选"
            >
              <option value="all">全部状态</option>
              <option value="true">在线</option>
              <option value="false">失效</option>
            </select>
            <ImportProxiesDialog onDone={load} />
          </div>
        }
      />
    </>
  );
}

function ImportProxiesDialog({ onDone }: { onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const [raw, setRaw] = useState("");
  const [type, setType] = useState("socks5");
  const [country, setCountry] = useState("US");
  const [busy, setBusy] = useState(false);

  const parsed = useMemo(() => parseProxies(raw, type, country), [raw, type, country]);
  const valid = parsed.length > 0 && country.trim().length === 2 && !busy;

  async function submit() {
    setBusy(true);
    try {
      const res = await api.post<{ imported: number; skipped: number }>(
        "/admin/resources/proxies",
        { proxies: parsed },
      );
      toast.success("代理导入完成", { description: `新增 ${res.imported} · 跳过 ${res.skipped}` });
      setOpen(false);
      setRaw("");
      onDone();
    } catch (e) {
      toast.error("导入失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button className="gap-2" />}>
        <Plus className="size-4" />
        批量导入代理
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>批量导入代理</DialogTitle>
          <DialogDescription>
            每行一个,格式 <span className="font-mono">IP:Port:User:Pass</span>(无认证可省略后两段)。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="flex gap-3">
            <div className="space-y-2">
              <label htmlFor="px-type" className="text-sm font-medium">类型</label>
              <select
                id="px-type"
                value={type}
                onChange={(e) => setType(e.target.value)}
                className="h-9 rounded-md border bg-transparent px-3 font-mono text-sm"
              >
                <option value="socks5">socks5</option>
                <option value="http">http</option>
              </select>
            </div>
            <div className="space-y-2">
              <label htmlFor="px-country" className="text-sm font-medium">国家 ISO-2</label>
              <Input
                id="px-country"
                value={country}
                onChange={(e) => setCountry(e.target.value)}
                maxLength={2}
                className="w-24 font-mono uppercase"
              />
            </div>
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <label htmlFor="px-raw" className="text-sm font-medium">代理列表</label>
              <span className="font-mono text-xs tabular-nums text-muted-foreground">
                解析到 {parsed.length} 条
              </span>
            </div>
            <Textarea
              id="px-raw"
              value={raw}
              onChange={(e) => setRaw(e.target.value)}
              placeholder={"203.0.113.5:1080:user:pass\n198.51.100.9:1080"}
              className="h-40 resize-none font-mono text-sm"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "导入中…" : `确认导入 ${parsed.length} 条`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
