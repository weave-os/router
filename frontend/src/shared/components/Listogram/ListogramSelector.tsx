import React, { useState } from "react";

import { cn } from "../../tools/cn";
import { Icon } from "../atoms/Icon";
import { Button } from "../molecules/Button";
import { Popover } from "../molecules/Popover";
import { Appearance } from "../types";

export interface ListogramSelectorProps<TOption extends string> {
  className?: string;
  /**
   * If provided, overrides the label for each option.
   */
  labels?: Partial<Record<TOption, string>>;
  option: TOption;
  options: readonly TOption[];
  setOption: (option: TOption) => void;
  title?: React.ReactNode;
}

/**
 * Allows the user to select an option for a listogram.
 */
export function ListogramSelector<TOption extends string>({
  className,
  labels,
  option,
  options,
  setOption,
  title,
}: ListogramSelectorProps<TOption>) {
  const [isOpen, setIsOpen] = useState(false);

  return (
    <Popover open={isOpen} onOpenChange={setIsOpen}>
      <Popover.Trigger>
        <Button appearance={Appearance.Hollow} size="sm" className={cn("px-2", className)}>
          <span>
            {title != null && <>{title}: </>}
            <strong className="font-semibold">{labels?.[option] ?? option}</strong>
          </span>

          <Icon type="ChevronDown" />
        </Button>
      </Popover.Trigger>

      <Popover.Content className="flex w-52 flex-col gap-1 p-1" align="start">
        {options.map(value => (
          <Button
            key={value}
            size="sm"
            appearance={Appearance.Hollow}
            className="w-full justify-start font-normal"
            onClick={() => {
              setOption(value);
              setIsOpen(false);
            }}
            aria-selected={option === value}
          >
            {labels?.[value] ?? value}
          </Button>
        ))}
      </Popover.Content>
    </Popover>
  );
}
