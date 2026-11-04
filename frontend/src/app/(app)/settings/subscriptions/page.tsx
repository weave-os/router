"use client";

import { SettingsPage, SettingsSection } from "@/components/settings/SettingsPage";
import { SubscriptionLoginPanel } from "@/components/settings/SubscriptionLoginPanel";
import { Link2 } from "lucide-react";

export default function SubscriptionsSettingsPage() {
  return (
    <SettingsPage href="/settings/subscriptions">
      <SettingsSection
        icon={<Link2 className="size-4" />}
        title="Claude and Codex subscriptions"
        description="Link a Claude or ChatGPT subscription to someone's routing key. The router then serves their eligible requests from that subscription."
      >
        <SubscriptionLoginPanel />
      </SettingsSection>
    </SettingsPage>
  );
}
