"use client";

import { useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";

/** The current session, as echoed by GET /auth/me. */
export interface Me {
  user_id: number;
  role: "admin" | "sales" | "customer";
  tenant_id: number | null;
}

// Module-level promise cache so the sidebar and header share one /auth/me
// request instead of each firing their own on mount.
let cached: Promise<Me> | null = null;

function fetchMe(): Promise<Me> {
  if (!cached) {
    cached = api.get<Me>("/auth/me").catch((e) => {
      cached = null; // let a later mount retry; swallow the 401 redirect noise
      throw e;
    });
  }
  return cached;
}

/** useMe resolves the signed-in account, shared across admin chrome. */
export function useMe(): Me | null {
  const [me, setMe] = useState<Me | null>(null);

  useEffect(() => {
    let alive = true;
    fetchMe()
      .then((d) => alive && setMe(d))
      .catch((e) => {
        // 401 is handled globally (redirect to /login); ignore everything else.
        if (!(e instanceof ApiError)) throw e;
      });
    return () => {
      alive = false;
    };
  }, []);

  return me;
}
