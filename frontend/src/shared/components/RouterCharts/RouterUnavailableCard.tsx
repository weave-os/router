import { ChartCard } from "@/shared/components/ChartCard";
import { ChartMessageCard } from "@/shared/components/Chart";
import { Card } from "@/shared/components/molecules/Card";
import { type Unavailable } from "@/shared/data";

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
