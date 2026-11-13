import { useState } from "react";

/**
 * State for a drawer opened from a list of rows and paged through them: which row is open, and
 * returning focus to that row's trigger when the drawer closes.
 */
export function useDrawerPager<TKey>(getTriggerId: (key: TKey) => string) {
  const [isOpen, setIsOpen] = useState(false);
  const [selectedKey, setSelectedKey] = useState<TKey | null>(null);

  return {
    isOpen,
    onCloseAutoFocus: (event: Event) => {
      event.preventDefault();
      if (selectedKey === null) return;
      document.getElementById(getTriggerId(selectedKey))?.focus({ preventScroll: true });
    },
    onOpenChange: setIsOpen,
    open: (key: TKey) => {
      setSelectedKey(key);
      setIsOpen(true);
    },
    selectedKey,
    setSelectedKey,
  };
}
