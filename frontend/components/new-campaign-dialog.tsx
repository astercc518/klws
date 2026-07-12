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
        source === "phones" ? `${count} 个号码已提交,后台将按受控速率发送` : `分段预计 ${previewCount ?? "—"} 个号码已提交`;
      toast.success("群发任务已进入调度队列", { description: sentDesc });
      onDone?.();
    } catch (e) {
      // 401 redirects globally; show the backend's message for everything else
      // (e.g. 余额不足 / 未配置单价).
      if (!(e instanceof ApiError && e.status === 401)) {
        toast.error("提交失败", {
          description: e instanceof ApiError ? e.message : "请稍后重试",
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
        新建群发
      </DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>新建群发任务</DialogTitle>
          <DialogDescription>
            粘贴收件号码,或从已保存的分段选取,写好文案后提交将按受控速率分散发送。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-5 py-1">
          <div className="space-y-2">
            <label htmlFor="country" className="text-sm font-medium">
              目标国家 <span className="font-mono text-xs text-muted-foreground">ISO-2,用于定价</span>
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
                粘贴号码
              </button>
              <button
                type="button"
                onClick={() => setSource("segment")}
                className={
                  "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                  (source === "segment" ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
                }
              >
                从分段选择
              </button>
            </div>
          </div>

          {source === "phones" ? (
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label htmlFor="phones" className="text-sm font-medium">
                  收件号码
                </label>
                <span className="font-mono text-xs tabular-nums text-muted-foreground">
                  {count} 个号码
                </span>
              </div>
              <Textarea
                id="phones"
                value={phones}
                onChange={(e) => setPhones(e.target.value)}
                placeholder={"每行一个号码,或用逗号分隔\n+8613800000000\n+8613900000000"}
                className="h-28 resize-none font-mono text-sm"
              />
            </div>
          ) : (
            <div className="space-y-2">
              <label htmlFor="segment" className="text-sm font-medium">
                收件分段
              </label>
              {segments === null ? (
                <div className="h-9 animate-pulse rounded-lg bg-muted motion-reduce:animate-none" />
              ) : segments.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  还没有已保存的分段,请先到「联系人 → 标签与分段」创建一个。
                </p>
              ) : (
                <select
                  id="segment"
                  value={segmentID}
                  onChange={(e) => setSegmentID(e.target.value)}
                  className="h-9 w-full rounded-lg border bg-transparent px-2.5 text-sm outline-none"
                >
                  <option value="" disabled>选择分段…</option>
                  {segments.map((s) => (
                    <option key={s.id} value={String(s.id)}>{s.name}</option>
                  ))}
                </select>
              )}
              {segmentID && (
                <p className="flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
                  <Users className="size-3.5" />
                  {previewing ? "计算预计发送量…" : previewCount !== null ? `预计发送量 ${previewCount} 个号码` : "预览失败"}
                </p>
              )}
            </div>
          )}

          <div className="space-y-2">
            <label htmlFor="body" className="text-sm font-medium">
              营销文案 <span className="font-mono text-xs text-muted-foreground">Spintax</span>
            </label>
            <Textarea
              id="body"
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder={"{Hi|Hello} {{name}}, 限时优惠 {今天|本周} 截止…"}
              className="h-28 resize-none text-sm"
            />
            <p className="font-mono text-[11px] leading-relaxed text-muted-foreground">
              {"{a|b} 随机取一 · {{name}} 变量替换 · 按消息做种,重试结果一致"}
            </p>
          </div>
        </div>

        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={handleSubmit} disabled={!canSubmit}>
            {submitting ? "提交中…" : "确认发送"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
