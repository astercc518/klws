"use client";

// admin-instance-wizard.tsx — P2 T7: the "接入新账号" QR pairing wizard,
// launched from admin-instances.tsx's toolbar button.
//
// Two steps:
//   1. form — pick a tenant + country code, POST /admin/instances.
//   2. qr   — GET the instance's QR (base64 PNG) and poll GET .../state every
//      POLL_INTERVAL_MS until Evolution reports the session connected, or
//      POLL_TIMEOUT_MS elapses (retry button re-fetches the QR + restarts the
//      poll).
//
// All three timers (poll interval, give-up timeout, post-success auto-close)
// are tracked in refs and torn down via clearTimers() on close/unmount/success
// so no interval survives the dialog.

import { useCallback, useEffect, useRef, useState } from "react";
import { CheckCircle2, Loader2, QrCode, RefreshCw } from "lucide-react";
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

interface Tenant {
  id: number;
  name: string;
  status: string;
}

interface CreateInstanceResponse {
  instance_name: string;
  evo_node: string;
  proxy_id: number | null;
  state: string;
}

const POLL_INTERVAL_MS = 2500;
const POLL_TIMEOUT_MS = 90_000;
// Evolution's live FetchState passthrough — "open" is Evolution's own
// vocabulary, "connected" mirrors this app's InstanceState. Accept either
// until T8's real-device pass confirms which one actually comes back.
const CONNECTED_STATES = new Set(["connected", "open"]);

// Evolution's QR payload shape is unconfirmed until T8's real-device check —
// some deployments may already return a full data URI, others a bare base64
// string. Accept either instead of double-prefixing. An empty payload
// (Evolution returns "" from ConnectInstance when the instance is already
// connected — see cluster/evolution_client.go) yields null so the render
// layer skips <img> instead of emitting a broken "data:image/png;base64,".
function toDataUri(raw: string | undefined | null): string | null {
  if (!raw) return null;
  return raw.startsWith("data:") ? raw : `data:image/png;base64,${raw}`;
}

type Step = "form" | "qr";

