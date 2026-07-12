"use client";

// contacts-import-dialog.tsx — bulk import for the contact library (Task 8,
// Step 2). Paste box (+ optional file, read client-side into the same
// textarea) + country select → POST /contacts/import → inline report.

import { useRef, useState } from "react";
import { Plus, Upload } from "lucide-react";
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

interface ImportReport {
  batch_id: number;
  total: number;
  inserted: number;
  duplicates: number;
  invalid: number;
}

function lineCount(raw: string): number {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean).length;
}

export function ImportContactsDialog({ onDone }: { onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [filename, setFilename] = useState("");
  const [country, setCountry] = useState("US");
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<ImportReport | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const count = lineCount(text);
  const valid = count > 0 && country.trim().length === 2 && !busy;

  function reset() {
    setText("");
    setFilename("");
    setReport(null);
  }

  async function handleFile(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    setFilename(f.name);
    const content = await f.text();
    setText((prev) => (prev.trim() ? `${prev.trim()}\n${content}` : content));
    e.target.value = "";
  }

  async function submit() {
    setBusy(true);
    try {
      const rep = await api.post<ImportReport>("/contacts/import", {
        country: country.trim().toUpperCase(),
        text,
        filename: filename || undefined,
      });
      setReport(rep);
      onDone();
    } catch (e) {
      toast.error("导入失败", { description: e instanceof ApiError ? e.message : "请重试" });
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
        <Plus className="size-4" />
        导入联系人
      </DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>批量导入联系人</DialogTitle>
          <DialogDescription>
            每行一个号码,或用逗号分隔;也可选择一个文本/CSV 文件,内容会追加到粘贴框。
          </DialogDescription>
        </DialogHeader>

        {report ? (
          <div className="space-y-3 py-1">
            <div className="grid grid-cols-4 gap-2 text-center">
              <ReportTile label="总数" value={report.total} />
              <ReportTile label="新增" value={report.inserted} tone="positive" />
              <ReportTile label="重复" value={report.duplicates} tone="warning" />
              <ReportTile label="无效" value={report.invalid} tone="negative" />
            </div>
            <p className="font-mono text-[11px] text-muted-foreground">批次号 #{report.batch_id}</p>
          </div>
        ) : (
          <div className="space-y-4 py-1">
            <div className="space-y-2">
              <label htmlFor="import-country" className="text-sm font-medium">
                目标国家 <span className="font-mono text-xs text-muted-foreground">ISO-2,用于号码归一化</span>
              </label>
              <Input
                id="import-country"
                value={country}
                onChange={(e) => setCountry(e.target.value)}
                placeholder="US"
                maxLength={2}
                className="w-24 font-mono uppercase"
              />
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label htmlFor="import-text" className="text-sm font-medium">号码列表</label>
                <span className="font-mono text-xs tabular-nums text-muted-foreground">{count} 个号码</span>
              </div>
              <Textarea
                id="import-text"
                value={text}
                onChange={(e) => setText(e.target.value)}
                placeholder={"每行一个号码,或用逗号分隔\n+8613800000000\n+8613900000000"}
                className="h-40 resize-none font-mono text-sm"
              />
              <div className="flex items-center gap-2">
                <input
                  ref={fileRef}
                  type="file"
                  accept=".csv,.txt"
                  className="hidden"
                  onChange={handleFile}
                />
                <Button type="button" variant="outline" size="sm" className="gap-1.5" onClick={() => fileRef.current?.click()}>
                  <Upload className="size-3.5" />
                  选择文件
                </Button>
                {filename && <span className="truncate text-xs text-muted-foreground">{filename}</span>}
              </div>
            </div>
          </div>
        )}

        <DialogFooter>
          {report ? (
            <>
              <Button variant="ghost" onClick={reset}>再导入一批</Button>
              <DialogClose render={<Button />}>完成</DialogClose>
            </>
          ) : (
            <>
              <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
              <Button onClick={submit} disabled={!valid}>
                {busy ? "导入中…" : `确认导入 ${count} 条`}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ReportTile({ label, value, tone }: { label: string; value: number; tone?: "positive" | "warning" | "negative" }) {
  const toneCls =
    tone === "positive"
      ? "text-emerald-600 dark:text-emerald-400"
      : tone === "warning"
        ? "text-amber-600 dark:text-amber-400"
        : tone === "negative"
          ? "text-rose-600 dark:text-rose-400"
          : "text-foreground";
  return (
    <div className="rounded-lg border bg-muted/30 py-3">
      <div className={`font-mono text-xl font-semibold tabular-nums ${toneCls}`}>{value}</div>
      <div className="mt-0.5 text-xs text-muted-foreground">{label}</div>
    </div>
  );
}
