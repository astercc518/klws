import type { Locale } from "@/lib/i18n";

const LOGOS = [
  "NovaPay", "ShopiGo", "Lazadu", "Finlytic", "DropFlow", "Quantix",
  "Meridian", "Voltel", "Cargora", "Pulsedesk", "Brightly", "Northwind",
];

const T = {
  trusted: {
    zh: ["深受全球", "16,000+", "出海企业信赖"] as const,
    en: ["Trusted by", "16,000+", "businesses worldwide"] as const,
  },
};

export function SocialProof({ locale }: { locale: Locale }) {
  const [pre, num, post] = T.trusted[locale];
  return (
    <section className="border-y border-border/60 bg-muted/30">
      <div className="py-12">
        <p className="px-5 text-center text-sm font-medium text-muted-foreground">
          {pre} <span className="text-foreground">{num}</span> {post}
        </p>

        <div className="group/marquee relative mt-8 overflow-hidden [mask-image:linear-gradient(to_right,transparent,#000_8%,#000_92%,transparent)]">
          <div className="animate-marquee flex w-max items-center gap-12 pr-12">
            {[...LOGOS, ...LOGOS].map((name, i) => (
              <span
                key={`${name}-${i}`}
                className="shrink-0 text-lg font-semibold tracking-tight whitespace-nowrap text-muted-foreground/60 transition-colors hover:text-foreground"
              >
                {name}
              </span>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}
