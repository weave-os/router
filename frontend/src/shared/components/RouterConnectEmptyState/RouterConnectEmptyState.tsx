"use client";

import { Icon } from "@weave-os/ui/components/atoms/Icon";
import { Text } from "@weave-os/ui/components/atoms/Text";
import { Button } from "@weave-os/ui/components/molecules/Button";
import { Card } from "@weave-os/ui/components/molecules/Card";
import { Appearance } from "@weave-os/ui/components/types";

import { createContext, useContext } from "react";

const RouterConnectHrefContext = createContext<string | undefined>(undefined);

/** Where an empty chart's "Connect Weave Router" button goes; without one the card has no button. */
export const RouterConnectHrefProvider = RouterConnectHrefContext;

export interface RouterConnectEmptyStateProps {
  message: string;
}

/** Shown in a router chart's body when it has no data, linking to where the host sets the router up. */
export function RouterConnectEmptyState({ message }: RouterConnectEmptyStateProps) {
  const href = useContext(RouterConnectHrefContext);

  return (
    <div className="flex h-full items-center justify-center">
      <Card className="flex w-full flex-col items-center gap-4 border-dashed p-8 text-center">
        <div className="rounded border border-border-darker p-2 text-wv-secondary">
          <Icon type="Link" className="size-5" />
        </div>

        <Text variant="p" className="text-wv-secondary">
          {message}
        </Text>

        {href != null && (
          <Button href={href} prefetch={false} appearance={Appearance.Tinted} size="sm">
            Connect Weave Router
          </Button>
        )}
      </Card>
    </div>
  );
}
