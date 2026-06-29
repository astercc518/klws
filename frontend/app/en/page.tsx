import type { Metadata } from "next";
import { LandingPage } from "@/components/landing/landing-page";

export const metadata: Metadata = {
  title: { absolute: "klws · Enterprise WhatsApp Growth Engine" },
  description:
    "Financial-grade billing, ban-proof isolation, and high deliverability — the WhatsApp concurrency engine built for cross-border marketing.",
  alternates: { canonical: "/en" },
};

export default function Page() {
  return <LandingPage locale="en" />;
}
