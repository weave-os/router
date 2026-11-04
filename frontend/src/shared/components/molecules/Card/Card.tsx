import { cva, VariantProps } from "class-variance-authority";

import React from "react";

import { cn } from "../../../tools/cn";
import { Skeleton } from "../../atoms/Skeleton";
import { Text, TextProps } from "../../atoms/Text";
import { Intent } from "../../types";

const cardVariants = cva(
  "surface-card group flex flex-col gap-x-4 rounded-lg border border-[var(--wv-card-border)] bg-[var(--wv-card-fill)]",
  {
    defaultVariants: {
      intent: Intent.Default,
      size: "default",
    },

    variants: {
      intent: {
        [Intent.Danger]: "card-callout card-danger",
        [Intent.Default]: "card-default",
        [Intent.Primary]: "card-callout card-primary",
        [Intent.Success]: "card-callout card-success",
        [Intent.Warning]: "card-callout card-warning",
      },

      size: {
        default: "gap-y-6 p-6",
        sm: "card-small gap-y-4 p-4",
      },
    },
  },
);

export interface CardProps
  extends React.HTMLAttributes<HTMLDivElement>, VariantProps<typeof cardVariants> {
  icon?: React.ReactNode;
}

type CardType = React.ForwardRefExoticComponent<CardProps & React.RefAttributes<HTMLDivElement>> & {
  /**
   * Renders the content for a card. Should be a child of a {@link Card}.
   */
  Content: typeof CardContent;

  /**
   * Renders the description for a card. Should be a child of a {@link Card.Header}.
   */
  Description: typeof CardDescription;

  /**
   * Renders the footer for a card. Should be a child of a {@link Card}.
   */
  Footer: typeof CardFooter;

  /**
   * Renders the header for a card. Should be a child of a {@link Card}. Typically renders
   * {@link Card.Title} and/or {@link Card.Description} as children.
   */
  Header: typeof CardHeader;

  /**
   * Renders a loading state for a card.
   */
  Loading: typeof CardLoading;

  /**
   * Renders the title for a card. Should be a child of a {@link Card.Header}.
   */
  Title: typeof CardTitle;
};

/**
 * Renders a generic card component.
 *
 * @example
 * <Card>
 *   <Card.Header>
 *     <Card.Title>Title</Card.Title>
 *     <Card.Description>Description</Card.Description>
 *   </Card.Header>
 *   <Card.Content>Content</Card.Content>
 *   <Card.Footer>Footer</Card.Footer>
 * </Card>
 */
export const Card: CardType = React.forwardRef<HTMLDivElement, CardProps>(
  ({ children, className, icon, intent, size, ...props }, ref) => {
    const hasIcon = icon != null;
    const small = size === "sm";

    return (
      <div
        ref={ref}
        className={cn(
          cardVariants({ intent: intent ?? Intent.Default, size }),
          hasIcon ? "flex-row" : "flex-col",
          className,
        )}
        {...props}
      >
        {hasIcon ? (
          <>
            <div className="flex shrink-0 grow-0 text-[var(--wv-card-heading)]">{icon}</div>
            <div className={cn("flex min-w-0 grow flex-col", small ? "gap-y-4" : "gap-y-6")}>
              {children}
            </div>
          </>
        ) : (
          children
        )}
      </div>
    );
  },
) as CardType;
Card.displayName = "Card";

const CardHeader = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={cn("flex flex-col gap-2", className)} {...props} />
  ),
);
CardHeader.displayName = "CardHeader";

export interface CardTitleProps extends React.HTMLAttributes<HTMLHeadingElement> {
  as?: TextProps["as"];
  /** @default "h3" */
  variant?: TextProps["variant"];
}

function CardTitle({ className, variant, ...props }: CardTitleProps) {
  return (
    <Text
      variant={variant ?? "h3"}
      className={cn(
        "leading-none text-[var(--wv-card-heading)]",
        (variant == null || variant === "h3" || variant === "h4") &&
          "font-[number:var(--wv-card-title-weight)]",
        variant == null && "group-[.card-small]:text-base group-[.card-small]:leading-tight",
        className,
      )}
      {...props}
    />
  );
}

function CardDescription({ className, ...props }: React.HTMLAttributes<HTMLParagraphElement>) {
  return <Text className={cn("text-body text-[var(--wv-card-body)]", className)} {...props} />;
}

const CardContent = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => <div ref={ref} className={className} {...props} />,
);
CardContent.displayName = "CardContent";

const CardFooter = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={cn("flex items-center p-6 pt-0", className)} {...props} />
  ),
);
CardFooter.displayName = "CardFooter";

const CardLoading = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  (props, ref) => {
    return (
      <Card ref={ref} {...props}>
        <Card.Header>
          <Card.Title>
            <Skeleton className="h-7 w-1/2 max-w-24" />
          </Card.Title>
          <Card.Description>
            <Skeleton className="h-3 w-3/4 max-w-36" as="span" />
          </Card.Description>
        </Card.Header>
        <Card.Content className="flex flex-col gap-1">
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-5/6" />
        </Card.Content>
      </Card>
    );
  },
);
CardLoading.displayName = "CardLoading";

Card.Header = CardHeader;
Card.Title = CardTitle;
Card.Description = CardDescription;
Card.Content = CardContent;
Card.Footer = CardFooter;
Card.Loading = CardLoading;
