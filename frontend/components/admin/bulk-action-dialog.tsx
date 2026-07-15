"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
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

export interface BulkActionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Already-translated copy — the caller owns i18n for its own domain. */
  title: string;
  description: string;
  confirmLabel: string;
  /** Renders the confirm button in the destructive (red) style. */
  destructive?: boolean;
  /** Optional input slot rendered between the description and the footer
   *  (e.g. a "assign to" dropdown for bulk sales assignment). */
  children?: React.ReactNode;
  /** Runs the bulk operation. This component only owns the busy/loading
   *  state around the call — it does NOT auto-close on completion. The
   *  caller decides when to close (call onOpenChange(false) once its own
   *  per-item loop + aggregated toast are done), which also lets it keep
   *  the dialog open on a client-side validation failure (e.g. no target
   *  picked in `children`) instead of silently closing. */
  onConfirm: () => Promise<void>;
}

// BulkActionDialog — shared confirm-and-run wrapper for per-item bulk
// operations, generalized from admin-instances.tsx's BulkConfirmDialog
// (bulk logout/delete). Callers loop the selected keys against the existing
// single-item endpoint themselves and report one aggregated toast; this
// component just supplies the confirm UI + busy state around that loop.
export function BulkActionDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  destructive,
  children,
  onConfirm,
}: BulkActionDialogProps) {
  const t = useT();
  const [busy, setBusy] = useState(false);

  async function submit() {
    setBusy(true);
    try {
      await onConfirm();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !busy && onOpenChange(o)}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {children && <div className="py-1">{children}</div>}
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" disabled={busy} />}>{t("table.cancel")}</DialogClose>
          <Button variant={destructive ? "destructive" : "default"} onClick={submit} disabled={busy}>
            {busy ? t("table.processing") : confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
