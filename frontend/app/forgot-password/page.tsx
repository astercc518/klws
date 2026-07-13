"use client";

import { useState } from "react";
import Link from "next/link";
import { MailCheck } from "lucide-react";
import { requestPasswordReset } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { AuthShell } from "@/components/auth/auth-shell";
import { Turnstile } from "@/components/auth/turnstile";
import { useT } from "@/components/locale-provider";

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export default function ForgotPasswordPage() {
  const t = useT();
  const [email, setEmail] = useState("");

  function emailError(v: string): string | undefined {
    if (!v.trim()) return t("auth.forgot.emailRequired");
    if (!EMAIL_RE.test(v.trim())) return t("auth.forgot.emailInvalid");
    return undefined;
  }
  const [token, setTsToken] = useState<string | null>(null);
  const [errors, setErrors] = useState<{ email?: string; captcha?: string }>({});
  const [attempted, setAttempted] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [sent, setSent] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setAttempted(true);
    const next = {
      email: emailError(email),
      captcha: token ? undefined : t("auth.forgot.captchaRequired"),
    };
    setErrors(next);
    if (next.email || next.captcha) return;

    setSubmitting(true);
    // Best-effort: always show success so we never leak whether an account exists.
    await requestPasswordReset(email.trim(), token ?? undefined).catch(() => {});
    setSent(true);
    setSubmitting(false);
  }

  if (sent) {
    return (
      <AuthShell title={t("auth.forgot.sentTitle")} subtitle={t("auth.forgot.sentSubtitle")}>
        <div className="mt-7 flex flex-col items-center text-center">
          <span className="flex size-12 items-center justify-center rounded-2xl bg-brand-500/10 text-brand-600 dark:text-brand-400">
            <MailCheck className="size-6" />
          </span>
          <p className="mt-4 text-sm text-muted-foreground">
            {t("auth.forgot.sentPrefix")}
            <span className="font-medium text-foreground">{email.trim()}</span>
            {t("auth.forgot.sentSuffix")}
          </p>
        </div>
        <Link
          href="/login"
          className="mt-7 inline-flex h-11 w-full items-center justify-center rounded-full bg-brand-600 text-sm font-medium text-white shadow-sm shadow-brand-600/25 transition-colors hover:bg-brand-700"
        >
          {t("auth.forgot.backToLogin")}
        </Link>
      </AuthShell>
    );
  }

  return (
    <AuthShell title={t("auth.forgot.title")} subtitle={t("auth.forgot.subtitle")}>
      <form onSubmit={handleSubmit} noValidate className="mt-7 space-y-4">
        <div>
          <label htmlFor="email" className="text-sm font-medium">
            {t("auth.forgot.emailLabel")}
          </label>
          <Input
            id="email"
            type="email"
            value={email}
            onChange={(e) => {
              const v = e.target.value;
              setEmail(v);
              if (attempted) setErrors((p) => ({ ...p, email: emailError(v) }));
            }}
            placeholder="you@company.com"
            autoComplete="email"
            aria-invalid={!!errors.email}
            aria-describedby="email-error"
            className="mt-2 h-11 rounded-xl"
          />
          <p id="email-error" className="mt-1.5 min-h-4 text-xs text-destructive">
            {errors.email}
          </p>
        </div>

        <div>
          <Turnstile
            onVerify={(v) => {
              setTsToken(v);
              setErrors((p) => ({ ...p, captcha: undefined }));
            }}
            onExpire={() => setTsToken(null)}
          />
          <p className="mt-1.5 min-h-4 text-xs text-destructive">{errors.captcha}</p>
        </div>

        <button
          type="submit"
          disabled={submitting}
          className="inline-flex h-11 w-full items-center justify-center rounded-full bg-brand-600 text-sm font-medium text-white shadow-sm shadow-brand-600/25 transition-colors hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500/50 focus-visible:ring-offset-2 focus-visible:ring-offset-background focus-visible:outline-none disabled:opacity-60"
        >
          {submitting ? t("auth.forgot.submitting") : t("auth.forgot.submit")}
        </button>
      </form>

      <p className="mt-6 text-center text-sm text-muted-foreground">
        {t("auth.forgot.rememberedPassword")}{" "}
        <Link
          href="/login"
          className="font-medium text-brand-600 hover:text-brand-700 dark:text-brand-400"
        >
          {t("auth.forgot.backToLogin")}
        </Link>
      </p>
    </AuthShell>
  );
}
