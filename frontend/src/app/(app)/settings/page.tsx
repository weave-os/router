"use client";

import { ConfigPanel } from "@/components/settings/ConfigPanel";
import { RouterKeysPanel } from "@/components/settings/RouterKeysPanel";
import { SettingsPage, SettingsSection } from "@/components/settings/SettingsPage";
import { KeyRound, Settings as SettingsIcon } from "lucide-react";

export default function GeneralSettingsPage() {
  return (
    <SettingsPage href="/settings">
      <SettingsSection
        icon={<KeyRound className="size-4" />}
        title="Router API keys"
        description="Keys used to authenticate requests to this router."
      >
        <RouterKeysPanel />
      </SettingsSection>

      <SettingsSection
        icon={<SettingsIcon className="size-4" />}
        title="Configuration"
        description="Runtime values set via environment variables."
      >
        <ConfigPanel />
      </SettingsSection>
    </SettingsPage>
  );
}
