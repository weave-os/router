"use client";

import { Icon } from "@weave-os/ui/components/atoms/Icon";
import { Kbd } from "@weave-os/ui/components/atoms/Kbd";
import { Button } from "@weave-os/ui/components/molecules/Button";
import { Tooltip } from "@weave-os/ui/components/molecules/Tooltip";
import { Appearance } from "@weave-os/ui/components/types";
import { cn } from "@weave-os/ui/tools/cn";
import { KeyboardKey } from "@weave-os/ui/tools/keyboard";

import { useRef, useState } from "react";
import { useHotkeys } from "react-hotkeys-hook";

import { useIndividualTableSearch } from "./IndividualTableSearchContext";

/**
 * Search bar for filtering individuals in the table by name.
 */
export function IndividualTableSearchBar() {
  const { searchQuery, setSearchQuery } = useIndividualTableSearch();

  const inputRef = useRef<HTMLInputElement>(null);
  const [isFocused, setIsFocused] = useState(false);

  const hasQuery = searchQuery.trim().length > 0;

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === KeyboardKey.Escape) {
      e.preventDefault();
      setSearchQuery("");
      inputRef.current?.blur();
    }
  };

  useHotkeys(
    "mod+f",
    event => {
      event.preventDefault();
      inputRef.current?.focus();
      inputRef.current?.select();
    },
    { enableOnFormTags: true },
    [],
  );

  return (
    <div
      className={cn(
        "ml-auto hidden h-9 w-full max-w-80 cursor-text items-center gap-2 rounded-md border border-wv-subtle px-3 text-wv-secondary transition-colors md:flex",
        isFocused && "border-wv-focus ring-1 ring-wv-focus",
      )}
      onClick={() => inputRef.current?.focus()}
    >
      <Icon type="Search" className="size-4 shrink-0" />

      <input
        ref={inputRef}
        type="text"
        placeholder="Search individuals..."
        value={searchQuery}
        onChange={e => setSearchQuery(e.target.value)}
        onKeyDown={handleKeyDown}
        onFocus={() => setIsFocused(true)}
        onBlur={() => setIsFocused(false)}
        name="individual-search"
        aria-label="Search individuals"
        className="h-full w-0 min-w-16 flex-1 bg-transparent text-body text-wv-primary outline-none placeholder:text-wv-secondary"
      />

      {hasQuery ? (
        <Tooltip content="Clear">
          <Button
            type="button"
            size="icon"
            appearance={Appearance.Hollow}
            onClick={e => {
              e.stopPropagation();
              setSearchQuery("");
            }}
            className="size-6 shrink-0"
          >
            <Icon type="X" className="size-3" />
          </Button>
        </Tooltip>
      ) : (
        <div className="flex shrink-0 items-center gap-1">
          <Kbd>
            <Icon type="Command" className="size-3 shrink-0 stroke-[2.5]" />
          </Kbd>
          <Kbd>F</Kbd>
        </div>
      )}
    </div>
  );
}
