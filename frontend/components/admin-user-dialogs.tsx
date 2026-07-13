"use client";

// admin-user-dialogs.tsx — tenant-scoped finance/ops dialogs used from the
// users page (customer hub, SP8 Task 4). Moved here (unchanged request
// bodies) from admin-tenants-table.tsx, which is being folded into the users
// page — see AGENT.md task history. All three operate on a customer's
// `tenant_id`; the caller passes a small `{ tenantId, label }` target instead
// of the old tenant-table `Row` so this file has no dependency on that page.

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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

/** Shared shape for the tenant-scoped dialogs below. `label` is shown in
 *  dialog copy/toasts — the caller passes the customer's account email. */
export interface TenantActionTarget {
  tenantId: number;
  label: string;
}

export interface AssignActionTarget extends TenantActionTarget {
  currentSalesId: number | null;
}

export interface SalesOption {
  id: number;
  email: string;
}

// ---------------------------------------------------------------------------
// Top-up dialog → POST /admin/finance/topup
// ---------------------------------------------------------------------------

export function TopupDialog({
  target,
  onClose,
  onDone,
}: {
  target: TenantActionTarget | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [amount, setAmount] = useState("");
  const [ref, setRef] = useState("");
  const [busy, setBusy] = useState(false);

  // Reset fields whenever a new target opens the dialog.
  useEffect(() => {
    if (target) {
      setAmount("");
      setRef(`manual-${Date.now()}`);
    }
  }, [target]);

  const cents = Math.round(parseFloat(amount) * 100);
  const valid = !Number.isNaN(cents) && cents > 0 && ref.trim().length > 0 && !busy;

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.post("/admin/finance/topup", {
        tenant_id: target.tenantId,
        amount: cents,
        ref: ref.trim(),
      });
      toast.success(t("admin.users.dlg.topupSuccessTitle"), {
        description: `${target.label} +$${(cents / 100).toFixed(2)}`,
      });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.users.dlg.topupFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.users.dlg.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.users.dlg.topupTitle")}</DialogTitle>
          <DialogDescription>
            {t("admin.users.dlg.topupDescPrefix")}
            <span className="font-mono">{target?.label}</span>
            {t("admin.users.dlg.topupDescSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="topup-amount" className="text-sm font-medium">
              {t("admin.users.dlg.topupAmountLabel")}{" "}
              <span className="font-mono text-xs text-muted-foreground">USD</span>
            </label>
            <Input
              id="topup-amount"
              type="number"
              min="0"
              step="0.01"
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              placeholder="100.00"
              className="font-mono"
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="topup-ref" className="text-sm font-medium">
              {t("admin.users.dlg.refLabel")}{" "}
              <span className="font-mono text-xs text-muted-foreground">{t("admin.users.dlg.refHint")}</span>
            </label>
            <Input
              id="topup-ref"
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              className="font-mono text-sm"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.users.dlg.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("admin.users.dlg.submitting") : t("admin.users.dlg.confirmTopup")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Pricing dialog → POST /admin/finance/pricing
// ---------------------------------------------------------------------------

export function PricingDialog({
  target,
  onClose,
  onDone,
}: {
  target: TenantActionTarget | null;
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
      await api.post("/admin/finance/pricing", {
        tenant_id: target.tenantId,
        country: country.trim().toUpperCase(),
        unit_price: cents,
      });
      toast.success(t("admin.users.dlg.pricingUpdatedTitle"), {
        description: `${country.toUpperCase()} → $${(cents / 100).toFixed(2)}${t("admin.users.dlg.perMessageUnitSuffix")}`,
      });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.users.dlg.pricingFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.users.dlg.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.users.dlg.pricingTitle")}</DialogTitle>
          <DialogDescription>
            {t("admin.users.dlg.pricingDescPrefix")}
            <span className="font-mono">{target?.label}</span>
            {t("admin.users.dlg.pricingDescSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="price-country" className="text-sm font-medium">
              {t("admin.users.dlg.countryLabel")}{" "}
              <span className="font-mono text-xs text-muted-foreground">ISO-2</span>
            </label>
            <Input
              id="price-country"
              value={country}
              onChange={(e) => setCountry(e.target.value)}
              maxLength={2}
              className="w-24 font-mono uppercase"
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="price-unit" className="text-sm font-medium">
              {t("admin.users.dlg.perMessagePriceLabel")}{" "}
              <span className="font-mono text-xs text-muted-foreground">USD</span>
            </label>
            <Input
              id="price-unit"
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
          <DialogClose render={<Button variant="ghost" />}>{t("admin.users.dlg.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("admin.users.dlg.submitting") : t("admin.users.dlg.confirmPricing")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Assign sales dialog → POST /admin/sales/:tenantId/assign
// ---------------------------------------------------------------------------

export function AssignSalesDialog({
  target,
  salesUsers,
  onClose,
  onDone,
}: {
  target: AssignActionTarget | null;
  salesUsers: SalesOption[];
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [salesId, setSalesId] = useState<string>("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target) setSalesId(target.currentSalesId != null ? String(target.currentSalesId) : "");
  }, [target]);

  const valid = salesId !== "" && !busy;

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.post(`/admin/sales/${target.tenantId}/assign`, { sales_user_id: Number(salesId) });
      toast.success(t("admin.users.dlg.assignedTitle"), {
        description: `${target.label} → ${salesUsers.find((s) => String(s.id) === salesId)?.email ?? salesId}`,
      });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.users.dlg.assignFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.users.dlg.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.users.dlg.assignTitle")}</DialogTitle>
          <DialogDescription>
            {t("admin.users.dlg.assignDescPrefix")}
            <span className="font-mono">{target?.label}</span>
            {t("admin.users.dlg.assignDescSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <label htmlFor="assign-sales" className="text-sm font-medium">{t("admin.users.dlg.salesAccountLabel")}</label>
          {salesUsers.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("admin.users.dlg.noSalesUsers")}</p>
          ) : (
            <select
              id="assign-sales"
              value={salesId}
              onChange={(e) => setSalesId(e.target.value)}
              className="h-9 w-full rounded-lg border bg-transparent px-2.5 text-sm outline-none"
            >
              <option value="" disabled>{t("admin.users.dlg.selectSalesPlaceholder")}</option>
              {salesUsers.map((s) => (
                <option key={s.id} value={String(s.id)}>{s.email}</option>
              ))}
            </select>
          )}
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.users.dlg.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("admin.users.dlg.submitting") : t("admin.users.dlg.confirmAssign")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
