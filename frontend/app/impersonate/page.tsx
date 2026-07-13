"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { LoaderCircle } from "lucide-react";
import { setImpersonationToken } from "@/lib/api";
import { useT } from "@/components/locale-provider";

// /impersonate — landing page for admin "login-as" impersonation links.
//
// The admin console opens this route in a NEW TAB with the target user's
// session token in the URL hash (e.g. #token=<urlencoded>&role=sales). The
// token is stored in sessionStorage (tab-local), never localStorage — so the
// admin's own session in the tab that opened this link is left untouched.
// See lib/api.ts (getToken/setImpersonationToken) for the read-side of this.
//
// Deliberately top-level (not under app/admin), so it does not inherit the
// admin console shell/layout.

type Status = "redirecting" | "invalid";

/** Reads the hash once, at mount, to decide which placeholder to render.
 *  Kept out of the effect (as a lazy useState initializer) so the effect
 *  below only performs side effects — storing the token and navigating —
 *  rather than setState, per react-hooks/set-state-in-effect. */
function initialStatus(): Status {
  if (typeof window === "undefined") return "redirecting";
  const hash = window.location.hash.replace(/^#/, "");
  return new URLSearchParams(hash).get("token") ? "redirecting" : "invalid";
}

export default function ImpersonatePage() {
  const t = useT();
  const [status] = useState<Status>(initialStatus);

  useEffect(() => {
    const hash = window.location.hash.replace(/^#/, "");
    const params = new URLSearchParams(hash);
    const token = params.get("token");
    const role = params.get("role");
    if (!token) return;

    setImpersonationToken(decodeURIComponent(token));
    // Hard navigation (not next/navigation router): a fresh document load
    // ensures the app boots by reading the sessionStorage token we just set,
    // rather than continuing to run with whatever session state was already
    // in memory for this tab.
    window.location.replace(role === "sales" ? "/sales" : "/dashboard");
  }, []);

  if (status === "invalid") {
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-3 px-4 text-center">
        <p className="text-sm font-medium text-destructive">{t("auth.impersonate.invalidLink")}</p>
        <Link
          href="/login"
          className="text-sm text-brand-600 hover:text-brand-700 dark:text-brand-400"
        >
          {t("auth.forgot.backToLogin")}
        </Link>
      </div>
    );
  }

  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-3 px-4 text-center">
      <LoaderCircle className="size-6 animate-spin text-muted-foreground" />
      <p className="text-sm text-muted-foreground">{t("auth.impersonate.entering")}</p>
    </div>
  );
}
