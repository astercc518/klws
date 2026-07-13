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
import { useT } from "@/components/locale-provider";

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
  const t = useT();
  const [rows, setRows] = useState<Customer[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pricingTarget, setPricingTarget] = useState<Customer | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await api.get<Customer[]>("/sales/customers"));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("sales.console.loadFailed"));
      }
    }
  }, [t]);
  useEffect(() => {
    load();
  }, [load]);

  if (error)
    return (
      <Card className="p-5 text-sm text-muted-foreground">
        {t("table.loadFailed")}
        {error}
      </Card>
    );
  if (!rows) return <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />;

  const totalBalance = rows.reduce((s, r) => s + r.balance, 0);
  const totalFrozen = rows.reduce((s, r) => s + r.frozen, 0);

  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <MetricCard
          hero
          label={t("sales.console.stat.customerCountLabel")}
          value={nf.format(rows.length)}
          sub={t("sales.console.stat.customerCountSub")}
          icon={Users}
        />
        <MetricCard
          label={t("sales.console.stat.totalBalanceLabel")}
          value={usd(totalBalance)}
          sub={t("sales.console.stat.totalBalanceSub")}
          icon={Wallet}
        />
        <MetricCard
          label={t("sales.console.stat.totalFrozenLabel")}
          value={usd(totalFrozen)}
          sub={t("sales.console.stat.totalFrozenSub")}
          icon={Snowflake}
        />
      </div>

      <Card className="overflow-hidden p-0">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/40">
              <TableHead>{t("sales.console.col.customer")}</TableHead>
              <TableHead>{t("sales.console.col.status")}</TableHead>
              <TableHead className="text-right">{t("sales.console.col.balance")}</TableHead>
              <TableHead className="text-right">{t("sales.console.col.frozen")}</TableHead>
              <TableHead className="text-right">{t("sales.console.col.actions")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="py-10 text-center text-sm text-muted-foreground">
                  {t("sales.console.emptyState")}
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
                      {t("sales.console.setPriceButton")}
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
  const t = useT();
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
      toast.success(t("sales.console.priceUpdatedTitle"), {
        description: `${target.name} · ${country.toUpperCase()} → $${(cents / 100).toFixed(2)}${t("sales.console.perUnitSuffix")}`,
      });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("sales.console.setFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("sales.console.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("sales.console.pricingDialog.title")}</DialogTitle>
          <DialogDescription>
            {t("sales.console.pricingDialog.descPrefix")}
            <span className="font-mono">{target?.name}</span>
            {t("sales.console.pricingDialog.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="s-country" className="text-sm font-medium">
              {t("sales.console.pricingDialog.countryLabel")}{" "}
              <span className="font-mono text-xs text-muted-foreground">ISO-2</span>
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
              {t("sales.console.pricingDialog.priceLabel")}{" "}
              <span className="font-mono text-xs text-muted-foreground">USD</span>
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
          <DialogClose render={<Button variant="ghost" />}>{t("sales.console.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("sales.console.submitting") : t("sales.console.confirmSet")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
