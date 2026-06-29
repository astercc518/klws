"use client";

import { useState } from "react";
import Link from "next/link";
import { MailCheck } from "lucide-react";
import { requestPasswordReset } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { AuthShell } from "@/components/auth/auth-shell";
import { Turnstile } from "@/components/auth/turnstile";

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

function emailError(v: string): string | undefined {
  if (!v.trim()) return "请输入邮箱";
  if (!EMAIL_RE.test(v.trim())) return "邮箱格式不正确";
  return undefined;
}

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState("");
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
      captcha: token ? undefined : "请完成人机验证",
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
      <AuthShell title="查收你的邮箱" subtitle="重置链接已发送（如果该账号存在）。">
        <div className="mt-7 flex flex-col items-center text-center">
          <span className="flex size-12 items-center justify-center rounded-2xl bg-brand-500/10 text-brand-600 dark:text-brand-400">
            <MailCheck className="size-6" />
          </span>
          <p className="mt-4 text-sm text-muted-foreground">
            我们已向 <span className="font-medium text-foreground">{email.trim()}</span>{" "}
            发送了密码重置链接。请在 30 分钟内点击完成重置。
          </p>
        </div>
        <Link
          href="/login"
          className="mt-7 inline-flex h-11 w-full items-center justify-center rounded-full bg-brand-600 text-sm font-medium text-white shadow-sm shadow-brand-600/25 transition-colors hover:bg-brand-700"
        >
          返回登录
        </Link>
      </AuthShell>
    );
  }

  return (
    <AuthShell title="找回密码" subtitle="输入注册邮箱，我们会发送重置链接。">
      <form onSubmit={handleSubmit} noValidate className="mt-7 space-y-4">
        <div>
          <label htmlFor="email" className="text-sm font-medium">
            邮箱
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
            onVerify={(t) => {
              setTsToken(t);
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
          {submitting ? "发送中…" : "发送重置链接"}
        </button>
      </form>

      <p className="mt-6 text-center text-sm text-muted-foreground">
        想起来了？{" "}
        <Link
          href="/login"
          className="font-medium text-brand-600 hover:text-brand-700 dark:text-brand-400"
        >
          返回登录
        </Link>
      </p>
    </AuthShell>
  );
}
