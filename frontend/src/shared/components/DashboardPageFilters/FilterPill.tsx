"use client";

import { createContext, forwardRef, useContext } from "react";

import { cn } from "../../tools/cn";
import { SafeOmit } from "../../tools/types";
import { Icon } from "../atoms/Icon";
import { Button, type ButtonProps } from "../molecules/Button";
import { Appearance } from "../types";

export interface FilterPillProps extends React.PropsWithChildren {
  /**
   * If provided, the pill will have a remove button that calls this function when clicked.
   */
  onRemove?: () => void;
  removeClassName?: string;
}

interface FilterPillContextValue {
  hasRemove: boolean;
}

const FilterPillContext = createContext<FilterPillContextValue>({
  hasRemove: false,
});

export interface FilterPillLabelProps extends React.PropsWithChildren {
  className?: string;
}

/**
 * Renders a pill indicating an active filter. This component just handles presentation and the
 * remove button.
 */
export function FilterPill({ children, onRemove, removeClassName }: FilterPillProps) {
  return (
    <FilterPillContext.Provider value={{ hasRemove: onRemove != null }}>
      <div className="flex h-7 shrink-0 grow-0 items-stretch gap-px text-body drop-shadow-[0_2px_2px_rgb(0_0_0_/_0.04)]">
        {children}

        {onRemove != null && <FilterPill.Remove className={removeClassName} onClick={onRemove} />}
      </div>
    </FilterPillContext.Provider>
  );
}

FilterPill.Label = function FilterPillLabel({ children, className }: FilterPillLabelProps) {
  return (
    <div
      className={cn(
        "flex items-center gap-1.5 rounded-l-lg bg-filter-pill px-2 py-1 font-medium text-wv-secondary only:rounded-lg",
        className,
      )}
    >
      {children}
    </div>
  );
};

interface FilterPillRemoveProps {
  className?: string;
  onClick: () => void;
}

FilterPill.Remove = function FilterPillRemove({ className, onClick }: FilterPillRemoveProps) {
  return (
    <Button
      type="button"
      aria-label="Remove filter"
      appearance={Appearance.Hollow}
      onClick={onClick}
      className={cn(
        "relative h-full shrink-0 rounded-none rounded-r-lg bg-filter-pill px-1.5 py-1 text-wv-secondary before:absolute before:-inset-y-1.5 before:-right-1.5 before:left-0 before:content-[''] focus-visible:z-10",
        className,
      )}
    >
      <Icon type="X" className="size-3.5" />
    </Button>
  );
};

interface FilterPillButtonProps extends SafeOmit<ButtonProps, "appearance" | "size"> {
  showChevron?: boolean;
}

FilterPill.Button = forwardRef<HTMLButtonElement, FilterPillButtonProps>(
  ({ children, className, showChevron = true, ...props }, ref) => {
    const { hasRemove } = useContext(FilterPillContext);

    return (
      <Button
        {...props}
        ref={ref}
        appearance={Appearance.Hollow}
        className={cn(
          "group relative h-auto rounded-none bg-filter-pill py-1 font-medium text-wv-primary before:absolute before:-inset-y-1.5 before:left-0 before:content-[''] focus-visible:z-10",
          hasRemove
            ? "gap-2 pl-3 pr-2.5 before:right-0"
            : "gap-1.5 rounded-r-lg pl-2 pr-1.5 before:-right-1.5",
          className,
        )}
      >
        {children}
        {showChevron && (
          <span className="inline-flex shrink-0 grow-0 transition-transform duration-200 group-aria-expanded:rotate-180">
            <Icon type="ChevronDown" className="size-3 text-wv-secondary" />
          </span>
        )}
      </Button>
    );
  },
);

FilterPill.Button.displayName = "FilterPill.Button";
