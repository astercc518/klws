import { ArrowRight } from "lucide-react";
import type { Locale } from "@/lib/i18n";
import { CtaButton } from "@/components/landing/cta-button";

const T = {
  heading: { zh: "准备好让 WhatsApp 成为你的增长引擎了吗？", en: "Ready to make WhatsApp your growth engine?" },
  sub: {
    zh: "几分钟接入，立刻拥有金融级计费与防封发送能力。无需信用卡即可开始。",
    en: "Integrate in minutes and get financial-grade billing and ban-proof sending. No credit card to start.",
  },
  primary: { zh: "免费开始试用", en: "Start for free" },
  demo: { zh: "预约产品演示", en: "Book a demo" },
};

export function FinalCta({ locale }: { locale: Locale }) {
  return (
    <section className="px-5 py-20 sm:px-8 sm:py-24">
      <div className="relative mx-auto max-w-6xl overflow-hidden rounded-3xl bg-gradient-to-br from-brand-600 to-brand-700 px-6 py-16 text-center shadow-xl shadow-brand-600/20 sm:px-12 sm:py-20">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_1px_1px,rgba(255,255,255,0.18)_1px,transparent_0)] [background-size:24px_24px] [mask-image:radial-gradient(ellipse_60%_80%_at_50%_0%,#000,transparent)]"
        />
        <div className="relative">
          <h2 className="mx-auto max-w-2xl text-balance text-3xl font-semibold tracking-tight text-white sm:text-4xl">
            {T.heading[locale]}
          </h2>
          <p className="mx-auto mt-4 max-w-xl text-pretty text-white/85">
            {T.sub[locale]}
          </p>
          <div className="mt-9 flex flex-col items-center justify-center gap-3 sm:flex-row">
            <CtaButton href="/login" variant="onColor">
              {T.primary[locale]}
              <ArrowRight className="size-4" />
            </CtaButton>
            <CtaButton href="/login" variant="ghost" className="text-white hover:bg-white/10">
              {T.demo[locale]}
            </CtaButton>
          </div>
        </div>
      </div>
    </section>
  );
}
