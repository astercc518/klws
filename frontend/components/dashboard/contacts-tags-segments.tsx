"use client";

// contacts-tags-segments.tsx — tag management + segment builder (Task 8,
// Step 3). One shared `tags` fetch feeds both panels since they're rendered
// together on the same tab.

import { useCallback, useEffect, useState } from "react";
import { Plus, Tag as TagIcon, Trash2, Users } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { Tag } from "@/components/dashboard/contacts-list";

interface SegmentFilter {
  tags?: number[];
  country?: string;
  status?: string;
}
interface Segment {
  id: number;
  name: string;
  filter: SegmentFilter;
  created_at: string;
}

const STATUS_OPTIONS = [
  { value: "", label: "任意状态" },
  { value: "active", label: "有效" },
  { value: "unsubscribed", label: "已退订" },
  { value: "invalid", label: "无效" },
];

export function ContactsTagsSegments() {
  const [tags, setTags] = useState<Tag[]>([]);
  const [tagsLoaded, setTagsLoaded] = useState(false);
  const [deleteTagTarget, setDeleteTagTarget] = useState<Tag | null>(null);

  const loadTags = useCallback(async () => {
    try {
      setTags(await api.get<Tag[]>("/contacts/tags"));
    } catch (e) {
      toast.error("加载标签失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setTagsLoaded(true);
    }
  }, []);
  useEffect(() => {
    loadTags();
  }, [loadTags]);

  return (
    <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>标签管理</CardTitle>
          <CardDescription>为联系人打标签,便于按标签筛选与建群发。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <CreateTagForm onCreated={loadTags} />
          <div className="flex flex-wrap gap-2">
            {!tagsLoaded ? (
              <div className="h-6 w-full animate-pulse rounded bg-muted motion-reduce:animate-none" />
            ) : tags.length === 0 ? (
              <p className="text-sm text-muted-foreground">还没有标签,先创建一个。</p>
            ) : (
              tags.map((t) => (
                <span
                  key={t.id}
                  className="inline-flex items-center gap-1.5 rounded-full border bg-muted/40 py-1 pl-2.5 pr-1.5 text-xs font-medium"
                >
                  <TagIcon className="size-3 text-muted-foreground" />
                  {t.name}
                  <button
                    type="button"
                    aria-label={`删除标签 ${t.name}`}
                    onClick={() => setDeleteTagTarget(t)}
                    className="rounded-full p-0.5 text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
                  >
                    <Trash2 className="size-3" />
                  </button>
                </span>
              ))
            )}
          </div>
        </CardContent>
      </Card>

      <SegmentBuilder tags={tags} />

      <DeleteTagDialog target={deleteTagTarget} onClose={() => setDeleteTagTarget(null)} onDone={loadTags} />
    </div>
  );
}

function CreateTagForm({ onCreated }: { onCreated: () => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!name.trim()) return;
    setBusy(true);
    try {
      await api.post("/contacts/tags", { name: name.trim() });
      setName("");
      onCreated();
    } catch (e) {
      toast.error("创建标签失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex gap-2">
      <Input
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && submit()}
        placeholder="新标签名称,如 VIP / 待激活"
        className="flex-1"
      />
      <Button size="sm" className="gap-1.5" onClick={submit} disabled={!name.trim() || busy}>
        <Plus className="size-4" />
        新建
      </Button>
    </div>
  );
}

function DeleteTagDialog({ target, onClose, onDone }: { target: Tag | null; onClose: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.delete(`/contacts/tags/${target.id}`);
      toast.success("标签已删除", { description: target.name });
      onClose();
      onDone();
    } catch (e) {
      toast.error("删除失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>删除标签</DialogTitle>
          <DialogDescription>
            确认删除标签 <span className="font-mono text-xs">{target?.name}</span>
            ?已打上该标签的联系人会自动解除关联,联系人本身不受影响。此操作不可撤销。
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? "删除中…" : "确认删除"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Segment builder — pick tag(s)/country/status, save, then show a live count.
//
// The backend only exposes a preview count for an *existing* segment
// (GET /contacts/segments/:id/preview) — there is no dry-run endpoint to
// preview before saving. So "live preview" here means: save, then
// immediately preview; existing segments in the list get their count
// fetched the same way as soon as they load.
// ---------------------------------------------------------------------------

function SegmentBuilder({ tags }: { tags: Tag[] }) {
  const [segments, setSegments] = useState<Segment[] | null>(null);
  const [counts, setCounts] = useState<Record<number, number | "loading" | null>>({});
  const [name, setName] = useState("");
  const [selectedTags, setSelectedTags] = useState<Set<number>>(new Set());
  const [country, setCountry] = useState("");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Segment | null>(null);

  const previewOne = useCallback(async (segID: number) => {
    setCounts((prev) => ({ ...prev, [segID]: "loading" }));
    try {
      const r = await api.get<{ count: number }>(`/contacts/segments/${segID}/preview`);
      setCounts((prev) => ({ ...prev, [segID]: r.count }));
    } catch {
      setCounts((prev) => ({ ...prev, [segID]: null }));
    }
  }, []);

  const load = useCallback(async () => {
    try {
      const list = await api.get<Segment[]>("/contacts/segments");
      setSegments(list);
      list.forEach((s) => previewOne(s.id));
    } catch (e) {
      toast.error("加载分段失败", { description: e instanceof ApiError ? e.message : "请重试" });
    }
  }, [previewOne]);
  useEffect(() => {
    load();
  }, [load]);

  function toggleTag(id: number) {
    setSelectedTags((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function submit() {
    if (!name.trim()) return;
    setBusy(true);
    try {
      const filter: SegmentFilter = {};
      if (selectedTags.size > 0) filter.tags = Array.from(selectedTags);
      if (country.trim()) filter.country = country.trim().toUpperCase();
      if (status) filter.status = status;
      const created = await api.post<Segment>("/contacts/segments", { name: name.trim(), filter });
      setName("");
      setSelectedTags(new Set());
      setCountry("");
      setStatus("");
      toast.success("分段已保存", { description: created.name });
      await load();
    } catch (e) {
      toast.error("保存分段失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>分段构建器</CardTitle>
        <CardDescription>按标签 / 国家 / 状态圈选一批联系人,保存后可在建群发时直接选用。</CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="space-y-3">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="分段名称,如 US-VIP-有效" />
          {tags.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {tags.map((t) => {
                const on = selectedTags.has(t.id);
                return (
                  <button
                    key={t.id}
                    type="button"
                    onClick={() => toggleTag(t.id)}
                    className={
                      "rounded-full border px-2.5 py-1 text-xs font-medium transition-colors " +
                      (on ? "border-brand-600 bg-brand-600/10 text-brand-700 dark:text-brand-400" : "text-muted-foreground hover:text-foreground")
                    }
                  >
                    {t.name}
                  </button>
                );
              })}
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            <Input
              value={country}
              onChange={(e) => setCountry(e.target.value)}
              placeholder="国家 ISO-2(可选)"
              maxLength={2}
              className="w-32 font-mono uppercase"
            />
            <select
              value={status}
              onChange={(e) => setStatus(e.target.value)}
              className="h-8 rounded-lg border bg-transparent px-2 text-sm"
            >
              {STATUS_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
            <Button size="sm" className="gap-1.5" onClick={submit} disabled={!name.trim() || busy}>
              <Plus className="size-4" />
              {busy ? "保存中…" : "保存分段"}
            </Button>
          </div>
        </div>

        <div className="space-y-2 border-t pt-4">
          {segments === null ? (
            <div className="h-16 animate-pulse rounded bg-muted motion-reduce:animate-none" />
          ) : segments.length === 0 ? (
            <p className="text-sm text-muted-foreground">还没有已保存的分段。</p>
          ) : (
            segments.map((s) => {
              const c = counts[s.id];
              return (
                <div key={s.id} className="flex items-center justify-between gap-2 rounded-lg border px-3 py-2">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{s.name}</div>
                    <div className="mt-0.5 flex items-center gap-1 font-mono text-xs text-muted-foreground">
                      <Users className="size-3" />
                      {c === "loading" || c === undefined ? "计算中…" : c === null ? "预览失败" : `预计 ${c} 个联系人`}
                    </div>
                  </div>
                  <Button variant="ghost" size="icon-sm" aria-label="删除分段" onClick={() => setDeleteTarget(s)}>
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              );
            })
          )}
        </div>
      </CardContent>

      <DeleteSegmentDialog target={deleteTarget} onClose={() => setDeleteTarget(null)} onDone={load} />
    </Card>
  );
}

function DeleteSegmentDialog({ target, onClose, onDone }: { target: Segment | null; onClose: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.delete(`/contacts/segments/${target.id}`);
      toast.success("分段已删除", { description: target.name });
      onClose();
      onDone();
    } catch (e) {
      toast.error("删除失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>删除分段</DialogTitle>
          <DialogDescription>
            确认删除分段 <span className="font-mono text-xs">{target?.name}</span>
            ?依赖此分段建群发的历史任务不受影响,但之后无法再用它建新任务。此操作不可撤销。
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? "删除中…" : "确认删除"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
