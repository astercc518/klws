"use client";

// contacts-list.tsx — customer contact library table (Task 8, Step 1).
// Mirrors admin-send-records.tsx's server-mode ProDataTable shape: paginated
// list + status tabs + toolbar filters, backed by GET /api/v1/contacts.

import { useCallback, useEffect, useMemo, useState } from "react";
import { Download, MoreHorizontal, Pencil, Trash2, Tags as TagsIcon } from "lucide-react";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";
import { ImportContactsDialog } from "@/components/dashboard/contacts-import-dialog";
import { useT } from "@/components/locale-provider";

export interface Contact {
  id: number;
  phone: string;
  country_code: string;
  display_name: string;
  status: "active" | "unsubscribed" | "invalid";
  created_at: string;
}
export interface Tag {
  id: number;
  name: string;
  created_at: string;
}

const STATUS_KEY: Record<Contact["status"], { tone: StatusTone; key: string }> = {
  active: { tone: "positive", key: "dash.contacts.status.active" },
  unsubscribed: { tone: "warning", key: "dash.contacts.status.unsubscribed" },
  invalid: { tone: "negative", key: "dash.contacts.status.invalid" },
};
const STATUS_TABS: { key: string; labelKey: string }[] = [
  { key: "", labelKey: "dash.contacts.status.all" },
  { key: "active", labelKey: "dash.contacts.status.active" },
  { key: "unsubscribed", labelKey: "dash.contacts.status.unsubscribed" },
  { key: "invalid", labelKey: "dash.contacts.status.invalid" },
];
const PAGE_SIZE = 15;

