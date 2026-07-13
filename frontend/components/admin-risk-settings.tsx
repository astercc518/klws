"use client";

import { useCallback, useEffect, useState } from "react";
import { Gauge, Info, Save, ShieldAlert, SlidersHorizontal } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { useT } from "@/components/locale-provider";

interface RiskConfig {
  min_delay_seconds: number;
  max_delay_seconds: number;
  daily_limit_per_device: number;
  ban_rate_circuit_breaker: number; // stored as a 0..1 fraction
  circuit_breaker_enabled: boolean;
  circuit_breaker_dry_run: boolean;
  min_sample: number;
  window_seconds: number;
  eval_interval_seconds: number;
  updated_at: string | null;
  updated_by: number | null;
}

// A small pill switch — the project has no Switch primitive, so we build one
// from a button to match the codebase's "compose from base elements" style.
function Toggle({
  checked,
  onChange,
  disabled,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        "relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        checked ? "bg-primary" : "bg-muted-foreground/30",
      )}
    >
      <span
        className={cn(
          "inline-block size-4 transform rounded-full bg-background shadow transition-transform",
          checked ? "translate-x-4" : "translate-x-0.5",
        )}
      />
    </button>
  );
}

function ToggleRow({
  label,
  hint,
  checked,
  onChange,
  disabled,
}: {
  label: string;
  hint?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex items-center justify-between gap-4">
      <div className="min-w-0">
        <div className="text-sm font-medium">{label}</div>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </div>
      <Toggle checked={checked} onChange={onChange} disabled={disabled} label={label} />
    </div>
  );
}

// A labelled slider + number input bound to one string field, so the two stay
// in sync. The string state lets the user clear/retype freely; parsing and
// validation happen in the parent.
function SliderField({
  id,
  label,
  unit,
  value,
  onChange,
  min,
  max,
  step = 1,
}: {
  id: string;
  label: string;
  unit: string;
  value: string;
  onChange: (v: string) => void;
  min: number;
  max: number;
  step?: number;
}) {
  const n = parseFloat(value);
  const sliderValue = Number.isFinite(n) ? Math.min(Math.max(n, min), max) : min;
  return (
    <div className="space-y-2">
      <div className="flex items-baseline justify-between">
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        <span className="font-mono text-xs text-muted-foreground">{unit}</span>
      </div>
      <div className="flex items-center gap-3">
        <input
          type="range"
          aria-label={label}
          min={min}
          max={max}
          step={step}
          value={sliderValue}
          onChange={(e) => onChange(e.target.value)}
          className="h-1.5 flex-1 cursor-pointer appearance-none rounded-full bg-muted accent-primary"
        />
        <Input
          id={id}
          type="number"
          min={min}
          max={max}
          step={step}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="w-24 font-mono text-sm"
        />
      </div>
    </div>
  );
}

