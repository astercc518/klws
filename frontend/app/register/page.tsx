"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { toast } from "sonner";
import { register, ApiError } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { AuthShell } from "@/components/auth/auth-shell";
import { Turnstile } from "@/components/auth/turnstile";
import { useT } from "@/components/locale-provider";

export default function RegisterPage() {
  const t = useT();
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [token, setTsToken] = useState<string | null>(null);
  const [errors, setErrors] = useState<{
    email?: string;
    password?: string;
    confirm?: string;
    captcha?: string;
  }>({});
  const [attempted, setAttempted] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  function emailError(v: string): string | undefined {
    if (!v.trim()) return t("auth.register.accountRequired");
    if (v.trim().length < 3) return t("auth.register.accountTooShort");
    return undefined;
  }
  function passwordError(v: string): string | undefined {
    if (!v) return t("auth.register.passwordRequired");
    if (v.length < 6) return t("auth.register.passwordTooShort");
    return undefined;
  }
  function confirmError(c: string, p: string): string | undefined {
    if (!c) return t("auth.register.confirmRequired");
    if (c !== p) return t("auth.register.passwordMismatch");
    return undefined;
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setAttempted(true);
    const next = {
      email: emailError(email),
      password: passwordError(password),
      confirm: confirmError(confirm, password),
      captcha: token ? undefined : t("auth.register.captchaRequired"),
    };
    setErrors(next);
    if (next.email || next.password || next.confirm || next.captcha) return;

    setSubmitting(true);
    try {
      await register({ email: email.trim(), password, turnstileToken: token ?? undefined });
      toast.success(t("auth.register.successTitle"), {
        description: t("auth.register.successDesc"),
      });
      router.push("/dashboard");
    } catch (err) {
      toast.error(t("auth.register.failedTitle"), {
        description: err instanceof ApiError ? err.message : t("auth.register.serverUnreachable"),
      });
      setSubmitting(false);
    }
  }

  return (
    <AuthShell title={t("auth.register.title")} subtitle={t("auth.register.subtitle")}>
      <form onSubmit={handleSubmit} noValidate className="mt-7 space-y-4">
        <div>
          <label htmlFor="email" className="text-sm font-medium">
            {t("auth.register.accountLabel")}
          </label>
          <Input
            id="email"
            type="text"
            value={email}
            onChange={(e) => {
              const v = e.target.value;
              setEmail(v);
              if (attempted) setErrors((p) => ({ ...p, email: emailError(v) }));
            }}
            placeholder={t("auth.register.accountPlaceholder")}
            autoComplete="username"
            aria-invalid={!!errors.email}
            aria-describedby="email-error"
            className="mt-2 h-11 rounded-xl"
          />
          <p id="email-error" className="mt-1.5 min-h-4 text-xs text-destructive">
            {errors.email}
          </p>
        </div>

        <div>
          <label htmlFor="password" className="text-sm font-medium">
            {t("auth.register.passwordLabel")}
          </label>
          <Input
            id="password"
            type="password"
            value={password}
            onChange={(e) => {
              const v = e.target.value;
              setPassword(v);
              if (attempted)
                setErrors((p) => ({
                  ...p,
                  password: passwordError(v),
                  confirm: confirmError(confirm, v),
                }));
            }}
            placeholder={t("auth.register.passwordPlaceholder")}
            autoComplete="new-password"
            aria-invalid={!!errors.password}
            aria-describedby="password-error"
            className="mt-2 h-11 rounded-xl"
          />
          <p id="password-error" className="mt-1.5 min-h-4 text-xs text-destructive">
            {errors.password}
          </p>
        </div>

        <div>
          <label htmlFor="confirm" className="text-sm font-medium">
            {t("auth.register.confirmLabel")}
          </label>
          <Input
            id="confirm"
            type="password"
            value={confirm}
            onChange={(e) => {
              const v = e.target.value;
              setConfirm(v);
              if (attempted)
                setErrors((p) => ({ ...p, confirm: confirmError(v, password) }));
            }}
            placeholder={t("auth.register.confirmPlaceholder")}
            autoComplete="new-password"
            aria-invalid={!!errors.confirm}
            aria-describedby="confirm-error"
            className="mt-2 h-11 rounded-xl"
          />
          <p id="confirm-error" className="mt-1.5 min-h-4 text-xs text-destructive">
            {errors.confirm}
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
          {submitting ? t("auth.register.submitting") : t("auth.register.submit")}
        </button>
      </form>

      <p className="mt-6 text-center text-sm text-muted-foreground">
        {t("auth.register.alreadyHaveAccount")}{" "}
        <Link
          href="/login"
          className="font-medium text-brand-600 hover:text-brand-700 dark:text-brand-400"
        >
          {t("auth.register.signInNow")}
        </Link>
      </p>
    </AuthShell>
  );
}
