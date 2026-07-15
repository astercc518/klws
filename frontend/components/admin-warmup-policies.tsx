"use client";

// admin-warmup-policies.tsx — lane-policy config dialog, extracted from
// admin-warmup.tsx (Task 10 re-scope; originally built inline in Task 9).
// Reads/writes the two lane policies (FAST/STANDARD) in one shot: GET
// returns both keyed by lane, PUT saves one lane at a time (per the API's
// own :lane path param), so Save fires up to two sequential requests.
// `Lane`/`LANES`/`LANE_LABEL_KEY` are shared with admin-warmup.tsx (also
// used by its row table + LaneDialog), so they stay exported from there
// and are imported here rather than duplicated.

import { useEffect, useState } from "react";
import { Flame, Sprout } from "lucide-react";
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
import { LANES, LANE_LABEL_KEY, type Lane } from "@/components/admin-warmup";

interface Policy {
  min_warmup_messages: number;
  min_replies: number;
  min_online_hours: number;
  warming_cap: number;
  mature_base_cap: number;
  mature_max_cap: number;
  mature_ramp_step: number;
}

const EMPTY_POLICY: Policy = {
  min_warmup_messages: 0,
  min_replies: 0,
  min_online_hours: 0,
  warming_cap: 0,
  mature_base_cap: 0,
  mature_max_cap: 0,
  mature_ramp_step: 0,
};

export function PoliciesDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useT();
  const [policies, setPolicies] = useState<Partial<Record<Lane, Policy>> | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    api
      .get<Partial<Record<Lane, Policy>>>("/admin/warmup/policies")
      .then(setPolicies)
      .catch(() =>
        toast.error(t("admin.warmup.policy.loadFailedTitle"), { description: t("admin.warmup.retry") }),
      );
  }, [open, t]);

  function setField(lane: Lane, field: keyof Policy, value: number) {
    setPolicies((prev) => ({
      ...prev,
      [lane]: { ...(prev?.[lane] ?? EMPTY_POLICY), [field]: value },
    }));
  }

  async function save() {
    if (!policies) return;
    setBusy(true);
    try {
      for (const l of LANES) {
        const p = policies[l];
        if (p) await api.put(`/admin/warmup/policies/${l}`, p);
      }
      toast.success(t("admin.warmup.policy.saveSuccessTitle"));
      onOpenChange(false);
    } catch (e) {
      toast.error(t("admin.warmup.policy.saveFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.warmup.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  const fields: { key: keyof Policy; labelKey: string }[] = [
    { key: "min_warmup_messages", labelKey: "admin.warmup.policy.minWarmupMessages" },
    { key: "min_replies", labelKey: "admin.warmup.policy.minReplies" },
    { key: "min_online_hours", labelKey: "admin.warmup.policy.minOnlineHours" },
    { key: "warming_cap", labelKey: "admin.warmup.policy.warmingCap" },
    { key: "mature_base_cap", labelKey: "admin.warmup.policy.matureBaseCap" },
    { key: "mature_max_cap", labelKey: "admin.warmup.policy.matureMaxCap" },
    { key: "mature_ramp_step", labelKey: "admin.warmup.policy.matureRampStep" },
  ];

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("admin.warmup.policy.title")}</DialogTitle>
          <DialogDescription>{t("admin.warmup.policy.desc")}</DialogDescription>
        </DialogHeader>
        {!policies ? (
          <div className="py-8 text-center text-sm text-muted-foreground">{t("admin.warmup.policy.loading")}</div>
        ) : (
          <div className="grid grid-cols-1 gap-6 sm:grid-cols-2">
            {LANES.map((l) => (
              <div key={l} className="space-y-2.5 rounded-xl border p-3.5">
                <div className="flex items-center gap-1.5 text-sm font-medium">
                  {l === "FAST" ? <Flame className="size-4" /> : <Sprout className="size-4" />}
                  {t(LANE_LABEL_KEY[l])}
                </div>
                {fields.map((f) => (
                  <label key={f.key} className="flex items-center justify-between gap-2 text-xs">
                    <span className="text-muted-foreground">{t(f.labelKey)}</span>
                    <Input
                      type="number"
                      className="h-7 w-24 font-mono text-xs"
                      value={policies[l]?.[f.key] ?? 0}
                      onChange={(e) => setField(l, f.key, Number(e.target.value))}
                    />
                  </label>
                ))}
              </div>
            ))}
          </div>
        )}
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.warmup.cancel")}</DialogClose>
          <Button onClick={save} disabled={busy || !policies}>
            {busy ? t("admin.warmup.policy.saving") : t("admin.warmup.policy.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
