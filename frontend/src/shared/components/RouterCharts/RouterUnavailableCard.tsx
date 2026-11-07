import { ChartMessageCard } from "@weave-os/ui/components/Chart";
import { ChartCard } from "@weave-os/ui/components/ChartCard";
import { Card } from "@weave-os/ui/components/molecules/Card";

import { type Unavailable } from "../../data";

export interface RouterUnavailableCardProps {
  className?: string;
  subtitle?: string;
  title: string;
  unavailable: Unavailable;
}

/** A card the host cannot serve, kept in its usual chrome so every tab keeps the same layout. */
export function RouterUnavailableCard({
  className,
  subtitle,
  title,
  unavailable,
}: RouterUnavailableCardProps) {
  return (
    <ChartCard className={className} title={title} subtitle={subtitle}>
      <ChartMessageCard>
        <Card.Header>
          <Card.Title>Not available</Card.Title>
          <Card.Description>{unavailable.message}</Card.Description>
        </Card.Header>
      </ChartMessageCard>
    </ChartCard>
  );
}
