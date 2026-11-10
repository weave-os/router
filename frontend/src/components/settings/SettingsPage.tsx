"use client";

import { Page } from "@/shared/components/Page";

import { Icon } from "@weave-os/ui/components/atoms/Icon";
import { Text } from "@weave-os/ui/components/atoms/Text";
import { PageHeader } from "@weave-os/ui/components/PageHeader";

import { type ReactNode } from "react";

import { settingsNavItem } from "./nav";

export interface SettingsPageProps {
  href: string;
  children: ReactNode;
}

/** Shell for a single settings tab: header sourced from the shared nav config. */
export function SettingsPage({ href, children }: SettingsPageProps) {
  const item = settingsNavItem(href);
  return (
    <Page
      header={
        <PageHeader
          left={
            <Text
              variant="h4"
              as="h2"
              className="flex flex-row items-center gap-2 whitespace-nowrap"
            >
              <Icon type={item.icon} className="size-4" />
              {item.label}
            </Text>
          }
        />
      }
    >
      <div className="flex w-full max-w-text-width flex-col gap-2">{children}</div>
    </Page>
  );
}

export interface SettingsSectionProps {
  icon: ReactNode;
  title: string;
  description: ReactNode;
  children: ReactNode;
}

export function SettingsSection({ children, description, icon, title }: SettingsSectionProps) {
  return (
    <Page.Section
      className="py-3"
      header={
        <Page.SectionHeader>
          {icon}
          <Text variant="h4" as="h3">
            {title}
          </Text>
        </Page.SectionHeader>
      }
    >
      <Text className="text-xs text-muted-foreground">{description}</Text>
      {children}
    </Page.Section>
  );
}
