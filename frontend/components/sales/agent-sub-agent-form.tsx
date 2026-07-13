"use client";

import { useState } from "react";
import { UserPlus } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { useT } from "@/components/locale-provider";

interface CreatedSubAgent {
  id: number;
  email: string;
}

/** AgentSubAgentForm: POST /sales/sub-agents {email,password} (see
 *  handleAgentCreateSubAgent) creates a role='sales' console_user with
 *  parent_id = the caller. There is no GET endpoint that lists an agent's
 *  own sub-agents, so this only keeps an honest, session-local log of what
 *  was just created rather than pretending to show the full downline roster. */
export function AgentSubAgentForm() {
  const t = useT();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<CreatedSubAgent[]>([]);

  const valid = email.trim().length > 3 && password.length >= 8 && !busy;

  async function submit() {
    setBusy(true);
    try {
      const data = await api.post<{ id: number; email: string; parent_id: number }>("/sales/sub-agents", {
        email: email.trim(),
        password,
      });
      toast.success(t("sales.subAgent.createdToastTitle"), { description: `${data.email} · #${data.id}` });
      setCreated((prev) => [{ id: data.id, email: data.email }, ...prev]);
      setEmail("");
      setPassword("");
    } catch (e) {
      toast.error(t("sales.subAgent.createFailedToastTitle"), {
        description: e instanceof ApiError ? e.message : t("sales.subAgent.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-6">
      <Card className="max-w-md space-y-4 p-5">
        <div className="space-y-2">
          <label htmlFor="sa-email" className="text-sm font-medium">
            {t("sales.subAgent.emailLabel")}
          </label>
          <Input
            id="sa-email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="agent@example.com"
          />
        </div>
        <div className="space-y-2">
          <label htmlFor="sa-password" className="text-sm font-medium">
            {t("sales.subAgent.passwordLabel")}{" "}
            <span className="font-mono text-xs text-muted-foreground">{t("sales.subAgent.passwordHint")}</span>
          </label>
          <Input
            id="sa-password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t("sales.subAgent.passwordPlaceholder")}
          />
        </div>
        <Button onClick={submit} disabled={!valid} className="gap-1.5">
          <UserPlus className="size-3.5" />
          {busy ? t("sales.subAgent.creating") : t("sales.subAgent.createButton")}
        </Button>
      </Card>

      <Card className="p-5">
        <div className="mb-3 text-sm font-medium">{t("sales.subAgent.sessionCreatedTitle")}</div>
        {created.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("sales.subAgent.emptyState")}</p>
        ) : (
          <ul className="space-y-2">
            {created.map((c) => (
              <li key={c.id} className="flex items-center gap-2 text-sm">
                <Badge variant="secondary" className="font-mono">
                  #{c.id}
                </Badge>
                <span>{c.email}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
