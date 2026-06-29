"use client";

import { Suspense, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { toast } from "sonner";
import { resetPassword, ApiError } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { AuthShell } from "@/components/auth/auth-shell";

function passwordError(v: string): string | undefined {
  if (!v) return "请输入新密码";
  if (v.length < 6) return "密码至少 6 位";
  return undefined;
}

function ResetForm() {
  const router = useRouter();
  const token = useSearchParams().get("token") ?? "";
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [errors, setErrors] = useState<{ password?: string; confirm?: string }>({});
  const [attempted, setAttempted] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  function confirmError(c: string, p: string): string | undefined {
    if (!c) return "请再次输入密码";
    if (c !== p) return "两次密码不一致";
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
      toast.success("密码已重置", { description: "请用新密码登录。" });
      router.push("/login");
    } catch (err) {
      toast.error("重置失败", {
        description: err instanceof ApiError ? err.message : "无法连接服务器",
      });
      setSubmitting(false);
    }
  }

  if (!token) {
    return (
      <AuthShell title="链接无效" subtitle="重置链接缺失或已失效。">
        <Link
          href="/forgot-password"
          className="mt-7 inline-flex h-11 w-full items-center justify-center rounded-full bg-brand-600 text-sm font-medium text-white shadow-sm shadow-brand-600/25 transition-colors hover:bg-brand-700"
        >
          重新申请重置链接
        </Link>
      </AuthShell>
    );
  }

  return (
    <AuthShell title="重置密码" subtitle="设置一个新密码以继续。">
      <form onSubmit={handleSubmit} noValidate className="mt-7 space-y-4">
        <div>
          <label htmlFor="password" className="text-sm font-medium">
            新密码
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
            placeholder="至少 6 位"
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
            确认新密码
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
            placeholder="再次输入新密码"
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
          {submitting ? "重置中…" : "重置密码"}
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

export default function ResetPasswordPage() {
  return (
    <Suspense>
      <ResetForm />
    </Suspense>
  );
}