export function AdminInstanceWizard({
  open,
  onOpenChange,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSuccess: () => void;
}) {
  const t = useT();
  const [step, setStep] = useState<Step>("form");

  // Step 1: form.
  const [tenants, setTenants] = useState<Tenant[] | null>(null);
  const [tenantId, setTenantId] = useState("");
  const [countryCode, setCountryCode] = useState("US");
  const [creating, setCreating] = useState(false);

  // Step 2: QR + poll.
  const [instanceName, setInstanceName] = useState<string | null>(null);
  const [qrImage, setQrImage] = useState<string | null>(null);
  const [qrLoading, setQrLoading] = useState(false);
  const [qrError, setQrError] = useState<string | null>(null);
  const [connected, setConnected] = useState(false);
  const [timedOut, setTimedOut] = useState(false);

  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const giveUpRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const closeRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Re-entrancy guards for the poll interval: inFlightRef prevents a second
  // GET /state from firing while the previous one is still pending (state
  // slower than POLL_INTERVAL_MS), and succeededRef makes the connected
  // branch fire exactly once even if two overlapping ticks both see "open".
  const inFlightRef = useRef(false);
  const succeededRef = useRef(false);
  // Tracks the current QR image for the poll interval's closure. The QR arrives
  // asynchronously via Evolution's qrcode.updated webhook (the backend caches it
  // and GET /qr reads the cache), so the first GET /qr right after create is
  // almost always empty — the poll keeps re-fetching /qr until the cached QR
  // shows up, instead of failing on that first empty read.
  const qrImageRef = useRef<string | null>(null);

  const clearTimers = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
    if (giveUpRef.current) {
      clearTimeout(giveUpRef.current);
      giveUpRef.current = null;
    }
    if (closeRef.current) {
      clearTimeout(closeRef.current);
      closeRef.current = null;
    }
  }, []);

  // resetState clears the wizard back to its initial step/form values. Called
  // from the Dialog's onOpenChange (below) rather than from a `!open` effect
  // branch — an effect body must not call setState synchronously (cascading
  // renders), so the reset lives in the event handler that actually closes
  // the dialog instead.
  const resetState = useCallback(() => {
    clearTimers();
    succeededRef.current = false;
    inFlightRef.current = false;
    qrImageRef.current = null;
    setStep("form");
    setTenantId("");
    setCountryCode("US");
    setInstanceName(null);
    setQrImage(null);
    setQrError(null);
    setConnected(false);
    setTimedOut(false);
  }, [clearTimers]);

  function handleOpenChange(next: boolean) {
    if (!next) resetState();
    onOpenChange(next);
  }

  // Lazy-load tenants the first time the dialog opens (mirrors
  // NewCampaignDialog's segment fetch — most admins won't reopen this
  // repeatedly in one sitting). setTenants only runs inside the promise
  // callback, never synchronously in the effect body.
  useEffect(() => {
    if (!open || tenants !== null) return;
    api
      .get<Tenant[]>("/admin/tenants")
      .then(setTenants)
      .catch(() => setTenants([]));
  }, [open, tenants]);

  // Unmount safety net alongside the close-triggered cleanup above.
  useEffect(() => clearTimers, [clearTimers]);

  // markConnected fires the success side-effects (toast + list refresh +
  // auto-close) exactly once — succeededRef short-circuits any second caller,
  // so two overlapping poll ticks that both observe "open" can't double-toast
  // or double-refresh.
  const markConnected = useCallback(
    (name: string) => {
      if (succeededRef.current) return;
      succeededRef.current = true;
      clearTimers();
      setConnected(true);
      toast.success(t("admin.instances.wizard.connectedTitle"), { description: name });
      onSuccess();
      closeRef.current = setTimeout(() => {
        resetState();
        onOpenChange(false);
      }, 1200);
    },
    [clearTimers, onOpenChange, onSuccess, resetState, t],
  );

  // Kick off the QR fetch. The QR arrives via webhook so the first read is
  // usually empty — that is NOT an error here; startPolling keeps re-fetching
  // /qr until the cached QR appears (or POLL_TIMEOUT_MS elapses). Only a hard
  // HTTP failure (e.g. the instance row is gone) surfaces immediately.
  const fetchQr = useCallback(
    async (name: string) => {
      setQrLoading(true);
      setQrError(null);
      try {
        const r = await api.get<{ base64: string }>(`/admin/instances/${name}/qr`);
        const uri = toDataUri(r.base64);
        if (uri) {
          qrImageRef.current = uri;
          setQrImage(uri);
        }
        // Empty base64 → the webhook hasn't delivered the QR yet. Leave qrImage
        // null (render shows a "waiting for QR" state) and let the poll retry.
      } catch (e) {
        setQrError(e instanceof ApiError ? e.message : t("admin.instances.wizard.qrFailed"));
      } finally {
        setQrLoading(false);
      }
    },
    [t],
  );

  const startPolling = useCallback(
    (name: string) => {
      clearTimers();
      succeededRef.current = false;
      inFlightRef.current = false;
      setTimedOut(false);
      pollRef.current = setInterval(async () => {
        // In-flight guard: if the previous tick's requests are slower than the
        // interval, skip this one rather than stacking overlapping requests.
        if (inFlightRef.current || succeededRef.current) return;
        inFlightRef.current = true;
        try {
          // Still waiting on the QR? Re-fetch /qr — the backend re-triggers
          // Evolution's connect (which keeps pushing qrcode.updated webhooks)
          // and returns the freshest cached QR. Clears any stale error once it
          // lands so the "waiting" state flips to the image.
          if (!qrImageRef.current) {
            const q = await api.get<{ base64: string }>(`/admin/instances/${name}/qr`);
            const uri = toDataUri(q.base64);
            if (uri) {
              qrImageRef.current = uri;
              setQrImage(uri);
              setQrError(null);
            }
          }
          const r = await api.get<{ state: string }>(`/admin/instances/${name}/state`);
          if (CONNECTED_STATES.has(r.state)) {
            markConnected(name);
          }
        } catch {
          // Transient poll failures are expected mid-pairing (node hiccups) —
          // keep polling until the give-up timeout fires.
        } finally {
          inFlightRef.current = false;
        }
      }, POLL_INTERVAL_MS);
      giveUpRef.current = setTimeout(() => {
        clearTimers();
        setTimedOut(true);
        // Timed out with no QR ever shown and no pairing — surface the failure.
        if (!qrImageRef.current && !succeededRef.current) {
          setQrError(t("admin.instances.wizard.qrFailed"));
        }
      }, POLL_TIMEOUT_MS);
    },
    [clearTimers, markConnected, t],
  );

  async function handleCreate() {
    if (!tenantId || !countryCode.trim()) return;
    setCreating(true);
    try {
      const r = await api.post<CreateInstanceResponse>("/admin/instances", {
        tenant_id: Number(tenantId),
        country_code: countryCode.trim().toUpperCase(),
      });
      setInstanceName(r.instance_name);
      setStep("qr");
      await fetchQr(r.instance_name);
      startPolling(r.instance_name);
    } catch (e) {
      const description =
        e instanceof ApiError
          ? e.status === 402
            ? t("admin.instances.wizard.errNoProxy")
            : e.status === 409
              ? t("admin.instances.wizard.errNoNode")
              : e.status === 503
                ? t("admin.instances.wizard.errNoEvolution")
                : e.message
          : t("admin.instances.wizard.createFailedDesc");
      toast.error(t("admin.instances.wizard.createFailedTitle"), { description });
    } finally {
      setCreating(false);
    }
  }

  function handleRetryQr() {
    if (!instanceName) return;
    fetchQr(instanceName);
    startPolling(instanceName);
  }

  const canCreate = tenantId !== "" && countryCode.trim().length === 2 && !creating;

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-md">
        {step === "form" ? (
          <>
            <DialogHeader>
              <DialogTitle>{t("admin.instances.wizard.title")}</DialogTitle>
              <DialogDescription>{t("admin.instances.wizard.desc")}</DialogDescription>
            </DialogHeader>
            <div className="space-y-4 py-1">
              <div className="space-y-2">
                <label htmlFor="wizard-tenant" className="text-sm font-medium">
                  {t("admin.instances.wizard.tenantLabel")}
                </label>
                {tenants === null ? (
                  <div className="h-9 animate-pulse rounded-lg bg-muted motion-reduce:animate-none" />
                ) : tenants.length === 0 ? (
                  <p className="text-sm text-muted-foreground">{t("admin.instances.wizard.noTenants")}</p>
                ) : (
                  <select
                    id="wizard-tenant"
                    value={tenantId}
                    onChange={(e) => setTenantId(e.target.value)}
                    className="h-9 w-full rounded-lg border bg-transparent px-2.5 text-sm outline-none"
                  >
                    <option value="" disabled>
                      {t("admin.instances.wizard.chooseTenant")}
                    </option>
                    {tenants.map((tenant) => (
                      <option key={tenant.id} value={String(tenant.id)}>
                        {tenant.name || `#${tenant.id}`}
                      </option>
                    ))}
                  </select>
                )}
              </div>
              <div className="space-y-2">
                <label htmlFor="wizard-country" className="text-sm font-medium">
                  {t("admin.instances.wizard.countryLabel")}{" "}
                  <span className="font-mono text-xs text-muted-foreground">
                    {t("admin.instances.wizard.countryHint")}
                  </span>
                </label>
                <Input
                  id="wizard-country"
                  value={countryCode}
                  onChange={(e) => setCountryCode(e.target.value)}
                  placeholder="US"
                  maxLength={2}
                  className="w-24 font-mono uppercase"
                />
              </div>
            </div>
            <DialogFooter>
              <DialogClose render={<Button variant="ghost" />}>{t("admin.instances.wizard.cancel")}</DialogClose>
              <Button onClick={handleCreate} disabled={!canCreate}>
                {creating ? t("admin.instances.wizard.creating") : t("admin.instances.wizard.createConfirm")}
              </Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t("admin.instances.wizard.qrTitle")}</DialogTitle>
              <DialogDescription>
                <span className="font-mono text-xs">{instanceName}</span>
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col items-center gap-4 py-2">
              {connected ? (
                <div className="flex flex-col items-center gap-2 py-8 text-center">
                  <CheckCircle2 className="size-10 text-emerald-500" />
                  <p className="text-sm font-medium">{t("admin.instances.wizard.connectedTitle")}</p>
                </div>
              ) : !qrImage && !qrError && !timedOut ? (
                // Waiting for the QR: either the initial fetch is in flight, or
                // it returned empty and the poll is re-fetching /qr until the
                // webhook delivers the QR into the backend cache. Show a spinner
                // (not a broken image, not an error) the whole time.
                <div className="flex h-56 w-56 flex-col items-center justify-center gap-2 rounded-xl border bg-muted/40">
                  <Loader2 className="size-6 animate-spin text-muted-foreground" />
                  <span className="text-[11px] text-muted-foreground">
                    {t("admin.instances.wizard.qrWaiting")}
                  </span>
                </div>
              ) : qrError ? (
                <div className="flex h-56 w-56 flex-col items-center justify-center gap-2 rounded-xl border border-destructive/40 bg-destructive/5 p-4 text-center text-sm text-destructive">
                  {qrError}
                </div>
              ) : qrImage ? (
                // Evolution-issued pairing QR — data URI built by toDataUri()
                // above (raw base64, prefixed if not already a data URI).
                // eslint-disable-next-line @next/next/no-img-element
                <img
                  src={qrImage}
                  alt={t("admin.instances.wizard.qrAlt")}
                  className="h-56 w-56 rounded-xl border object-contain"
                />
              ) : null}

              {!connected && timedOut && (
                <div className="flex flex-col items-center gap-2 text-center text-sm text-muted-foreground">
                  <p>{t("admin.instances.wizard.timeoutDesc")}</p>
                  <Button variant="outline" size="sm" className="gap-1.5" onClick={handleRetryQr}>
                    <RefreshCw className="size-3.5" />
                    {t("admin.instances.wizard.retry")}
                  </Button>
                </div>
              )}
              {!connected && !timedOut && (
                <p className="flex items-center gap-1.5 font-mono text-[11px] text-muted-foreground">
                  <QrCode className="size-3.5" />
                  {t("admin.instances.wizard.pollingHint")}
                </p>
              )}
            </div>
            <DialogFooter>
              <DialogClose render={<Button variant="ghost" />}>{t("admin.instances.wizard.close")}</DialogClose>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
