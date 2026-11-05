import { unreachable } from "../../../../tools/types";
import { Intent } from "../../../types";
import { IconType } from "../Icon";

export function getIconForIntent(intent: Intent): IconType {
  switch (intent) {
    case Intent.Danger:
    case Intent.Warning:
      return "CircleAlert";
    case Intent.Default:
    case Intent.Primary:
      return "Info";
    case Intent.Success:
      return "Check";
    default:
      unreachable(intent);
  }
}
