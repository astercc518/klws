"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { toast } from "sonner";
import { AtSign, LockKeyhole, Eye, EyeOff, LoaderCircle } from "lucide-react";
import { login, ApiError } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { AuthShell } from "@/components/auth/auth-shell";
import { Turnstile } from "@/components/auth/turnstile";

function accountError(v: string): string | undefined {
  if (!v.trim()) return "请输入账号";
  if (v.trim().length < 3) return "账号至少 3 个字符";
  return undefined;
}
function passwordError(v: string): string | undefined {
  if (!v) return "请输入密码";
  if (v.length < 6) return "密码至少 6 位";
  return undefined;
}

export default function LoginPage() {
  const router = useRouter();
  const [account, setAccount] = useState("");
  const [password, setPassword] = useState("");
  const [show, setShow] = useState(false);
  const [capsOn, setCapsOn] = useState(false);
  const [token, setTsToken] = useState<string | null>(null);
  const [errors, setErrors] = useState<{
    account?: string;
    password?: string;
    captcha?: string;
  }>({});
  const [attempted, setAttempted] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setAttempted(true);
    const next = {
      account: accountError(account),
      password: passwordError(password),
      captcha: token ? undefined : "请完成人机验证",
    };
    setErrors(next);
    if (next.account || next.password || next.captcha) return;

    setSubmitting(true);
    try {
      const { role } = await login(account.trim(), password, token ?? undefined);
      router.push(
        role === "admin" ? "/admin" : role === "sales" ? "/sales" : "/dashboard",
      );
    } catch (err) {
      toast.error("登录失败", {
        description: err instanceof ApiError ? err.message : "无法连接服务器",
      });
      setSubmitting(false);
    }
  }

  return (
    <AuthShell title="欢迎回来" subtitle="登录以访问你的 klws 控制台。">
      <form onSubmit={handleSubmit} noValidate className="mt-7 space-y-4">
        <div>
          <label htmlFor="account" className="text-sm font-medium">
            账号
          </label>
          <div className="relative mt-2">
            <AtSign className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              id="account"
              value={account}
              autoFocus
              onChange={(e) => {
                const v = e.target.value;
                setAccount(v);
                if (attempted) setErrors((p) => ({ ...p, account: accountError(v) }));
              }}
              placeholder="邮箱或用户名"
              autoComplete="username"
              aria-invalid={!!errors.account}
              aria-describedby="account-error"
              className="h-12 rounded-xl pl-9"
            />
          </div>
          <p id="account-error" className="mt-1.5 min-h-4 text-xs text-destructive">
            {errors.account}
          </p>
        </div>

        <div>
          <div className="flex items-center justify-between">
            <label htmlFor="password" className="text-sm font-medium">
              密码
            </label>
            <Link
              href="/forgot-password"
              className="text-xs text-muted-foreground transition-colors hover:text-foreground"
            >
              忘记密码？
            </Link>
          </div>
          <div className="relative mt-2">
            <LockKeyhole className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              id="password"
              type={show ? "text" : "password"}
              value={password}
              onChange={(e) => {
                const v = e.target.value;
                setPassword(v);
                if (attempted) setErrors((p) => ({ ...p, password: passwordError(v) }));
              }}
              onKeyUp={(e) => setCapsOn(e.getModifierState("CapsLock"))}
              onKeyDown={(e) => setCapsOn(e.getModifierState("CapsLock"))}
              placeholder="••••••••"
              autoComplete="current-password"
              aria-invalid={!!errors.password}
              aria-describedby="password-error"
              className="h-12 rounded-xl pl-9 pr-10"
            />
            <button
              type="button"
              onClick={() => setShow((v) => !v)}
              aria-label={show ? "隐藏密码" : "显示密码"}
              className="absolute top-1/2 right-2 inline-flex size-7 -translate-y-1/2 items-center justify-center rounded-md text-muted-foreground transition-colors hover:text-foreground"
            >
              {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </button>
          </div>
          <p
            id="password-error"
            className="mt-1.5 min-h-4 text-xs text-destructive"
          >
            {errors.password || (capsOn ? "⚠ 大写锁定已开启" : "")}
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
          className="inline-flex h-12 w-full items-center justify-center gap-2 rounded-full bg-brand-600 text-sm font-medium text-white shadow-sm shadow-brand-600/25 transition-colors hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500/50 focus-visible:ring-offset-2 focus-visible:ring-offset-background focus-visible:outline-none disabled:opacity-60"
        >
          {submitting && <LoaderCircle className="size-4 animate-spin" />}
          {submitting ? "登录中…" : "登录"}
        </button>
      </form>

      <p className="mt-6 text-center text-sm text-muted-foreground">
        还没有账号？{" "}
        <Link
          href="/register"
          className="font-medium text-brand-600 hover:text-brand-700 dark:text-brand-400"
        >
          免费注册
        </Link>
      </p>
    </AuthShell>
  );
}
