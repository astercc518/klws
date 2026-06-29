"use client";

import { useCallback, useEffect, useState } from "react";
import { Tag, Wallet, Snowflake, Users } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { MetricCard } from "@/components/metric-card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

interface Customer {
  id: number;
  name: string;
  status: string;
  balance: number;
  frozen: number;
}

const usd = (smallest: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(smallest / 100);
const nf = new Intl.NumberFormat("en-US");

export function SalesConsole() {
  const [rows, setRows] = useState<Customer[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pricingTarget, setPricingTarget] = useState<Customer | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await api.get<Customer[]>("/sales/customers"));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  if (error) return <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>;
  if (!rows) return <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />;

  const totalBalance = rows.reduce((s, r) => s + r.balance, 0);
  const totalFrozen = rows.reduce((s, r) => s + r.frozen, 0);

  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <MetricCard hero label="名下客户数" value={nf.format(rows.length)} sub="我负责的租户" icon={Users} />
        <MetricCard label="名下总余额" value={usd(totalBalance)} sub="可用合计 · USD" icon={Wallet} />
        <MetricCard label="名下总冻结" value={usd(totalFrozen)} sub="进行中活动占用" icon={Snowflake} />
      </div>

      <Card className="overflow-hidden p-0">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/40">
              <TableHead>客户</TableHead>
              <TableHead>状态</TableHead>
              <TableHead className="text-right">可用余额</TableHead>
              <TableHead className="text-right">冻结</TableHead>
              <TableHead className="text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="py-10 text-center text-sm text-muted-foreground">
                  你名下还没有客户。请联系管理员为你分配。
                </TableCell>
              </TableRow>
            ) : (
              rows.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="font-medium">{r.name}</TableCell>
                  <TableCell>
                    <Badge variant={r.status === "active" ? "secondary" : "outline"}>{r.status}</Badge>
                  </TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{usd(r.balance)}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums text-muted-foreground">
                    {usd(r.frozen)}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button variant="ghost" size="sm" className="gap-1.5" onClick={() => setPricingTarget(r)}>
                      <Tag className="size-3.5" />
                      设单价
                    </Button>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>

      <PricingDialog target={pricingTarget} onClose={() => setPricingTarget(null)} onDone={load} />
    </>
  );
}

function PricingDialog({
  target,
  onClose,
  onDone,
}: {
  target: Customer | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const [country, setCountry] = useState("US");
  const [price, setPrice] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target) {
      setCountry("US");
      setPrice("");
    }
  }, [target]);

  const cents = Math.round(parseFloat(price) * 100);
  const valid = country.trim().length === 2 && !Number.isNaN(cents) && cents > 0 && !busy;

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.post(`/sales/customers/${target.id}/pricing`, {
        country: country.trim().toUpperCase(),
        unit_price: cents,
      });
      toast.success("单价已更新", {
        description: `${target.name} · ${country.toUpperCase()} → $${(cents / 100).toFixed(2)} / 条`,
      });
      onClose();
      onDone();
    } catch (e) {
      toast.error("设置失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>配置发信单价</DialogTitle>
          <DialogDescription>
            为名下客户 <span className="font-mono">{target?.name}</span> 设置指定国家的每条单价。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="s-country" className="text-sm font-medium">
              国家 <span className="font-mono text-xs text-muted-foreground">ISO-2</span>
            </label>
            <Input
              id="s-country"
              value={country}
              onChange={(e) => setCountry(e.target.value)}
              maxLength={2}
              className="w-24 font-mono uppercase"
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="s-price" className="text-sm font-medium">
              每条单价 <span className="font-mono text-xs text-muted-foreground">USD</span>
            </label>
            <Input
              id="s-price"
              type="number"
              min="0"
              step="0.01"
              value={price}
              onChange={(e) => setPrice(e.target.value)}
              placeholder="0.05"
              className="font-mono"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "提交中…" : "确认设置"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
