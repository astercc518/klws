import type { Metadata } from "next";
import { DocsView } from "@/components/landing/docs-view";

export const metadata: Metadata = {
  title: { absolute: "API Docs · klws" },
  description: "REST API reference for the klws sending engine.",
  alternates: { canonical: "/en/docs" },
};

export default function EnDocsPage() {
  return <DocsView locale="en" />;
}
