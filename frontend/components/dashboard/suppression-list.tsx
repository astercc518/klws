"use client";

// suppression-list.tsx — blacklist / opt-out tab (Task 8, Step 4).
// suppression_list only ever returns {id, reason, added_at} — the backend
// stores a one-way blind index, not the phone, so there is nothing to show
// or search on beyond that. Append-only by compliance design: no delete/
// un-suppress endpoint exists, so this UI has none either.

import { useCallback, useEffect, useState } from "react";
import { Plus, ShieldBan } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
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
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { useT } from "@/components/locale-provider";

interface SuppressionRow {
  id: number;
  reason: string;
  added_at: string;
}
const PAGE_SIZE = 20;

export function SuppressionList() {
  const t = useT();
  const [rows, setRows] = useState<SuppressionRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const d = await api.get<{ rows: SuppressionRow[]; total: number }>(
        `/suppression?limit=${PAGE_SIZE}&offset=${page * PAGE_SIZE}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("dash.suppression.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, t]);
  useEffect(() => {
    load();
  }, [load]);

  const columns: Column<SuppressionRow>[] = [
    { key: "id", header: t("dash.suppression.col.id"), cell: (r) => <span className="font-mono text-xs text-muted-foreground">#{r.id}</span> },
    { key: "reason", header: t("dash.suppression.col.reason"), cell: (r) => <span className="text-sm">{r.reason || "—"}</span> },
    {
      key: "added_at",
      header: t("dash.suppression.col.addedAt"),
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.added_at.slice(0, 19).replace("T", " ")}</span>,
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          {t("dash.suppression.complianceNote")}
        </p>
        <AddSuppressionDialog onDone={load} />
      </div>

      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(r) => r.id}
        emptyState={t("dash.suppression.empty")}
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query: "",
          onQueryChange: () => {},
          loading,
        }}
      />
    </div>
  );
}

interface SuppressionReport {
  total: number;
  inserted: number;
  duplicates: number;
  invalid: number;
}

function parsePhones(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

// Single dialog covers both "add" (typed/pasted phones) and "bulk import" —
// both hit /suppression/import, which accepts freeform pasted text; a
// dedicated single-phone POST /suppression flow would just duplicate this UI.
function AddSuppressionDialog({ onDone }: { onDone: () => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [country, setCountry] = useState("US");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<SuppressionReport | null>(null);

  const count = parsePhones(text).length;
  const valid = count > 0 && country.trim().length === 2 && !busy;

  function reset() {
    setText("");
    setReason("");
    setReport(null);
  }

  async function submit() {
    setBusy(true);
    try {
      const rep = await api.post<SuppressionReport>("/suppression/import", {
        country: country.trim().toUpperCase(),
        text,
        reason: reason.trim() || undefined,
      });
      setReport(rep);
      onDone();
    } catch (e) {
      toast.error(t("dash.suppression.addFailed"), { description: e instanceof ApiError ? e.message : t("dash.suppression.retry") });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) reset();
      }}
    >
      <DialogTrigger render={<Button size="sm" className="gap-1.5" />}>
        <ShieldBan className="size-4" />
        {t("dash.suppression.addTrigger")}
      </DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("dash.suppression.addTitle")}</DialogTitle>
          <DialogDescription>
            {t("dash.suppression.addDesc")}
          </DialogDescription>
        </DialogHeader>

        {report ? (
          <div className="space-y-3 py-1">
            <div className="grid grid-cols-4 gap-2 text-center">
              <div className="rounded-lg border bg-muted/30 py-3">
                <div className="font-mono text-xl font-semibold tabular-nums">{report.total}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">{t("dash.suppression.report.total")}</div>
              </div>
              <div className="rounded-lg border bg-muted/30 py-3">
                <div className="font-mono text-xl font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">{report.inserted}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">{t("dash.suppression.report.inserted")}</div>
              </div>
              <div className="rounded-lg border bg-muted/30 py-3">
                <div className="font-mono text-xl font-semibold tabular-nums text-amber-600 dark:text-amber-400">{report.duplicates}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">{t("dash.suppression.report.duplicates")}</div>
              </div>
              <div className="rounded-lg border bg-muted/30 py-3">
                <div className="font-mono text-xl font-semibold tabular-nums text-rose-600 dark:text-rose-400">{report.invalid}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">{t("dash.suppression.report.invalid")}</div>
              </div>
            </div>
          </div>
        ) : (
          <div className="space-y-4 py-1">
            <div className="flex gap-3">
              <div className="space-y-2">
                <label htmlFor="sup-country" className="text-sm font-medium">{t("dash.suppression.countryLabel")} <span className="font-mono text-xs text-muted-foreground">ISO-2</span></label>
                <Input id="sup-country" value={country} onChange={(e) => setCountry(e.target.value)} maxLength={2} className="w-24 font-mono uppercase" />
              </div>
              <div className="flex-1 space-y-2">
                <label htmlFor="sup-reason" className="text-sm font-medium">{t("dash.suppression.reasonLabel")} <span className="font-mono text-xs text-muted-foreground">{t("dash.suppression.optionalHint")}</span></label>
                <Input id="sup-reason" value={reason} onChange={(e) => setReason(e.target.value)} placeholder={t("dash.suppression.reasonPlaceholder")} />
              </div>
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label htmlFor="sup-text" className="text-sm font-medium">{t("dash.suppression.phoneListLabel")}</label>
                <span className="font-mono text-xs tabular-nums text-muted-foreground">{t("dash.suppression.countSuffix").replace("{n}", () => String(count))}</span>
              </div>
              <Textarea
                id="sup-text"
                value={text}
                onChange={(e) => setText(e.target.value)}
                placeholder={t("dash.suppression.phonesPlaceholder")}
                className="h-32 resize-none font-mono text-sm"
              />
            </div>
          </div>
        )}

        <DialogFooter>
          {report ? (
            <>
              <Button variant="ghost" onClick={reset}>{t("dash.suppression.addMore")}</Button>
              <DialogClose render={<Button />}>{t("dash.suppression.done")}</DialogClose>
            </>
          ) : (
            <>
              <DialogClose render={<Button variant="ghost" />}>{t("dash.suppression.cancel")}</DialogClose>
              <Button onClick={submit} disabled={!valid}>
                <Plus className="size-4" />
                {busy ? t("dash.suppression.submitting") : t("dash.suppression.confirmAdd").replace("{n}", () => String(count))}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
