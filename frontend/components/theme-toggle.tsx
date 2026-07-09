"use client";

import { useTheme } from "next-themes";
import { Moon, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/components/locale-provider";

export function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme();
  const t = useT();

  return (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={t("toggle.theme")}
      onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
      className="text-muted-foreground"
    >
      {/* Both icons render server-side; the active theme class toggles which is
          visible, so there's no hydration mismatch and no mount-state effect. */}
      <Sun className="hidden dark:block" />
      <Moon className="block dark:hidden" />
    </Button>
  );
}
