import type { Locale } from "@/lib/i18n";
import { CtaButton } from "@/components/landing/cta-button";

const APPS = [
  "Shopify", "HubSpot", "Salesforce", "Zapier", "WooCommerce", "Stripe",
  "Notion", "Slack", "Google Sheets", "Make", "Segment", "Webhook",
];

const COPY = {
  eyebrow: { zh: "集成生态", en: "Integrations" },
  heading: { zh: "接入你团队现有的技术栈", en: "Connect to your team's existing stack" },
  sub: {
    zh: "开放 REST API 与 Webhook，100+ 应用即插即用。把发送能力嵌进你的 CRM、电商与自动化流程，数据双向同步。",
    en: "Open REST API and Webhooks with 100+ plug-and-play apps. Embed sending into your CRM, e-commerce, and automations with two-way sync.",
  },
  cta: { zh: "浏览集成目录", en: "Browse integrations" },
};

export function Integrations({ locale }: { locale: Locale }) {
  return (
    <section className="border-t border-border/60">
      <div className="mx-auto max-w-7xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="grid items-center gap-12 lg:grid-cols-[0.9fr_1.1fr] lg:gap-16">
          <div className="max-w-md">
            <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
              {COPY.eyebrow[locale]}
            </div>
            <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
              {COPY.heading[locale]}
            </h2>
            <p className="mt-4 text-pretty text-muted-foreground">{COPY.sub[locale]}</p>
            <div className="mt-8">
              <CtaButton href="/docs" variant="outline" size="md">
                {COPY.cta[locale]}
              </CtaButton>
            </div>
          </div>

          <div className="grid grid-cols-3 gap-3 sm:grid-cols-4">
            {APPS.map((app) => (
              <div
                key={app}
                className="flex h-20 items-center justify-center rounded-2xl border border-border/70 bg-card text-center text-sm font-medium text-muted-foreground shadow-sm transition-colors hover:border-brand-500/40 hover:text-foreground"
              >
                {app}
              </div>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}
