"use client";

import { useEffect, useRef, useState } from "react";
import { Send, CheckCheck, Clock, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { Mascot } from "@/components/auth/mascot";

/**
 * Interactive "group broadcast" demo for the auth panel — the product thesis,
 * shown rather than told: one message fanning out to many recipients across
 * regions, each ticking through 排队 → 发送中 → 已送达 → 已读 with a live reach
 * counter and a concurrency meter. Auto-loops; click Send to fire a new wave.
 * Pure React + CSS (no animation lib); respects prefers-reduced-motion.
 */

type Status = "queued" | "sending" | "delivered" | "read";

const CONTACTS = [
  { name: "Wang Lei", phone: "+86 138••••6021", tint: "#f59e0b" },
  { name: "María G.", phone: "+55 11••••7745", tint: "#38bdf8" },
  { name: "陈晓", phone: "+852 6••••2290", tint: "#a78bfa" },
  { name: "A. Khan", phone: "+971 5••••0148", tint: "#fb7185" },
  { name: "Tunde O.", phone: "+234 80••••6155", tint: "#34d399" },
];

const META: Record<Status, { label: string; cls: string }> = {
  queued: { label: "排队", cls: "text-white/45" },
  sending: { label: "发送中", cls: "text-amber-200" },
  delivered: { label: "已送达", cls: "text-emerald-100" },
  read: { label: "已读", cls: "text-sky-300" },
};

function withItem<T>(arr: T[], i: number, v: T): T[] {
  const copy = arr.slice();
  copy[i] = v;
  return copy;
}

export function BroadcastDemo({ className }: { className?: string }) {
  const [statuses, setStatuses] = useState<Status[]>(() =>
    CONTACTS.map(() => "queued"),
  );
  const [reached, setReached] = useState(12480);
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);
  // set by the effect; lets the Send button fire a fresh wave on demand
  const trigger = useRef<() => void>(() => {});

  useEffect(() => {
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const clearTimers = () => {
      timers.current.forEach(clearTimeout);
      timers.current = [];
    };

    function runWave() {
      clearTimers();

      if (reduced) {
        setStatuses(CONTACTS.map(() => "read"));
        setReached((r) => r + 196);
        return;
      }

      setStatuses(CONTACTS.map(() => "queued"));

      // count-up: spread one batch of reach across the wave duration
      const batch = 150 + Math.floor(Math.random() * 120);
      const steps = 22;
      for (let s = 1; s <= steps; s++) {
        timers.current.push(
          setTimeout(
            () => setReached((r) => r + Math.round(batch / steps)),
            120 + (s * 1700) / steps,
          ),
        );
      }

      // each recipient advances through the receipt states, staggered = concurrency
      CONTACTS.forEach((_, i) => {
        timers.current.push(
          setTimeout(() => setStatuses((p) => withItem(p, i, "sending")), 160 + i * 150),
        );
        timers.current.push(
          setTimeout(() => setStatuses((p) => withItem(p, i, "delivered")), 640 + i * 175),
        );
        timers.current.push(
          setTimeout(() => setStatuses((p) => withItem(p, i, "read")), 1120 + i * 205),
        );
      });

      // loop
      const done = 1120 + CONTACTS.length * 205;
      timers.current.push(setTimeout(runWave, done + 2400));
    }

    trigger.current = runWave;
    const start = setTimeout(runWave, 450);
    return () => {
      clearTimeout(start);
      clearTimers();
    };
  }, []);

  return (
    <div className={cn("w-full max-w-[360px]", className)}>
      <div className="rounded-2xl bg-white/10 p-5 shadow-2xl shadow-black/25 ring-1 ring-white/20 backdrop-blur-md">
        {/* header */}
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Mascot face={{ focus: null, hidden: false }} className="size-7" />
            <span className="text-sm font-semibold text-white">群发控制台</span>
          </div>
          <span className="inline-flex items-center gap-1.5 text-[11px] font-medium text-white/80">
            <span className="relative flex size-2">
              <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-300 opacity-75" />
              <span className="relative inline-flex size-2 rounded-full bg-emerald-300" />
            </span>
            LIVE
          </span>
        </div>

        {/* compose row */}
        <div className="mt-4 flex items-center gap-2 rounded-xl bg-black/15 p-2 pl-3">
          <p className="flex-1 truncate text-[13px] text-white/85">
            🎉 双 11 专属 5 折，点此领取…
          </p>
          <button
            type="button"
            onClick={() => trigger.current()}
            aria-label="立即群发"
            className="inline-flex size-9 shrink-0 items-center justify-center rounded-lg bg-white text-brand-700 shadow transition hover:bg-emerald-50 active:scale-90"
          >
            <Send className="size-4" />
          </button>
        </div>

        {/* recipients */}
        <ul className="mt-3 space-y-1.5">
          {CONTACTS.map((c, i) => {
            const st = statuses[i];
            const m = META[st];
            return (
              <li
                key={c.phone}
                className={cn(
                  "flex items-center gap-2.5 rounded-lg px-2 py-1.5 transition-colors duration-300",
                  st === "sending" ? "bg-white/12" : "bg-transparent",
                )}
              >
                <span
                  className="grid size-7 shrink-0 place-items-center rounded-full text-[11px] font-semibold text-white ring-1 ring-white/25"
                  style={{ backgroundColor: c.tint }}
                >
                  {c.name.slice(0, 1)}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-xs font-medium text-white">{c.name}</div>
                  <div className="truncate font-mono text-[10px] text-white/55">
                    {c.phone}
                  </div>
                </div>
                <span
                  className={cn(
                    "inline-flex w-16 items-center justify-end gap-1 text-[11px] font-medium",
                    m.cls,
                  )}
                >
                  {st === "queued" && <Clock className="size-3.5" />}
                  {st === "sending" && <Loader2 className="size-3.5 animate-spin" />}
                  {(st === "delivered" || st === "read") && (
                    <CheckCheck className="size-3.5" />
                  )}
                  {m.label}
                </span>
              </li>
            );
          })}
        </ul>

        {/* metrics */}
        <div className="mt-4 flex items-end justify-between border-t border-white/15 pt-3">
          <div>
            <div className="text-[10px] text-white/55">已触达</div>
            <div className="font-mono text-lg font-semibold tabular-nums text-white">
              {reached.toLocaleString()}
            </div>
          </div>
          <div className="flex flex-col items-center gap-1">
            <div className="flex h-7 items-end gap-[3px]">
              {[0, 1, 2, 3, 4, 5, 6].map((b) => (
                <span
                  key={b}
                  className="bcast-bar w-[3px] rounded-full bg-emerald-300/80"
                  style={{ animationDelay: `${b * 0.12}s` }}
                />
              ))}
            </div>
            <div className="text-[9px] tracking-wide text-white/40">并发 128</div>
          </div>
          <div className="text-right">
            <div className="text-[10px] text-white/55">投递率</div>
            <div className="font-mono text-lg font-semibold tabular-nums text-white">
              99.2%
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
