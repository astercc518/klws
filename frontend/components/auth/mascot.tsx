"use client";

import { createContext, useContext } from "react";
import { cn } from "@/lib/utils";

/**
 * Shared face state the auth form reports up to the mascot:
 *  - focus: which field the user is in (drives where the mascot looks)
 *  - hidden: whether the password is currently masked (drives eye-covering)
 */
export type FaceState = { focus: "account" | "password" | null; hidden: boolean };

type Report = (next: FaceState) => void;
const MascotContext = createContext<Report | null>(null);

export function MascotProvider({
  report,
  children,
}: {
  report: Report;
  children: React.ReactNode;
}) {
  return <MascotContext.Provider value={report}>{children}</MascotContext.Provider>;
}

/** No-ops when rendered outside a provider (e.g. mobile, where the mascot is hidden). */
export function useMascot(): Report {
  return useContext(MascotContext) ?? (() => {});
}

/**
 * Wadi — the klws chat-bubble mascot. A white speech bubble with a face that
 * reacts to the login form: looks down while you type your account, and shyly
 * covers its eyes while your password is masked (peeking when you reveal it).
 */
export function Mascot({
  face,
  className,
}: {
  face: FaceState;
  className?: string;
}) {
  const covering = face.focus === "password" && face.hidden;
  const peeking = face.focus === "password" && !face.hidden;
  const lookingDown = face.focus === "account";

  const eyeTransform = peeking
    ? "translateY(-2px) scale(1.14)"
    : lookingDown
      ? "translateY(8px)"
      : "translateY(0)";

  const gazeStyle: React.CSSProperties = {
    transform: eyeTransform,
    transition: "transform 0.35s cubic-bezier(0.34, 1.56, 0.64, 1)",
  };
  const handsStyle: React.CSSProperties = {
    transform: covering ? "translateY(0)" : "translateY(74px)",
    opacity: covering ? 1 : 0,
    transition: "transform 0.4s cubic-bezier(0.34, 1.56, 0.64, 1), opacity 0.3s",
  };

  return (
    <svg
      viewBox="0 0 220 220"
      fill="none"
      aria-hidden
      className={cn("mascot-float", className)}
    >
      <defs>
        <linearGradient id="mascotShadow" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#0a6e37" stopOpacity="0.25" />
          <stop offset="1" stopColor="#0a6e37" stopOpacity="0" />
        </linearGradient>
      </defs>

      {/* ground shadow */}
      <ellipse cx="110" cy="206" rx="58" ry="10" fill="url(#mascotShadow)" />

      {/* bubble body + tail */}
      <g className="[filter:drop-shadow(0_8px_18px_rgba(10,110,55,0.28))]">
        <path d="M70 168 L50 202 L98 176 Z" fill="#ffffff" />
        <rect x="24" y="26" width="172" height="150" rx="46" fill="#ffffff" />
      </g>

      {/* "online" status dot */}
      <circle cx="182" cy="46" r="13" fill="#ffffff" />
      <circle cx="182" cy="46" r="8" fill="#25d366" className="mascot-pulse" />

      {/* cheeks */}
      <ellipse cx="74" cy="126" rx="13" ry="8" fill="#34d77f" opacity="0.4" />
      <ellipse cx="146" cy="126" rx="13" ry="8" fill="#34d77f" opacity="0.4" />

      {/* eyes (gaze group) */}
      <g style={gazeStyle}>
        <ellipse
          cx="86"
          cy="104"
          rx="11"
          ry="13"
          fill="#0a6e37"
          className="mascot-blink"
        />
        <ellipse
          cx="134"
          cy="104"
          rx="11"
          ry="13"
          fill="#0a6e37"
          className="mascot-blink"
        />
      </g>

      {/* smile */}
      <path
        d="M88 140 Q110 158 132 140"
        stroke="#0e8a45"
        strokeWidth="6"
        strokeLinecap="round"
        fill="none"
      />

      {/* paws that rise to cover the eyes while the password is hidden */}
      <g style={handsStyle}>
        <ellipse
          cx="86"
          cy="104"
          rx="20"
          ry="16"
          fill="#ffffff"
          stroke="#34d77f"
          strokeWidth="3"
        />
        <ellipse
          cx="134"
          cy="104"
          rx="20"
          ry="16"
          fill="#ffffff"
          stroke="#34d77f"
          strokeWidth="3"
        />
      </g>
    </svg>
  );
}
