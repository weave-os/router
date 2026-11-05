import { cn } from "../../../tools/cn";
import { Icon } from "../../atoms/Icon";
import { Appearance } from "../../types";
import { Button } from "../Button";

interface DefaultExpandButtonProps {
  isExpanded: boolean;
  onToggle: () => void;
}

export function DefaultExpandButton({ isExpanded, onToggle }: DefaultExpandButtonProps) {
  return (
    <Button
      size="icon"
      appearance={Appearance.Hollow}
      className={cn("size-6 shrink-0", isExpanded && "bg-wv-active hover:bg-wv-hover")}
      onClick={onToggle}
      aria-label={isExpanded ? "Collapse row" : "Expand row"}
      aria-expanded={isExpanded}
    >
      <Icon
        type="ChevronDown"
        className={cn("size-4 transition-transform duration-200", !isExpanded && "-rotate-90")}
      />
    </Button>
  );
}
