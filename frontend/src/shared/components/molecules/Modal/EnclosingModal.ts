"use client";

import type * as Dialog from "@radix-ui/react-dialog";

import { createContext, useContext } from "react";

export interface EnclosingModal {
  /** Where the enclosing {@link Modal.Content} is portaled; `null` means `<body>`. */
  portalContainer: React.ComponentPropsWithoutRef<typeof Dialog.Portal>["container"] | null;
}

export const EnclosingModalContext = createContext<EnclosingModal | null>(null);

/** The {@link Modal.Content} this component renders inside, or `null` outside any modal. */
export function useEnclosingModal() {
  return useContext(EnclosingModalContext);
}

/** Whether this renders inside a {@link Modal.Content} portaled to `<body>`, covering the viewport. */
export function useIsInsideViewportModal() {
  const enclosingModal = useEnclosingModal();
  return enclosingModal != null && enclosingModal.portalContainer == null;
}
