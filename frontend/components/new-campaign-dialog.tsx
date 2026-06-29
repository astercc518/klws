"use client";

import { useMemo, useState } from "react";
import { Plus } from "lucide-react";
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

export function NewCampaignDialog() {
  const [open, setOpen] = useState(false);
  const [phones, setPhones] = useState("");
  const [body, setBody] = useState("");
  const [country, setCountry] = useState("US");
  const [submitting, setSubmitting] = useState(false);

  const list = useMemo(() => parsePhones(phones), [phones]);
  const count = list.length;
  const canSubmit =
    count > 0 && body.trim().length > 0 && country.trim().length === 2 && !submitting;

  async function handleSubmit() {
    setSubmitting(true);
    try {
      await api.post("/campaigns", {
        country: country.trim().toUpperCase(),
        body,
        phones: list,
      });
      setOpen(false);
      setPhones("");
      setBody("");
      toast.success("群发任务已进入调度队列", {
        description: `${count} 个号码已提交,后台将按受控速率发送`,
      });
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
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button size="lg" className="gap-2" />}>
        <Plus className="size-4" />
        新建群发
      </DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>新建群发任务</DialogTitle>
          <DialogDescription>
            粘贴收件号码并写好文案,提交后将按受控速率分散发送。
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
