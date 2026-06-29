import type { Metadata } from "next";
import { DocsView } from "@/components/landing/docs-view";

export const metadata: Metadata = {
  title: "API 文档",
  description: "klws 发送引擎的 REST API 接入文档。",
};

export default function DocsPage() {
  return <DocsView locale="zh" />;
}
