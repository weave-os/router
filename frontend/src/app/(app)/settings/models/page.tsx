"use client";

import { ModelSelectionPanel } from "@/components/settings/ModelSelectionPanel";
import { ProviderSelectionPanel } from "@/components/settings/ProviderSelectionPanel";
import { RoutingPriorityPanel } from "@/components/settings/RoutingPriorityPanel";
import { SettingsPage, SettingsSection } from "@/components/settings/SettingsPage";
import { Filter, Network, SlidersHorizontal } from "lucide-react";

export default function ModelsSettingsPage() {
  return (
    <SettingsPage href="/settings/models">
      <SettingsSection
        icon={<SlidersHorizontal className="size-4" />}
        title="Routing priority"
        description="Bias routing toward stronger models or cheaper ones. Every request is balanced between the two. Leave as default to let the router decide."
      >
        <RoutingPriorityPanel />
      </SettingsSection>

      <SettingsSection
        icon={<Filter className="size-4" />}
        title="Model selection"
        description="Uncheck a model to drop it from routing decisions for this installation. Unchecked models are skipped at request time."
      >
        <ModelSelectionPanel />
      </SettingsSection>

      <SettingsSection
        icon={<Network className="size-4" />}
        title="Provider selection"
        description="Uncheck a provider to never serve requests through it, including failover. Models hosted only by unchecked providers become unroutable."
      >
        <ProviderSelectionPanel />
      </SettingsSection>
    </SettingsPage>
  );
}