export function AdminRiskSettings() {
  const t = useT();
  const [loaded, setLoaded] = useState<RiskConfig | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Editable fields as strings (banPct is a 0..100 percentage for the UI).
  const [minDelay, setMinDelay] = useState("");
  const [maxDelay, setMaxDelay] = useState("");
  const [dailyLimit, setDailyLimit] = useState("");
  const [banPct, setBanPct] = useState("");
  // Circuit-breaker control knobs.
  const [cbEnabled, setCbEnabled] = useState(false);
  const [cbDryRun, setCbDryRun] = useState(true);
  const [minSample, setMinSample] = useState("");
  const [windowSec, setWindowSec] = useState("");
  const [evalSec, setEvalSec] = useState("");

  const applyConfig = useCallback((c: RiskConfig) => {
    setMinDelay(String(c.min_delay_seconds));
    setMaxDelay(String(c.max_delay_seconds));
    setDailyLimit(String(c.daily_limit_per_device));
    setBanPct(String(Math.round(c.ban_rate_circuit_breaker * 1000) / 10)); // 0.15 → 15
    setCbEnabled(c.circuit_breaker_enabled);
    setCbDryRun(c.circuit_breaker_dry_run);
    setMinSample(String(c.min_sample));
    setWindowSec(String(c.window_seconds));
    setEvalSec(String(c.eval_interval_seconds));
  }, []);

  const load = useCallback(async () => {
    try {
      const c = await api.get<RiskConfig>("/admin/settings/risk");
      setLoaded(c);
      applyConfig(c);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.risk.loadFailed"));
      }
    }
  }, [applyConfig, t]);
  useEffect(() => {
    load();
  }, [load]);

  const minN = parseInt(minDelay, 10);
  const maxN = parseInt(maxDelay, 10);
  const dailyN = parseInt(dailyLimit, 10);
  const pctN = parseFloat(banPct);

  const sampleN = parseInt(minSample, 10);
  const windowN = parseInt(windowSec, 10);
  const evalN = parseInt(evalSec, 10);

  const minOk = Number.isFinite(minN) && minN >= 0;
  const maxOk = Number.isFinite(maxN) && maxN >= minN;
  const dailyOk = Number.isFinite(dailyN) && dailyN > 0;
  const pctOk = Number.isFinite(pctN) && pctN >= 0 && pctN <= 100;
  const sampleOk = Number.isFinite(sampleN) && sampleN >= 1;
  const windowOk = Number.isFinite(windowN) && windowN >= 1;
  const evalOk = Number.isFinite(evalN) && evalN >= 1;
  const valid = minOk && maxOk && dailyOk && pctOk && sampleOk && windowOk && evalOk && !busy;

  async function save() {
    if (!valid) return;
    setBusy(true);
    try {
      const c = await api.put<RiskConfig>("/admin/settings/risk", {
        min_delay_seconds: minN,
        max_delay_seconds: maxN,
        daily_limit_per_device: dailyN,
        ban_rate_circuit_breaker: Math.round((pctN / 100) * 10000) / 10000,
        circuit_breaker_enabled: cbEnabled,
        circuit_breaker_dry_run: cbDryRun,
        min_sample: sampleN,
        window_seconds: windowN,
        eval_interval_seconds: evalN,
      });
      setLoaded((prev) => ({ ...(prev ?? c), ...c }));
      toast.success(t("admin.risk.saveSuccessTitle"), {
        description: t("admin.risk.saveSuccessDesc"),
      });
    } catch (e) {
      toast.error(t("admin.risk.saveFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.risk.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  if (error) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-sm text-muted-foreground">
          {t("admin.risk.loadFailedPrefix")}
          {error}
        </CardContent>
      </Card>
    );
  }
  if (!loaded) {
    return (
      <div className="space-y-4">
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="h-36 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Honest integration status — this console persists policy; the running
          dispatch/sendgate engine does not yet consume it. */}
      <div className="flex items-start gap-2.5 rounded-lg bg-amber-500/10 px-3.5 py-3 text-amber-700 ring-1 ring-inset ring-amber-600/20 dark:text-amber-400">
        <Info className="mt-0.5 size-4 shrink-0" />
        <p className="text-xs leading-relaxed">
          {t("admin.risk.noticePrefix")}
          <strong>{t("admin.risk.noticeStrong")}</strong>
          {t("admin.risk.noticeSuffix")}
        </p>
      </div>

      {/* 1. 发信频率控制 */}
      <Card>
        <CardHeader className="border-b">
          <CardTitle className="flex items-center gap-2">
            <SlidersHorizontal className="size-4 text-muted-foreground" />
            {t("admin.risk.pacing.title")}
          </CardTitle>
          <CardDescription>{t("admin.risk.pacing.desc")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5 pt-1">
          <SliderField
            id="min-delay"
            label={t("admin.risk.pacing.minDelayLabel")}
            unit={t("admin.risk.unit.seconds")}
            value={minDelay}
            onChange={setMinDelay}
            min={0}
            max={120}
          />
          <SliderField
            id="max-delay"
            label={t("admin.risk.pacing.maxDelayLabel")}
            unit={t("admin.risk.unit.seconds")}
            value={maxDelay}
            onChange={setMaxDelay}
            min={0}
            max={120}
          />
          {!maxOk && (
            <p className="text-xs text-destructive">{t("admin.risk.pacing.maxDelayError")}</p>
          )}
        </CardContent>
      </Card>

      {/* 2. 设备过载保护 */}
      <Card>
        <CardHeader className="border-b">
          <CardTitle className="flex items-center gap-2">
            <Gauge className="size-4 text-muted-foreground" />
            {t("admin.risk.overload.title")}
          </CardTitle>
          <CardDescription>{t("admin.risk.overload.desc")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5 pt-1">
          <SliderField
            id="daily-limit"
            label={t("admin.risk.overload.dailyLimitLabel")}
            unit={t("admin.risk.unit.perDay")}
            value={dailyLimit}
            onChange={setDailyLimit}
            min={1}
            max={5000}
            step={10}
          />
          {!dailyOk && <p className="text-xs text-destructive">{t("admin.risk.overload.dailyLimitError")}</p>}
        </CardContent>
      </Card>

      {/* 3. 自动熔断策略 */}
      <Card>
        <CardHeader className="border-b">
          <CardTitle className="flex items-center gap-2">
            <ShieldAlert className="size-4 text-muted-foreground" />
            {t("admin.risk.breaker.title")}
          </CardTitle>
          <CardDescription>{t("admin.risk.breaker.desc")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5 pt-1">
          <SliderField
            id="ban-rate"
            label={t("admin.risk.breaker.banRateLabel")}
            unit={t("admin.risk.unit.percent")}
            value={banPct}
            onChange={setBanPct}
            min={0}
            max={100}
            step={0.5}
          />
          {!pctOk && <p className="text-xs text-destructive">{t("admin.risk.breaker.banRateError")}</p>}

          <div className="space-y-4 border-t pt-4">
            <ToggleRow
              label={t("admin.risk.breaker.enableLabel")}
              hint={t("admin.risk.breaker.enableHint")}
              checked={cbEnabled}
              onChange={setCbEnabled}
            />
            <ToggleRow
              label={t("admin.risk.breaker.dryRunLabel")}
              hint={t("admin.risk.breaker.dryRunHint")}
              checked={cbDryRun}
              onChange={setCbDryRun}
              disabled={!cbEnabled}
            />
            <div className="grid gap-4 sm:grid-cols-3">
              <div className="space-y-2">
                <label htmlFor="cb-sample" className="text-sm font-medium">
                  {t("admin.risk.breaker.minSampleLabel")}
                </label>
                <Input
                  id="cb-sample"
                  type="number"
                  min={1}
                  value={minSample}
                  onChange={(e) => setMinSample(e.target.value)}
                  className="font-mono text-sm"
                />
                <p className="text-xs text-muted-foreground">{t("admin.risk.breaker.minSampleHint")}</p>
              </div>
              <div className="space-y-2">
                <label htmlFor="cb-window" className="text-sm font-medium">
                  {t("admin.risk.breaker.evalWindowLabel")}
                </label>
                <Input
                  id="cb-window"
                  type="number"
                  min={1}
                  value={windowSec}
                  onChange={(e) => setWindowSec(e.target.value)}
                  className="font-mono text-sm"
                />
                <p className="text-xs text-muted-foreground">{t("admin.risk.breaker.evalWindowHint")}</p>
              </div>
              <div className="space-y-2">
                <label htmlFor="cb-eval" className="text-sm font-medium">
                  {t("admin.risk.breaker.evalIntervalLabel")}
                </label>
                <Input
                  id="cb-eval"
                  type="number"
                  min={1}
                  value={evalSec}
                  onChange={(e) => setEvalSec(e.target.value)}
                  className="font-mono text-sm"
                />
                <p className="text-xs text-muted-foreground">{t("admin.risk.breaker.evalIntervalHint")}</p>
              </div>
            </div>
            {(!sampleOk || !windowOk || !evalOk) && (
              <p className="text-xs text-destructive">{t("admin.risk.breaker.validationError")}</p>
            )}
          </div>
        </CardContent>
      </Card>

      {/* 底部统一保存 */}
      <div className="flex items-center justify-between gap-3 border-t pt-4">
        <span className="font-mono text-xs text-muted-foreground">
          {loaded.updated_at
            ? `${t("admin.risk.lastUpdatedPrefix")}${loaded.updated_at.slice(0, 19).replace("T", " ")}`
            : t("admin.risk.noPolicySetYet")}
        </span>
        <div className="flex items-center gap-2">
          <Button variant="ghost" onClick={() => applyConfig(loaded)} disabled={busy}>
            {t("admin.risk.resetButton")}
          </Button>
          <Button onClick={save} disabled={!valid} className="gap-2">
            <Save className="size-4" />
            {busy ? t("admin.risk.savingButton") : t("admin.risk.savePolicyButton")}
          </Button>
        </div>
      </div>
    </div>
  );
}
