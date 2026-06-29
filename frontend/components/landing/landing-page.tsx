import type { Locale } from "@/lib/i18n";
import { Reveal } from "@/components/landing/reveal";
import { HtmlLang } from "@/components/landing/html-lang";
import { SiteHeader } from "@/components/landing/site-header";
import { Hero } from "@/components/landing/hero";
import { SocialProof } from "@/components/landing/social-proof";
import { Stats } from "@/components/landing/stats";
import { Features } from "@/components/landing/features";
import { HowItWorks } from "@/components/landing/how-it-works";
import { UseCases } from "@/components/landing/use-cases";
import { BillingShowcase } from "@/components/landing/billing-showcase";
import { Security } from "@/components/landing/security";
import { Infrastructure } from "@/components/landing/infrastructure";
import { Integrations } from "@/components/landing/integrations";
import { Developer } from "@/components/landing/developer";
import { Testimonials } from "@/components/landing/testimonials";
import { CaseStudy } from "@/components/landing/case-study";
import { Comparison } from "@/components/landing/comparison";
import { CostEstimator } from "@/components/landing/cost-estimator";
import { Pricing } from "@/components/landing/pricing";
import { Faq } from "@/components/landing/faq";
import { Resources } from "@/components/landing/resources";
import { FinalCta } from "@/components/landing/final-cta";
import { SiteFooter } from "@/components/landing/site-footer";

export function LandingPage({ locale }: { locale: Locale }) {
  // Hero stays un-wrapped (above the fold); everything below fades in on scroll.
  const sections = [
    SocialProof, Stats, Features, HowItWorks, UseCases, BillingShowcase,
    Security, Infrastructure, Integrations, Developer, Testimonials,
    CaseStudy, Comparison, CostEstimator, Pricing, Faq, Resources, FinalCta,
  ];

  return (
    <div className="flex min-h-screen flex-col">
      <HtmlLang locale={locale} />
      <SiteHeader locale={locale} />
      <main className="flex-1">
        <Hero locale={locale} />
        {sections.map((Section, i) => (
          <Reveal key={i}>
            <Section locale={locale} />
          </Reveal>
        ))}
      </main>
      <SiteFooter locale={locale} />
    </div>
  );
}
