"use client";

import { SettingsPage, SettingsSection } from "@/components/settings/SettingsPage";
import {
  SubscriptionLoginPanel,
  SubscriptionSeatsPanel,
} from "@/components/settings/SubscriptionLoginPanel";
import { Link2, Users } from "lucide-react";
import { useCallback, useState } from "react";

export default function SubscriptionsSettingsPage() {
  const [refreshKey, setRefreshKey] = useState(0);
  const refreshSeats = useCallback(() => setRefreshKey(key => key + 1), []);
  return (
    <SettingsPage href="/settings/subscriptions">
      <SettingsSection
        icon={<Link2 className="size-4" />}
        title="Claude and Codex subscriptions"
        description="Link a Claude or ChatGPT subscription to someone's routing key. The router then serves their eligible requests from that subscription."
      >
        <SubscriptionLoginPanel onLinked={refreshSeats} />
      </SettingsSection>
      <SettingsSection
        icon={<Users className="size-4" />}
        title="Linked subscriptions"
        description="Unlink a subscription to stop serving requests from it, or to link it to someone else."
      >
        <SubscriptionSeatsPanel refreshKey={refreshKey} />
      </SettingsSection>
    </SettingsPage>
  );
}
