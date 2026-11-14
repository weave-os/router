"use client";

import { createContext, useContext, useMemo, useState } from "react";

interface IndividualTableSearchContextType {
  /** The current search query */
  searchQuery: string;
  /** Set the search query */
  setSearchQuery: (query: string) => void;
}

const IndividualTableSearchContext = createContext<IndividualTableSearchContextType | undefined>(
  undefined,
);

/**
 * Returns the individual table search context for filtering and highlighting rows.
 * Throws an error if used outside of an IndividualTableSearchProvider.
 */
export function useIndividualTableSearch() {
  const context = useContext(IndividualTableSearchContext);
  if (context == null) {
    throw new Error(
      "useIndividualTableSearch must be used within an IndividualTableSearchProvider",
    );
  }
  return context;
}

interface IndividualTableSearchProviderProps {
  children: React.ReactNode;
}

/**
 * Provides search state for filtering individuals in the table.
 */
export function IndividualTableSearchProvider({ children }: IndividualTableSearchProviderProps) {
  const [searchQuery, setSearchQuery] = useState("");

  const value = useMemo(
    () => ({
      searchQuery,
      setSearchQuery,
    }),
    [searchQuery],
  );

  return (
    <IndividualTableSearchContext.Provider value={value}>
      {children}
    </IndividualTableSearchContext.Provider>
  );
}
