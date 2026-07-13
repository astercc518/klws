"use client";

import { Suspense, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { toast } from "sonner";
import { resetPassword, ApiError } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { AuthShell } from "@/components/auth/auth-shell";
import { useT } from "@/components/locale-provider";

function ResetForm() {
  const t = useT();
  const router = useRouter();
  const token = useSearchParams().get("token") ?? "";
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [errors, setErrors] = useState<{ password?: string; confirm?: string }>({});
  const [attempted, setAttempted] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  function passwordError(v: string): string | undefined {
    if (!v) return t("auth.reset.newPasswordRequired");
    if (v.length < 6) return t("auth.reset.passwordTooShort");
    return undefined;
  }
  function confirmError(c: string, p: string): string | undefined {
    if (!c) return t("auth.reset.confirmRequired");
    if (c !== p) return t("auth.reset.passwordMismatch");
    return undefined;
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setAttempted(true);
    const next = {
      password: passwordError(password),
      confirm: confirmError(confirm, password),
    };
    setErrors(next);
    if (next.password || next.confirm) return;

    setSubmitting(true);
    try {
      await resetPassword(token, password);
      toast.success(t("auth.reset.successTitle"), { description: t("auth.reset.successDesc") });
      router.push("/login");
    } catch (err) {
      toast.error(t("auth.reset.failedTitle"), {
        description: err instanceof ApiError ? err.message : t("auth.reset.serverUnreachable"),
      });
      setSubmitting(false);
    }
  }

  if (!token) {
    return (
      <AuthShell title={t("auth.reset.invalidTitle")} subtitle={t("auth.reset.invalidSubtitle")}>
        <Link
          href="/forgot-password"
          className="mt-7 inline-flex h-11 w-full items-center justify-center rounded-full bg-brand-600 text-sm font-medium text-white shadow-sm shadow-brand-600/25 transition-colors hover:bg-brand-700"
        >
          {t("auth.reset.requestNewLink")}
        </Link>
      </AuthShell>
    );
  }

  return (
    <AuthShell title={t("auth.reset.title")} subtitle={t("auth.reset.subtitle")}>
      <form onSubmit={handleSubmit} noValidate className="mt-7 space-y-4">
        <div>
          <label htmlFor="password" className="text-sm font-medium">
            {t("auth.reset.newPasswordLabel")}
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
            placeholder={t("auth.reset.passwordPlaceholder")}
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
            {t("auth.reset.confirmLabel")}
          </label>
          <Input
            id="confirm"
            type="password"
            value={confirm}
            onChange={(e) => {
              const v = e.target.value;
              setConfirm(v);
              if (attempted) setErrors((p) => ({ ...p, confirm: confirmError(v, password) }));
            }}
            placeholder={t("auth.reset.confirmPlaceholder")}
            autoComplete="new-password"
            aria-invalid={!!errors.confirm}
            aria-describedby="confirm-error"
            className="mt-2 h-11 rounded-xl"
          />
          <p id="confirm-error" className="mt-1.5 min-h-4 text-xs text-destructive">
            {errors.confirm}
          </p>
        </div>

        <button
          type="submit"
          disabled={submitting}
          className="inline-flex h-11 w-full items-center justify-center rounded-full bg-brand-600 text-sm font-medium text-white shadow-sm shadow-brand-600/25 transition-colors hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500/50 focus-visible:ring-offset-2 focus-visible:ring-offset-background focus-visible:outline-none disabled:opacity-60"
        >
          {submitting ? t("auth.reset.submitting") : t("auth.reset.submit")}
        </button>
      </form>

      <p className="mt-6 text-center text-sm text-muted-foreground">
        {t("auth.reset.rememberedPassword")}{" "}
        <Link
          href="/login"
          className="font-medium text-brand-600 hover:text-brand-700 dark:text-brand-400"
        >
          {t("auth.reset.backToLogin")}
        </Link>
      </p>
    </AuthShell>
  );
}

export default function ResetPasswordPage() {
  return (
    <Suspense>
      <ResetForm />
    </Suspense>
  );
}
