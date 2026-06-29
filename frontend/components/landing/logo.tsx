import { cn } from "@/lib/utils";

/**
 * klws mark — a WhatsApp-green chat bubble (with a soft rim-light + tail)
 * carrying a clean, bold "k" monogram. `color` for light surfaces, `white`
 * for the green auth background.
 */
export function Logo({
  className,
  variant = "color",
}: {
  className?: string;
  variant?: "color" | "white";
}) {
  const bubble = variant === "white" ? "#ffffff" : "url(#klwsLogoGrad)";
  const mark = variant === "white" ? "#0e8a45" : "#ffffff";

  return (
    <svg
      viewBox="0 0 32 32"
      fill="none"
      aria-hidden
      className={cn(
        variant === "color" &&
          "[filter:drop-shadow(0_1px_2px_rgba(10,110,55,0.35))]",
        className,
      )}
    >
      <defs>
        <linearGradient id="klwsLogoGrad" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#34d77f" />
          <stop offset="0.55" stopColor="#14a85a" />
          <stop offset="1" stopColor="#0a6e37" />
        </linearGradient>
      </defs>

      {/* bubble body + tail */}
      <g fill={bubble}>
        <rect x="2.5" y="3" width="27" height="22" rx="8" />
        <path d="M9.5 22.5 L6.6 29 L14.5 23.2 Z" />
      </g>
      {/* subtle rim-light (color variant only) */}
      {variant === "color" && (
        <rect
          x="3.25"
          y="3.75"
          width="25.5"
          height="20.5"
          rx="7.25"
          fill="none"
          stroke="#ffffff"
          strokeOpacity="0.22"
          strokeWidth="1"
        />
      )}

      {/* bold "k" monogram */}
      <g
        stroke={mark}
        strokeWidth="2.8"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      >
        <path d="M12.7 7.6 V20.4" />
        <path d="M12.7 15.4 L20 8.6" />
        <path d="M12.7 15.4 L20.6 20.6" />
      </g>
    </svg>
  );
}