export function ContactsList() {
  const t = useT();
  const [rows, setRows] = useState<Contact[] | null>(null);
  const [total, setTotal] = useState(0);
  const [tags, setTags] = useState<Tag[]>([]);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [country, setCountry] = useState("");
  const [tagID, setTagID] = useState("");
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [editTarget, setEditTarget] = useState<Contact | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Contact | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (status) params.set("status", status);
    if (country.trim()) params.set("country", country.trim().toUpperCase());
    if (tagID) params.set("tag_id", tagID);
    try {
      const d = await api.get<{ rows: Contact[]; total: number }>(`/contacts?${params.toString()}`);
      setRows(d.rows);
      setTotal(d.total);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("dash.contacts.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, status, country, tagID, t]);

  useEffect(() => {
    load();
  }, [load]);

  const loadTags = useCallback(async () => {
    try {
      setTags(await api.get<Tag[]>("/contacts/tags"));
    } catch {
      // Filter/apply UI just degrades to "no tags yet" — not fatal for the list.
    }
  }, []);
  useEffect(() => {
    loadTags();
  }, [loadTags]);

  // Clear a selection that no longer matches what's on screen (new page/filter).
  useEffect(() => {
    setSelected(new Set());
  }, [page, query, status, country, tagID]);

  function toggleRow(id: number) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function exportCsv() {
    const params = new URLSearchParams();
    if (query.trim()) params.set("q", query.trim());
    if (status) params.set("status", status);
    if (country.trim()) params.set("country", country.trim().toUpperCase());
    if (tagID) params.set("tag_id", tagID);
    try {
      await api.download(`/contacts/export?${params.toString()}`, "contacts.csv");
    } catch (e) {
      toast.error(t("dash.contacts.exportFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("dash.contacts.retry"),
      });
    }
  }

  async function applyTagToSelected(tag: Tag, remove: boolean) {
    if (selected.size === 0) return;
    try {
      await api.post(`/contacts/tags/${tag.id}/apply`, {
        contact_ids: Array.from(selected),
        remove,
      });
      toast.success(t(remove ? "dash.contacts.tagRemoved" : "dash.contacts.tagAdded"), {
        description: t("dash.contacts.tagAppliedDesc")
          .replace("{tag}", () => tag.name)
          .replace("{n}", () => String(selected.size)),
      });
      setSelected(new Set());
    } catch (e) {
      toast.error(t(remove ? "dash.contacts.tagRemoveFailed" : "dash.contacts.tagApplyFailed"), {
        description: e instanceof ApiError ? e.message : t("dash.contacts.retry"),
      });
    }
  }

  // The list endpoint doesn't return each contact's tags (GET /contacts has no
  // tags field) — only a single tag_id filter. When that filter is active every
  // visible row does carry it, so show it; otherwise there's no per-row tag
  // data to display honestly.
  const activeTagName = useMemo(() => tags.find((t) => String(t.id) === tagID)?.name, [tags, tagID]);

  const allOnPageSelected = rows !== null && rows.length > 0 && rows.every((r) => selected.has(r.id));

  const columns: Column<Contact>[] = [
    {
      key: "select",
      header: (
        <input
          type="checkbox"
          aria-label={t("table.selectAll")}
          checked={allOnPageSelected}
          onChange={(e) => {
            setSelected((prev) => {
              const next = new Set(prev);
              (rows ?? []).forEach((r) => (e.target.checked ? next.add(r.id) : next.delete(r.id)));
              return next;
            });
          }}
          className="size-3.5 accent-brand-600"
        />
      ),
      cell: (r) => (
        <input
          type="checkbox"
          aria-label={t("dash.contacts.selectRowAria").replace("{phone}", () => r.phone)}
          checked={selected.has(r.id)}
          onChange={() => toggleRow(r.id)}
          className="size-3.5 accent-brand-600"
        />
      ),
      headClassName: "w-8",
      cellClassName: "w-8",
    },
    { key: "phone", header: t("dash.contacts.col.phone"), cell: (r) => <span className="font-mono text-sm">{r.phone}</span> },
    {
      key: "country",
      header: t("dash.contacts.col.country"),
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.country_code || "—"}</span>,
    },
    {
      key: "status",
      header: t("dash.contacts.col.status"),
      cell: (r) => {
        const s = STATUS_KEY[r.status];
        return <StatusBadge tone={s.tone}>{t(s.key)}</StatusBadge>;
      },
    },
    {
      key: "tags",
      header: t("dash.contacts.col.tags"),
      cell: () =>
        activeTagName ? (
          <span className="inline-flex h-5.5 w-fit items-center rounded-full bg-muted px-2 text-xs text-muted-foreground">
            {activeTagName}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        ),
    },
    {
      key: "created_at",
      header: t("dash.contacts.col.createdAt"),
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.created_at.slice(0, 19).replace("T", " ")}</span>,
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
          {STATUS_TABS.map((tab) => (
            <button
              key={tab.key || "all"}
              onClick={() => {
                setStatus(tab.key);
                setPage(0);
              }}
              className={
                "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                (status === tab.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
              }
            >
              {t(tab.labelKey)}
            </button>
          ))}
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" className="gap-1.5" onClick={exportCsv}>
            <Download className="size-4" />
            {t("dash.contacts.exportCsv")}
          </Button>
          <ImportContactsDialog onDone={load} />
        </div>
      </div>

      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(r) => r.id}
        emptyState={t("dash.contacts.empty")}
        search={{ placeholder: t("dash.contacts.searchPlaceholder"), accessor: () => "" }}
        toolbar={
          <div className="flex flex-wrap items-center gap-2">
            <Input
              value={country}
              onChange={(e) => {
                setCountry(e.target.value);
                setPage(0);
              }}
              placeholder={t("dash.contacts.countryPlaceholder")}
              maxLength={2}
              className="h-8 w-24 font-mono uppercase"
              aria-label={t("dash.contacts.countryFilterAria")}
            />
            <select
              value={tagID}
              onChange={(e) => {
                setTagID(e.target.value);
                setPage(0);
              }}
              className="h-8 rounded-lg border bg-transparent px-2 text-sm"
              aria-label={t("dash.contacts.tagFilterAria")}
            >
              <option value="">{t("dash.contacts.allTags")}</option>
              {tags.map((tag) => (
                <option key={tag.id} value={String(tag.id)}>
                  {tag.name}
                </option>
              ))}
            </select>
            {selected.size > 0 && (
              <DropdownMenu>
                <DropdownMenuTrigger render={<Button variant="outline" size="sm" className="gap-1.5" />}>
                  <TagsIcon className="size-4" />
                  {t("dash.contacts.batchTag").replace("{n}", () => String(selected.size))}
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  {tags.length === 0 ? (
                    <DropdownMenuLabel className="text-muted-foreground">{t("dash.contacts.noTagsYet")}</DropdownMenuLabel>
                  ) : (
                    <>
                      <DropdownMenuLabel>{t("dash.contacts.addTagLabel")}</DropdownMenuLabel>
                      {tags.map((tag) => (
                        <DropdownMenuItem key={`add-${tag.id}`} onClick={() => applyTagToSelected(tag, false)}>
                          {tag.name}
                        </DropdownMenuItem>
                      ))}
                      <DropdownMenuSeparator />
                      <DropdownMenuLabel>{t("dash.contacts.removeTagLabel")}</DropdownMenuLabel>
                      {tags.map((tag) => (
                        <DropdownMenuItem key={`rm-${tag.id}`} variant="destructive" onClick={() => applyTagToSelected(tag, true)}>
                          {tag.name}
                        </DropdownMenuItem>
                      ))}
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </div>
        }
        rowActions={(r) => (
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t("dash.contacts.rowActionsAria")} />}>
              <MoreHorizontal className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => setEditTarget(r)}>
                <Pencil className="size-4" />
                {t("dash.contacts.editAction")}
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onClick={() => setDeleteTarget(r)}>
                <Trash2 className="size-4" />
                {t("dash.contacts.deleteAction")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query,
          onQueryChange: (q) => {
            setQuery(q);
            setPage(0);
          },
          loading,
        }}
      />

      <EditContactDialog target={editTarget} onClose={() => setEditTarget(null)} onDone={load} />
      <DeleteContactDialog target={deleteTarget} onClose={() => setDeleteTarget(null)} onDone={load} />
    </div>
  );
}

function EditContactDialog({
  target,
  onClose,
  onDone,
}: {
  target: Contact | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [displayName, setDisplayName] = useState("");
  const [status, setStatus] = useState<Contact["status"]>("active");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target) {
      setDisplayName(target.display_name);
      setStatus(target.status);
    }
  }, [target]);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.put(`/contacts/${target.id}`, { display_name: displayName, status });
      toast.success(t("dash.contacts.updated"), { description: target.phone });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("dash.contacts.updateFailed"), { description: e instanceof ApiError ? e.message : t("dash.contacts.retry") });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("dash.contacts.editTitle")}</DialogTitle>
          <DialogDescription>
            {t("dash.contacts.editDescPrefix")}
            <span className="font-mono text-xs">{target?.phone}</span>
            {t("dash.contacts.editDescSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="edit-contact-name" className="text-sm font-medium">{t("dash.contacts.displayNameLabel")}</label>
            <Input id="edit-contact-name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
          </div>
          <div className="space-y-2">
            <label htmlFor="edit-contact-status" className="text-sm font-medium">{t("dash.contacts.col.status")}</label>
            <select
              id="edit-contact-status"
              value={status}
              onChange={(e) => setStatus(e.target.value as Contact["status"])}
              className="h-9 w-full rounded-lg border bg-transparent px-2.5 text-sm outline-none"
            >
              <option value="active">{t("dash.contacts.status.active")}</option>
              <option value="unsubscribed">{t("dash.contacts.status.unsubscribed")}</option>
              <option value="invalid">{t("dash.contacts.status.invalid")}</option>
            </select>
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("dash.contacts.cancel")}</DialogClose>
          <Button onClick={submit} disabled={busy}>{busy ? t("dash.contacts.saving") : t("dash.contacts.confirmSave")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeleteContactDialog({
  target,
  onClose,
  onDone,
}: {
  target: Contact | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.delete(`/contacts/${target.id}`);
      toast.success(t("dash.contacts.deleted"), { description: target.phone });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("dash.contacts.deleteFailed"), { description: e instanceof ApiError ? e.message : t("dash.contacts.retry") });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("dash.contacts.deleteTitle")}</DialogTitle>
          <DialogDescription>
            {t("dash.contacts.deleteDescPrefix")}
            <span className="font-mono text-xs">{target?.phone}</span>
            {t("dash.contacts.deleteDescSuffix")}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("dash.contacts.cancel")}</DialogClose>
          <Button variant="destructive" onClick={submit} disabled={busy}>
            {busy ? t("dash.contacts.deleting") : t("dash.contacts.confirmDelete")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
