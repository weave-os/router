"use client";

import { Button } from "@/components/molecules/Button";
import { Appearance } from "@/components/types";
import { INSTALL_SHELLS, type InstallShell } from "@/lib/installCommands";

interface InstallShellPickerProps {
  shell: InstallShell;
  onChange: (shell: InstallShell) => void;
}

/** Picks the terminal syntax for the install command; the env prefix differs per shell. */
export function InstallShellPicker({ shell, onChange }: InstallShellPickerProps) {
  return (
    <div role="radiogroup" aria-label="Terminal" className="flex flex-wrap gap-2">
      {INSTALL_SHELLS.map(s => (
        <Button
          key={s.value}
          type="button"
          role="radio"
          aria-checked={shell === s.value}
          size="sm"
          appearance={Appearance.Outlined}
          className={shell === s.value ? "!border-foreground/40" : "text-muted-foreground"}
          onClick={() => onChange(s.value)}
        >
          {s.label}
        </Button>
      ))}
    </div>
  );
}
