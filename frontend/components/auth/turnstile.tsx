"use client";

import { useEffect, useRef } from "react";

// Cloudflare Turnstile. Uses the public site key from env, falling back to
// Cloudflare's official "always passes" TEST key so the widget renders in dev.
const SITE_KEY =
  process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY || "1x00000000000000000000AA";
const SCRIPT_SRC =
  "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";

interface TurnstileApi {
  render: (el: HTMLElement, opts: Record<string, unknown>) => string;
  remove: (id: string) => void;
  reset: (id?: string) => void;
}
declare global {
  interface Window {
    turnstile?: TurnstileApi;
  }
}

let scriptPromise: Promise<void> | null = null;
function loadScript(): Promise<void> {
  if (scriptPromise) return scriptPromise;
  scriptPromise = new Promise((resolve, reject) => {
    if (typeof window !== "undefined" && window.turnstile) return resolve();
    const s = document.createElement("script");
    s.src = SCRIPT_SRC;
    s.async = true;
    s.defer = true;
    s.onload = () => resolve();
    s.onerror = () => reject(new Error("turnstile failed to load"));
    document.head.appendChild(s);
  });
  return scriptPromise;
}

export function Turnstile({
  onVerify,
  onExpire,
  className,
}: {
  onVerify: (token: string) => void;
  onExpire?: () => void;
  className?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const widgetId = useRef<string | null>(null);
  const cbRef = useRef({ onVerify, onExpire });

  useEffect(() => {
    cbRef.current = { onVerify, onExpire };
  });

  useEffect(() => {
    let cancelled = false;
    loadScript()
      .then(() => {
        if (cancelled || !containerRef.current || !window.turnstile) return;
        widgetId.current = window.turnstile.render(containerRef.current, {
          sitekey: SITE_KEY,
          theme: "auto",
          callback: (token: string) => cbRef.current.onVerify(token),
          "expired-callback": () => cbRef.current.onExpire?.(),
          "error-callback": () => cbRef.current.onExpire?.(),
        });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      if (widgetId.current && window.turnstile) {
        try {
          window.turnstile.remove(widgetId.current);
        } catch {
          /* widget already gone */
        }
      }
    };
  }, []);

  return <div ref={containerRef} className={className} />;
}
