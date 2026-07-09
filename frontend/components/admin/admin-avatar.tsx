"use client";

import { cn } from "@/lib/utils";
import type { Me } from "@/components/admin/use-me";
import { useT } from "@/components/locale-provider";

const ROLE_LABEL: Record<Me["role"], string> = {
  admin: "Super Admin",
  sales: "Sales",
  customer: "Customer",
};

/** A monogram avatar derived from the account role — the admin session carries
 *  no display name, so the role initial is the honest, stable identifier. */
export function AdminAvatar({ me, className }: { me: Me | null; className?: string }) {
  const initial = me ? me.role.charAt(0).toUpperCase() : "·";
  return (
    <span
      className={cn(
        "flex size-8 shrink-0 items-center justify-center rounded-full bg-foreground text-background",
        "font-mono text-xs font-semibold ring-1 ring-foreground/10 ring-offset-2 ring-offset-background",
        className,
      )}
      aria-hidden
    >
      {initial}
    </span>
  );
}

/** Avatar + two-line identity, used in the sidebar footer and header menu. */
export function AdminIdentity({ me, className }: { me: Me | null; className?: string }) {
  const t = useT();
  return (
    <div className={cn("flex min-w-0 items-center gap-2.5", className)}>
      <AdminAvatar me={me} />
      <div className="min-w-0 leading-tight">
        <div className="truncate text-sm font-medium">{me ? ROLE_LABEL[me.role] : t("common.loading")}</div>
        <div className="truncate font-mono text-[11px] text-muted-foreground">
          {me ? `uid #${me.user_id}` : "—"}
        </div>
      </div>
    </div>
  );
}
