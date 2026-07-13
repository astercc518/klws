"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Plus, Users } from "lucide-react";
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
import { useT } from "@/components/locale-provider";

// Split non-empty lines / comma-separated entries into a trimmed phone list.
function parsePhones(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

interface Segment {
  id: number;
  name: string;
}

export function NewCampaignDialog({ onDone }: { onDone?: () => void } = {}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  // Recipient source is exactly one of pasted phones XOR a saved segment —
  // mirrors the backend's createCampaignRequest contract (POST /campaigns
  // requires exactly one of phones / segment_id).
  const [source, setSource] = useState<"phones" | "segment">("phones");
  const [phones, setPhones] = useState("");
  const [segments, setSegments] = useState<Segment[] | null>(null);
  const [segmentID, setSegmentID] = useState("");
  const [previewCount, setPreviewCount] = useState<number | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [body, setBody] = useState("");
  const [country, setCountry] = useState("US");
  const [submitting, setSubmitting] = useState(false);

  const list = useMemo(() => parsePhones(phones), [phones]);
  const count = list.length;
  const canSubmit =
    body.trim().length > 0 &&
    country.trim().length === 2 &&
    !submitting &&
    (source === "phones" ? count > 0 : segmentID !== "");

  // Load saved segments the first time "从分段选择" is opened, not eagerly —
  // most sends still paste phones, so skip the round trip until needed.
  useEffect(() => {
    if (source !== "segment" || segments !== null) return;
    api
      .get<Segment[]>("/contacts/segments")
      .then(setSegments)
      .catch(() => setSegments([]));
  }, [source, segments]);

  const loadPreview = useCallback(async (id: string) => {
    if (!id) {
      setPreviewCount(null);
      return;
    }
    setPreviewing(true);
    try {
      const r = await api.get<{ count: number }>(`/contacts/segments/${id}/preview`);
      setPreviewCount(r.count);
    } catch {
      setPreviewCount(null);
    } finally {
      setPreviewing(false);
    }
  }, []);
  useEffect(() => {
    loadPreview(segmentID);
  }, [segmentID, loadPreview]);

  function resetForm() {
    setSource("phones");
    setPhones("");
    setSegmentID("");
    setPreviewCount(null);
    setSegments(null);
    setBody("");
  }

  async function handleSubmit() {
    setSubmitting(true);
    try {
      const payload =
        source === "phones"
          ? { country: country.trim().toUpperCase(), body, phones: list }
          : { country: country.trim().toUpperCase(), body, segment_id: Number(segmentID) };
      await api.post("/campaigns", payload);
      setOpen(false);
      resetForm();
      const sentDesc =
        source === "phones"
          ? t("dash.newCampaign.queuedDescPhones").replace("{n}", () => String(count))
          : t("dash.newCampaign.queuedDescSegment").replace("{n}", () => String(previewCount ?? "—"));
      toast.success(t("dash.newCampaign.queuedTitle"), { description: sentDesc });
      onDone?.();
    } catch (e) {
      // 401 redirects globally; show the backend's message for everything else
      // (e.g. 余额不足 / 未配置单价).
      if (!(e instanceof ApiError && e.status === 401)) {
        toast.error(t("dash.newCampaign.submitFailed"), {
          description: e instanceof ApiError ? e.message : t("dash.newCampaign.submitFailedDesc"),
        });
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) resetForm();
      }}
    >
      <DialogTrigger render={<Button size="lg" className="gap-2" />}>
        <Plus className="size-4" />
        {t("dash.newCampaign.trigger")}
      </DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("dash.newCampaign.title")}</DialogTitle>
          <DialogDescription>
            {t("dash.newCampaign.desc")}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-5 py-1">
          <div className="space-y-2">
            <label htmlFor="country" className="text-sm font-medium">
              {t("dash.newCampaign.countryLabel")} <span className="font-mono text-xs text-muted-foreground">{t("dash.newCampaign.countryHint")}</span>
            </label>
            <Input
              id="country"
              value={country}
              onChange={(e) => setCountry(e.target.value)}
              placeholder="US"
              maxLength={2}
              className="w-24 font-mono uppercase"
            />
          </div>

          <div className="space-y-2">
            <div className="inline-flex rounded-lg border bg-muted/40 p-0.5">
              <button
                type="button"
                onClick={() => setSource("phones")}
                className={
                  "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                  (source === "phones" ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
                }
              >
                {t("dash.newCampaign.sourcePhones")}
              </button>
              <button
                type="button"
                onClick={() => setSource("segment")}
                className={
                  "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                  (source === "segment" ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
                }
              >
                {t("dash.newCampaign.sourceSegment")}
              </button>
            </div>
          </div>

          {source === "phones" ? (
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label htmlFor="phones" className="text-sm font-medium">
                  {t("dash.newCampaign.phonesLabel")}
                </label>
                <span className="font-mono text-xs tabular-nums text-muted-foreground">
                  {t("dash.newCampaign.countSuffix").replace("{n}", () => String(count))}
                </span>
              </div>
              <Textarea
                id="phones"
                value={phones}
                onChange={(e) => setPhones(e.target.value)}
                placeholder={t("dash.newCampaign.phonesPlaceholder")}
                className="h-28 resize-none font-mono text-sm"
              />
            </div>
          ) : (
            <div className="space-y-2">
              <label htmlFor="segment" className="text-sm font-medium">
                {t("dash.newCampaign.segmentLabel")}
              </label>
              {segments === null ? (
                <div className="h-9 animate-pulse rounded-lg bg-muted motion-reduce:animate-none" />
              ) : segments.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  {t("dash.newCampaign.noSegments")}
                </p>
              ) : (
                <select
                  id="segment"
                  value={segmentID}
                  onChange={(e) => setSegmentID(e.target.value)}
                  className="h-9 w-full rounded-lg border bg-transparent px-2.5 text-sm outline-none"
                >
                  <option value="" disabled>{t("dash.newCampaign.chooseSegment")}</option>
                  {segments.map((s) => (
                    <option key={s.id} value={String(s.id)}>{s.name}</option>
                  ))}
                </select>
              )}
              {segmentID && (
                <p className="flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
                  <Users className="size-3.5" />
                  {previewing
                    ? t("dash.newCampaign.previewCalculating")
                    : previewCount !== null
                      ? t("dash.newCampaign.previewCount").replace("{n}", () => String(previewCount))
                      : t("dash.newCampaign.previewFailed")}
                </p>
              )}
            </div>
          )}

          <div className="space-y-2">
            <label htmlFor="body" className="text-sm font-medium">
              {t("dash.newCampaign.bodyLabel")} <span className="font-mono text-xs text-muted-foreground">Spintax</span>
            </label>
            <Textarea
              id="body"
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder={t("dash.newCampaign.bodyPlaceholder")}
              className="h-28 resize-none text-sm"
            />
            <p className="font-mono text-[11px] leading-relaxed text-muted-foreground">
              {t("dash.newCampaign.bodyHint")}
            </p>
          </div>
        </div>

        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("dash.newCampaign.cancel")}</DialogClose>
          <Button onClick={handleSubmit} disabled={!canSubmit}>
            {submitting ? t("dash.newCampaign.submitting") : t("dash.newCampaign.confirmSubmit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
