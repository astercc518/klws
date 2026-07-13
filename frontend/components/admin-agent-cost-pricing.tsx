"use client";

// admin-agent-cost-pricing.tsx — platform cost-price CRUD (module 9 / T8
// step 1). Mirrors GET/POST /admin/agent/cost-pricing (agent_admin.go):
// unit_cost is REQUIRED on write (a missing field is a 400; an explicit 0 is
// allowed) because T5/T6 join this table for money math — a forgotten field
// must not silently write a free-tier cost. The dialog below always sends a
// parsed number, never omits the field.

import { useCallback, useEffect, useState } from "react";
import { Plus, Pencil, Coins } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card } from "@/components/ui/card";
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

/** Mirrors costPricingRow in internal/api/agent_admin.go. */
interface CostPricingRow {
  country_code: string;
  unit_cost: number;
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

export function AdminAgentCostPricing() {
  const t = useT();
  const [rows, setRows] = useState<CostPricingRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editTarget, setEditTarget] = useState<CostPricingRow | null | "new">(null);

  const load = useCallback(async () => {
    try {
      const d = await api.get<CostPricingRow[]>("/admin/agent/cost-pricing");
      setRows(d);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.agents.cost.loadFailed"));
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

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <Button className="gap-2" onClick={() => setEditTarget("new")}>
          <Plus className="size-4" />
          {t("admin.agents.cost.addButton")}
        </Button>
      </div>

      <Card className="overflow-hidden p-0">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/40">
              <TableHead>{t("admin.agents.cost.col.country")}</TableHead>
              <TableHead>{t("admin.agents.cost.col.unitCost")}</TableHead>
              <TableHead className="w-12" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={3} className="py-10 text-center text-sm text-muted-foreground">
                  {t("admin.agents.cost.emptyState")}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((r) => (
                <TableRow key={r.country_code}>
                  <TableCell className="font-mono text-sm uppercase">{r.country_code}</TableCell>
                  <TableCell className="font-mono text-sm tabular-nums">{usd(r.unit_cost)}</TableCell>
                  <TableCell>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={t("admin.agents.cost.editAria")}
                      onClick={() => setEditTarget(r)}
                    >
                      <Pencil className="size-4" />
                    </Button>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>

      <CostPricingDialog target={editTarget} onClose={() => setEditTarget(null)} onDone={load} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Add/edit dialog → POST /admin/agent/cost-pricing (upsert on country_code)
// ---------------------------------------------------------------------------

function CostPricingDialog({
  target,
  onClose,
  onDone,
}: {
  target: CostPricingRow | null | "new";
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const isNew = target === "new";
  const [country, setCountry] = useState("");
  const [price, setPrice] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target === "new") {
      setCountry("");
      setPrice("");
    } else if (target) {
      setCountry(target.country_code);
      setPrice((target.unit_cost / 100).toFixed(2));
    }
  }, [target]);

  const cents = Math.round(parseFloat(price) * 100);
  const valid = country.trim().length === 2 && Number.isFinite(cents) && cents >= 0 && !busy;

  async function submit() {
    setBusy(true);
    try {
      await api.post("/admin/agent/cost-pricing", {
        country_code: country.trim().toUpperCase(),
        unit_cost: cents,
      });
      toast.success(t("admin.agents.cost.updateSuccessTitle"), {
        description: `${country.toUpperCase()} → ${usd(cents)}${t("admin.agents.cost.perUnitSuffix")}`,
      });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.agents.cost.setFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.agents.cost.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            <Coins className="mr-1.5 inline size-4 align-[-2px]" />
            {isNew ? t("admin.agents.cost.addTitle") : t("admin.agents.cost.editTitle")}
          </DialogTitle>
          <DialogDescription>{t("admin.agents.cost.dialogDesc")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="cp-country" className="text-sm font-medium">
              {t("admin.agents.cost.col.country")} <span className="font-mono text-xs text-muted-foreground">ISO-2</span>
            </label>
            <Input
              id="cp-country"
              value={country}
              onChange={(e) => setCountry(e.target.value)}
              maxLength={2}
              disabled={!isNew}
              className="w-24 font-mono uppercase"
            />
            {!isNew && (
              <p className="text-xs text-muted-foreground">{t("admin.agents.cost.countryImmutableHint")}</p>
            )}
          </div>
          <div className="space-y-2">
            <label htmlFor="cp-cost" className="text-sm font-medium">
              {t("admin.agents.cost.priceLabel")}{" "}
              <span className="font-mono text-xs text-muted-foreground">{t("admin.agents.cost.priceLabelHint")}</span>
            </label>
            <Input
              id="cp-cost"
              type="number"
              min="0"
              step="0.01"
              value={price}
              onChange={(e) => setPrice(e.target.value)}
              placeholder="0.02"
              className="font-mono"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.agents.cost.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("admin.agents.cost.submitting") : t("admin.agents.cost.confirmSave")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
