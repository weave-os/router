import { Icon } from "../../atoms/Icon";
import { Text } from "../../atoms/Text";

/**
 * Renders a nice empty state for a data table.
 */
export function DataTableEmpty() {
  return (
    <div className="flex h-full w-full flex-col items-center justify-center gap-4 text-wv-secondary">
      <div className="flex rounded-lg border p-2">
        <Icon type="Search" className="size-5" />
      </div>

      <Text variant="h4" as="p">
        No results found
      </Text>
    </div>
  );
}
