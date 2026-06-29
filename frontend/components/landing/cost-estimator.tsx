"use client";

import { useState } from "react";
import { Calculator, TrendingDown } from "lucide-react";
import type { Locale } from "@/lib/i18n";
import { CtaButton } from "@/components/landing/cta-button";

const REGIONS: { key: string; label: Record<Locale, string>; price: number }[] = [
  { key: "sea", label: { zh: "东南亚", en: "SE Asia" }, price: 0.03 },
  { key: "latam", label: { zh: "拉美", en: "LATAM" }, price: 0.04 },
  { key: "na-eu", label: { zh: "北美 / 欧洲", en: "NA / EU" }, price: 0.05 },
  { key: "mena", label: { zh: "中东", en: "Middle East" }, price: 0.06 },
];

const DIY_MONTHLY_OVERHEAD = 4500;

const fmtUsd = (n: number) => "$" + Math.round(n).toLocaleString("en-US");

const T = {
  eyebrow: { zh: "用量估价", en: "Estimate" },
  heading: { zh: "算一算，你每月大概花多少", en: "Estimate your monthly spend" },
  sub: {
    zh: "拖动滑块，按目标地区估算月度发送成本。用多少、计多少，发送失败自动退款。",
    en: "Drag the slider to estimate monthly cost by region. Pay for what you use — failed sends are refunded.",
  },
  params: { zh: "估算参数", en: "Parameters" },
  volume: { zh: "每月发送量", en: "Monthly volume" },
  unit: { zh: "条", en: "msgs" },
  region: { zh: "目标地区", en: "Target region" },
  perEach: { zh: "/条", en: "/msg" },
  resultLabel: { zh: "预计月度发送成本", en: "Estimated monthly cost" },
  perMonth: { zh: "/ 月", en: "/ mo" },
  perMsg: { zh: "折合每条", en: "Per message" },
  plan: { zh: "推荐方案", en: "Recommended plan" },
  saving: { zh: "相比自建团队约省", en: "Saved vs. in-house" },
  savingMonth: { zh: "/ 月", en: "/ mo" },
  start: { zh: "按此用量开始", en: "Start at this volume" },
  disclaimer: { zh: "估算仅供参考，最终以实际计费为准。", en: "Estimate only; actual billing applies." },
};

export function CostEstimator({ locale }: { locale: Locale }) {
  const [volume, setVolume] = useState(100_000);
  const [region, setRegion] = useState(REGIONS[2]);

  const spend = volume * region.price;
  const perMsg = region.price;
  const diySaving = Math.max(0, DIY_MONTHLY_OVERHEAD - spend * 0.15);
  const recommended = volume <= 20_000 ? "Pro" : "Enterprise";

  return (
    <section className="border-t border-border/60">
      <div className="mx-auto max-w-5xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="mx-auto max-w-2xl text-center">
          <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
            {T.eyebrow[locale]}
          </div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
            {T.heading[locale]}
          </h2>
          <p className="mt-4 text-pretty text-muted-foreground">{T.sub[locale]}</p>
        </div>

        <div className="mt-12 overflow-hidden rounded-3xl border border-border/70 bg-card shadow-sm ring-1 ring-foreground/5">
          <div className="grid lg:grid-cols-[1.1fr_0.9fr]">
            <div className="border-b border-border/60 p-7 sm:p-9 lg:border-r lg:border-b-0">
              <div className="flex items-center gap-2 text-sm font-medium">
                <Calculator className="size-4 text-brand-600 dark:text-brand-400" />
                {T.params[locale]}
              </div>

              <div className="mt-7">
                <div className="flex items-baseline justify-between">
                  <label htmlFor="volume" className="text-sm text-muted-foreground">
                    {T.volume[locale]}
                  </label>
                  <span className="font-mono text-lg font-semibold tabular-nums">
                    {volume.toLocaleString("en-US")}
                    <span className="ml-1 text-xs font-normal text-muted-foreground">
                      {T.unit[locale]}
                    </span>
                  </span>
                </div>
                <input
                  id="volume"
                  type="range"
                  min={10_000}
                  max={2_000_000}
                  step={10_000}
                  value={volume}
                  onChange={(e) => setVolume(Number(e.target.value))}
                  className="mt-3 w-full accent-brand-600"
                />
                <div className="mt-1 flex justify-between font-mono text-[10px] text-muted-foreground">
                  <span>10K</span>
                  <span>2M</span>
                </div>
              </div>

              <div className="mt-7">
                <div className="text-sm text-muted-foreground">{T.region[locale]}</div>
                <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
                  {REGIONS.map((r) => {
                    const active = r.key === region.key;
                    return (
                      <button
                        key={r.key}
                        type="button"
                        onClick={() => setRegion(r)}
                        className={
                          active
                            ? "rounded-xl border border-brand-500 bg-brand-500/10 px-2 py-2.5 text-xs font-medium text-brand-700 dark:text-brand-400"
                            : "rounded-xl border border-border/70 px-2 py-2.5 text-xs font-medium text-muted-foreground hover:border-foreground/20 hover:text-foreground"
                        }
                      >
                        {r.label[locale]}
                        <span className="mt-0.5 block font-mono text-[10px] opacity-70">
                          ${r.price.toFixed(2)}{T.perEach[locale]}
                        </span>
                      </button>
                    );
                  })}
                </div>
              </div>
            </div>

            <div className="bg-muted/30 p-7 sm:p-9">
              <div className="font-mono text-[10px] uppercase tracking-[0.16em] text-muted-foreground">
                {T.resultLabel[locale]}
              </div>
              <div className="mt-2 text-4xl font-semibold tracking-tight tabular-nums sm:text-5xl">
                {fmtUsd(spend)}
                <span className="ml-1 text-base font-normal text-muted-foreground">
                  {T.perMonth[locale]}
                </span>
              </div>

              <dl className="mt-6 space-y-3 text-sm">
                <div className="flex items-center justify-between">
                  <dt className="text-muted-foreground">{T.perMsg[locale]}</dt>
                  <dd className="font-mono tabular-nums">${perMsg.toFixed(2)}</dd>
                </div>
                <div className="flex items-center justify-between">
                  <dt className="text-muted-foreground">{T.plan[locale]}</dt>
                  <dd className="font-medium text-brand-600 dark:text-brand-400">
                    {recommended}
                  </dd>
                </div>
                <div className="flex items-center justify-between border-t border-border/60 pt-3">
                  <dt className="flex items-center gap-1.5 text-muted-foreground">
                    <TrendingDown className="size-4 text-emerald-600 dark:text-emerald-500" />
                    {T.saving[locale]}
                  </dt>
                  <dd className="font-semibold text-emerald-600 dark:text-emerald-500">
                    {fmtUsd(diySaving)} {T.savingMonth[locale]}
                  </dd>
                </div>
              </dl>

              <CtaButton href="/login" className="mt-7 w-full">
                {T.start[locale]}
              </CtaButton>
              <p className="mt-3 text-center text-[11px] text-muted-foreground/70">
                {T.disclaimer[locale]}
              </p>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
