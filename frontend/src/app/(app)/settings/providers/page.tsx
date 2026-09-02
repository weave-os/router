"use client";

import { ProviderKeysPanel } from "@/components/settings/ProviderKeysPanel";
import { CodexOAuthPanel } from "@/components/settings/CodexOAuthPanel";
import { SettingsPage, SettingsSection } from "@/components/settings/SettingsPage";
import { Plug } from "lucide-react";

export default function ProviderKeysSettingsPage() {
  return (
    <SettingsPage href="/settings/providers">
      <SettingsSection
        icon={<Plug className="size-4" />}
        title="Provider API keys"
        description="Connect Codex OAuth or manage keys for Anthropic, OpenAI, Google, OpenRouter, and compatible gateways."
      >
        <CodexOAuthPanel />
        <ProviderKeysPanel />
      </SettingsSection>
    </SettingsPage>
  );
}
