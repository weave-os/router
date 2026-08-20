"use client";

import { Button } from "@/components/molecules/Button";
import { Appearance } from "@/components/types";
import { Check, Copy } from "lucide-react";
import { useState } from "react";

interface CopyBlockProps {
  value: string;
  /** Accessible label for the copy button. Defaults to "Copy". */
  title?: string;
}

/**
 * A monospace block with a copy button. Long install commands scroll
 * horizontally rather than wrapping, so the command stays selectable as one
 * line if the user prefers to copy by hand.
 */
export function CopyBlock({ value, title }: CopyBlockProps) {
  const [copied, setCopied] = useState(false);

  function handleCopy() {
    navigator.clipboard.writeText(value).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  }

  return (
    <div className="relative">
      <pre className="w-full overflow-x-auto rounded-lg bg-muted p-3 pr-24 font-mono text-2xs text-foreground">
        <code>{value}</code>
      </pre>
      <Button
        appearance={Appearance.Outlined}
        size="sm"
        onClick={handleCopy}
        title={title ?? "Copy"}
        aria-label={copied ? "Copied" : (title ?? "Copy")}
        className="absolute right-2 top-2 bg-background"
      >
        {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
        {copied ? "Copied" : "Copy"}
      </Button>
    </div>
  );
}
