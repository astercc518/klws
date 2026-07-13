"use client";

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useT } from "@/components/locale-provider";

interface LedgerRow {
  id: number;
  tenant_id: number;
  tenant_name: string | null;
  kind: string;
  delta_balance: number;
  delta_frozen: number;
  balance_after: number;
  frozen_after: number;
  created_at: string;
}

interface RefundRow {
  id: number;
  tenant_id: number;
  amount: number;
  reason: string;
  state: string;
}

interface LedgerResponse {
  rows: LedgerRow[];
  total: number;
  refunds: RefundRow[];
}

const LIMIT = 50;

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

function Money({ cents }: { cents: number }) {
  const cls = cents < 0 ? "text-rose-600 dark:text-rose-400" : "text-emerald-600 dark:text-emerald-400";
  return <span className={`font-mono tabular-nums ${cls}`}>{usd(cents)}</span>;
}

/** Reusable side sheet showing one customer/tenant's wallet ledger (最近 50 条).
 *  Open state is driven by `tenantId != null`; caller owns the state via `onClose`. */
export function CustomerLedgerSheet({
  tenantId,
  label,
  onClose,
}: {
  tenantId: number | null;
  label: string;
  onClose: () => void;
}) {
  const t = useT();
  const [rows, setRows] = useState<LedgerRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  // Reset state whenever a different tenant is opened.
  useEffect(() => {
    setRows(null);
    setError(null);
  }, [tenantId]);

  const load = useCallback(async () => {
    if (tenantId == null) return;
    setLoading(true);
    try {
      const res = await api.get<LedgerResponse>(
        `/admin/finance/ledger?tenant_id=${tenantId}&limit=${LIMIT}`,
      );
      setRows(res.rows);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.ledger.sheet.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [tenantId, t]);
  useEffect(() => {
    load();
  }, [load]);

  return (
    <Sheet open={tenantId != null} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>
            {label}
            {t("admin.ledger.sheet.titleSuffix")}
          </SheetTitle>
          <SheetDescription>{t("admin.ledger.sheet.recentCount")}</SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 overflow-y-auto rounded-lg border">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-muted/60 backdrop-blur">
              <TableRow>
                <TableHead className="h-9">{t("admin.ledger.sheet.col.kind")}</TableHead>
                <TableHead className="h-9 text-right">{t("admin.ledger.sheet.col.deltaBalance")}</TableHead>
                <TableHead className="h-9 text-right">{t("admin.ledger.sheet.col.balanceAfter")}</TableHead>
                <TableHead className="h-9 text-right">{t("admin.ledger.sheet.col.time")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {error ? (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={4} className="py-12 text-center text-sm text-muted-foreground">
                    {t("admin.ledger.sheet.loadFailedPrefix")}
                    {error}
                  </TableCell>
                </TableRow>
              ) : loading && !rows ? (
                Array.from({ length: 6 }).map((_, i) => (
                  <TableRow key={i} className="hover:bg-transparent">
                    <TableCell colSpan={4} className="py-2">
                      <div className="h-5 animate-pulse rounded bg-muted motion-reduce:animate-none" />
                    </TableCell>
                  </TableRow>
                ))
              ) : !rows || rows.length === 0 ? (
                <TableRow className="hover:bg-transparent">
                  <TableCell colSpan={4} className="py-12 text-center text-sm text-muted-foreground">
                    {t("admin.ledger.sheet.empty")}
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell>
                      <Badge variant="outline">{r.kind}</Badge>
                    </TableCell>
                    <TableCell className="text-right">
                      <Money cents={r.delta_balance} />
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">
                      {usd(r.balance_after)}
                    </TableCell>
                    <TableCell className="text-right font-mono text-xs text-muted-foreground">
                      {r.created_at}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      </SheetContent>
    </Sheet>
  );
}
