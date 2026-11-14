import { cva, VariantProps } from "class-variance-authority";

import React from "react";

import { cn } from "../../../tools/cn";

const textVariants = cva("leading-normal", {
  defaultVariants: {
    variant: "p",
  },
  variants: {
    variant: {
      h1: "font-display text-display font-semibold",
      h2: "font-display text-page-title font-semibold",
      h3: "text-section-title font-medium leading-relaxed",
      h4: "text-card-title font-medium",
      p: "text-base",
    },
  },
});

export interface TextProps
  extends
    React.PropsWithChildren,
    VariantProps<typeof textVariants>,
    React.HTMLAttributes<HTMLHeadingElement> {
  /**
   * If you need to render the text as a different HTML element than its variant, use this prop.
   * Otherwise stick with defining the variant only.
   */
  as?: "h1" | "h2" | "h3" | "h4" | "p" | null;
}

export const Text = React.forwardRef<HTMLHeadingElement, TextProps>(
  ({ as, children, className, variant, ...props }, ref) => {
    const Element = as ?? variant ?? "p";

    return (
      <Element ref={ref} className={cn(textVariants({ variant }), className)} {...props}>
        {children}
      </Element>
    );
  },
);
Text.displayName = "Text";
