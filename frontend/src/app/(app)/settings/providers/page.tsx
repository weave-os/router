"use client";

import { ProviderKeysPanel } from "@/components/settings/ProviderKeysPanel";
import { SettingsPage, SettingsSection } from "@/components/settings/SettingsPage";
import { Plug } from "lucide-react";

export default function ProviderKeysSettingsPage() {
  return (
    <SettingsPage href="/settings/providers">
      <SettingsSection
        icon={<Plug className="size-4" />}
        title="Provider API keys"
        description="Bring your own provider keys, or point at an Anthropic- or OpenAI-compatible gateway."
      >
        <ProviderKeysPanel />
      </SettingsSection>
    </SettingsPage>
  );
}
