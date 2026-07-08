import Link from "next/link";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";

interface SetupStep {
  n: number;
  title: string;
  description: string;
  cta: string;
  href: string;
}

const STEPS: SetupStep[] = [
  {
    n: 1,
    title: "资费套餐",
    description: "设置客户的发信单价 / 套餐。",
    cta: "去设置定价",
    href: "/admin/users",
  },
  {
    n: 2,
    title: "企业客户",
    description: "新建一个客户账号（自动开通对应租户与钱包）。",
    cta: "新建客户",
    href: "/admin/users",
  },
  {
    n: 3,
    title: "账户充值",
    description: "给客户钱包充值，余额用于发信扣费。",
    cta: "去充值",
    href: "/admin/users",
  },
  {
    n: 4,
    title: "配置账号 / 设备",
    description: "接入 WhatsApp 账号 / 节点设备。",
    cta: "配置设备",
    href: "/admin/devices",
  },
  {
    n: 5,
    title: "配置代理",
    description: "为设备配置出口代理网络。",
    cta: "配置代理",
    href: "/admin/resources",
  },
  {
    n: 6,
    title: "配置发送路由",
    description: "创建发送任务，编排消息如何发出。",
    cta: "创建发送任务",
    href: "/admin/campaigns",
  },
];

/** OKCC-style guided setup flow: six numbered steps mapping onboarding
 *  concepts (tariffs, tenants, wallet top-up, devices, proxies, routing)
 *  onto the existing admin pages that fulfill them. Pure navigation layer —
 *  no new backend, so this stays a server component. */
export function AdminSetupWizard() {
  return (
    <div className="space-y-6">
      <p className="max-w-2xl text-sm text-muted-foreground">
        按顺序完成以下六步，即可为一个新客户开通从定价到发送的完整链路。每一步都会跳转到对应的管理页面。
      </p>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {STEPS.map((step) => (
          <Card key={step.n} className="flex h-full flex-col">
            <CardContent className="flex flex-1 flex-col gap-4 pt-1">
              <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-brand-500/10 font-mono text-base font-semibold text-brand-600 dark:text-brand-400">
                {step.n}
              </span>
              <div className="flex-1 space-y-1">
                <h3 className="font-semibold tracking-tight">{step.title}</h3>
                <p className="text-sm text-muted-foreground">{step.description}</p>
              </div>
              <Button className="w-full" render={<Link href={step.href} />}>
                {step.cta}
              </Button>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  );
}
