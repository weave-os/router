import { Fragment } from "react";

export function HighlightedText({ query, text }: { query: string; text: string }) {
  const trimmedQuery = query.trim();
  if (trimmedQuery === "") {
    return <>{text}</>;
  }

  const escapedQuery = trimmedQuery.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const regex = new RegExp(`(${escapedQuery})`, "gi");
  const parts = text.split(regex);

  return (
    <>
      {parts.map((part, index) => {
        if (part.toLowerCase() === trimmedQuery.toLowerCase()) {
          return (
            <mark key={index} className="rounded-sm">
              {part}
            </mark>
          );
        }
        return <Fragment key={index}>{part}</Fragment>;
      })}
    </>
  );
}
