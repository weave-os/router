import { cn } from "../../tools/cn";
import { Icon } from "../atoms/Icon";
import { Card, CardProps } from "../molecules/Card";
import { Intent } from "../types";

export interface ErrorCardProps extends CardProps {
  description: string;
  title: string;
}

/**
 * Renders an error state.
 */
export function ErrorCard({
  children,
  className,
  description,
  title,
  ...cardProps
}: ErrorCardProps) {
  return (
    <Card
      intent={Intent.Danger}
      size="sm"
      icon={<Icon type="TriangleAlert" intent={Intent.Danger} className="size-5" />}
      className={cn("m-4", className)}
      {...cardProps}
    >
      <Card.Header>
        <Card.Title>{title}</Card.Title>
        <Card.Description>{description}</Card.Description>
      </Card.Header>
      {children}
    </Card>
  );
}
