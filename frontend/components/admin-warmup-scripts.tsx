"use client";

// admin-warmup-scripts.tsx — 脚本库管理 dialog (Task 18), triggered from
// admin-warmup.tsx next to the lane-policy dialog (PoliciesDialog in
// admin-warmup-policies.tsx). Lists all scripts (including disabled ones,
// unlike the warmup engine's own LoadScripts which only sees enabled=true),
// lets an admin enable/disable/delete a script, and create a new multi-turn
// script (lang + >=2 alternating A/B turns) that the warmup engine can pick
// up on its next tick.

import { useEffect, useState } from "react";
import { BookOpen, Plus, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { StatusBadge } from "@/components/admin/status-badge";
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

interface ScriptTurn {
  from: string;
  text: string;
}

interface ScriptRow {
  id: number;
  lang: string;
  turns: ScriptTurn[];
  enabled: boolean;
}

const EMPTY_TURNS: ScriptTurn[] = [
  { from: "A", text: "" },
  { from: "B", text: "" },
];

export function ScriptsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useT();
  const [scripts, setScripts] = useState<ScriptRow[] | null>(null);
  const [busyID, setBusyID] = useState<number | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ScriptRow | null>(null);

  const [lang, setLang] = useState("");
  const [turns, setTurns] = useState<ScriptTurn[]>(EMPTY_TURNS);
  const [creating, setCreating] = useState(false);

  const load = () => {
    api
      .get<{ rows: ScriptRow[] }>("/admin/warmup/scripts")
      .then((d) => setScripts(d.rows ?? []))
      .catch(() =>
        toast.error(t("admin.warmup.script.loadFailedTitle"), { description: t("admin.warmup.retry") }),
      );
  };

  useEffect(() => {
    if (!open) return;
    load();
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  async function toggleEnabled(row: ScriptRow) {
    setBusyID(row.id);
    try {
      await api.put(`/admin/warmup/scripts/${row.id}`, { enabled: !row.enabled });
      load();
    } catch (e) {
      toast.error(t("admin.warmup.script.setEnabledFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.warmup.retry"),
      });
    } finally {
      setBusyID(null);
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return;
    setBusyID(deleteTarget.id);
    try {
      await api.delete(`/admin/warmup/scripts/${deleteTarget.id}`);
      toast.success(t("admin.warmup.script.deleteSuccessTitle"));
      setDeleteTarget(null);
      load();
    } catch (e) {
      toast.error(t("admin.warmup.script.deleteFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.warmup.retry"),
      });
    } finally {
      setBusyID(null);
    }
  }

  function setTurnField(idx: number, field: keyof ScriptTurn, value: string) {
    setTurns((prev) => prev.map((tn, i) => (i === idx ? { ...tn, [field]: value } : tn)));
  }

  function addTurn() {
    const nextFrom = turns.length % 2 === 0 ? "A" : "B";
    setTurns((prev) => [...prev, { from: nextFrom, text: "" }]);
  }

  function removeTurn(idx: number) {
    setTurns((prev) => (prev.length <= 2 ? prev : prev.filter((_, i) => i !== idx)));
  }

  async function createScript() {
    if (!lang.trim()) {
      toast.error(t("admin.warmup.script.langRequiredError"));
      return;
    }
    const cleanTurns = turns.filter((tn) => tn.text.trim());
    if (cleanTurns.length < 2) {
      toast.error(t("admin.warmup.script.minTurnsError"));
      return;
    }
    setCreating(true);
    try {
      await api.post("/admin/warmup/scripts", { lang: lang.trim(), turns: cleanTurns });
      toast.success(t("admin.warmup.script.createSuccessTitle"));
      setLang("");
      setTurns(EMPTY_TURNS);
      load();
    } catch (e) {
      toast.error(t("admin.warmup.script.createFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.warmup.retry"),
      });
    } finally {
      setCreating(false);
    }
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t("admin.warmup.script.title")}</DialogTitle>
            <DialogDescription>{t("admin.warmup.script.desc")}</DialogDescription>
          </DialogHeader>

          <div className="max-h-64 space-y-2 overflow-y-auto">
            {!scripts ? (
              <div className="py-6 text-center text-sm text-muted-foreground">
                {t("admin.warmup.script.loading")}
              </div>
            ) : scripts.length === 0 ? (
              <div className="py-6 text-center text-sm text-muted-foreground">
                {t("admin.warmup.script.emptyState")}
              </div>
            ) : (
              scripts.map((sc) => (
                <div key={sc.id} className="flex items-center justify-between gap-3 rounded-lg border p-2.5">
                  <div className="min-w-0 flex-1 space-y-1">
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-xs font-medium">{sc.lang}</span>
                      <StatusBadge tone={sc.enabled ? "positive" : "neutral"}>
                        {sc.enabled ? t("admin.warmup.script.enabledYes") : t("admin.warmup.script.enabledNo")}
                      </StatusBadge>
                      <span className="text-[10px] text-muted-foreground">
                        {t("admin.warmup.script.turnsCount").replace("{n}", String(sc.turns.length))}
                      </span>
                    </div>
                    <p className="truncate text-xs text-muted-foreground">
                      {sc.turns.map((tn) => `${tn.from}: ${tn.text}`).join("  →  ")}
                    </p>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={busyID === sc.id}
                      onClick={() => toggleEnabled(sc)}
                    >
                      {sc.enabled ? t("admin.warmup.script.disable") : t("admin.warmup.script.enable")}
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={t("admin.warmup.script.delete")}
                      disabled={busyID === sc.id}
                      onClick={() => setDeleteTarget(sc)}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                </div>
              ))
            )}
          </div>

          <div className="space-y-3 rounded-lg border p-3">
            <div className="flex items-center gap-1.5 text-sm font-medium">
              <Plus className="size-4" />
              {t("admin.warmup.script.createTitle")}
            </div>
            <label className="flex items-center gap-2 text-xs">
              <span className="w-16 shrink-0 text-muted-foreground">{t("admin.warmup.script.langLabel")}</span>
              <Input
                value={lang}
                onChange={(e) => setLang(e.target.value)}
                placeholder={t("admin.warmup.script.langPlaceholder")}
                className="h-8 max-w-32 font-mono text-xs"
              />
            </label>
            <div className="space-y-2">
              {turns.map((tn, idx) => (
                <div key={idx} className="flex items-center gap-2">
                  <select
                    value={tn.from}
                    onChange={(e) => setTurnField(idx, "from", e.target.value)}
                    className="h-8 w-14 shrink-0 rounded-md border bg-transparent px-1 text-xs"
                    aria-label={t("admin.warmup.script.turnFrom")}
                  >
                    <option value="A">A</option>
                    <option value="B">B</option>
                  </select>
                  <Textarea
                    value={tn.text}
                    onChange={(e) => setTurnField(idx, "text", e.target.value)}
                    placeholder={t("admin.warmup.script.turnTextPlaceholder")}
                    className="h-8 min-h-8 flex-1 py-1.5 text-xs"
                  />
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t("admin.warmup.script.removeTurn")}
                    disabled={turns.length <= 2}
                    onClick={() => removeTurn(idx)}
                  >
                    <X className="size-4" />
                  </Button>
                </div>
              ))}
            </div>
            <Button variant="outline" size="sm" className="gap-1.5" onClick={addTurn}>
              <Plus className="size-3.5" />
              {t("admin.warmup.script.addTurn")}
            </Button>
          </div>

          <DialogFooter>
            <DialogClose render={<Button variant="ghost" />}>{t("admin.warmup.cancel")}</DialogClose>
            <Button onClick={createScript} disabled={creating} className="gap-1.5">
              <BookOpen className="size-4" />
              {creating ? t("admin.warmup.script.creating") : t("admin.warmup.script.create")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteTarget != null} onOpenChange={(o) => !o && setDeleteTarget(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("admin.warmup.script.deleteConfirmTitle")}</DialogTitle>
            <DialogDescription>
              {t("admin.warmup.script.deleteConfirmDesc")}
              <span className="font-mono text-xs">{deleteTarget?.lang}</span>
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose render={<Button variant="ghost" />}>{t("admin.warmup.cancel")}</DialogClose>
            <Button variant="destructive" onClick={confirmDelete} disabled={busyID === deleteTarget?.id}>
              {busyID === deleteTarget?.id
                ? t("admin.warmup.script.deleting")
                : t("admin.warmup.script.deleteConfirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
