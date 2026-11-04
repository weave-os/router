import { type ReactNode, useMemo, useState } from "react";

import { Icon } from "../../atoms/Icon";
import { Input } from "../../atoms/Input";
import { Text } from "../../atoms/Text";
import { Appearance } from "../../types";
import { Button } from "../Button";

const DEFAULT_MAX_VISIBLE = 50;

interface SearchableListProps<T> {
  ariaLabel?: string;
  /**
   * `filteredItems` is every item matching the search across all pages, which is what bulk
   * selection should act on; `visibleItems` is only the rendered page or capped slice.
   */
  children: (visibleItems: T[], filteredItems: T[]) => ReactNode;
  getSearchValue: (item: NoInfer<T>) => string;
  itemLabel?: string;
  items: T[];
  /**
   * Caps visible results when {@link pageSize} is omitted. Ignored in paginated mode.
   */
  maxVisible?: number;
  /**
   * When set, filters across the full list then pages through matches with prev/next controls.
   */
  pageSize?: number;
  placeholder?: string;
}

/**
 * Renders a search input that filters a list of items. By default it caps visible
 * results; pass {@link SearchableListProps.pageSize} to page through the full filtered list.
 */
export function SearchableList<T>({
  ariaLabel,
  children,
  getSearchValue,
  itemLabel = "items",
  items,
  maxVisible = DEFAULT_MAX_VISIBLE,
  pageSize,
  placeholder = "Search...",
}: SearchableListProps<T>) {
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);

  const filtered = useMemo(() => {
    const trimmed = search.trim();
    if (trimmed === "") return items;
    const lower = trimmed.toLowerCase();
    return items.filter(item => getSearchValue(item).toLowerCase().includes(lower));
  }, [items, search, getSearchValue]);

  const { clampedPage, start, totalPages, visible } = getVisibleWindow({
    filtered,
    maxVisible,
    page,
    pageSize,
  });

  return (
    <>
      <div className="relative w-full">
        <Icon
          type="Search"
          className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-wv-secondary"
        />
        <Input
          aria-label={ariaLabel ?? placeholder}
          placeholder={placeholder}
          value={search}
          onChange={e => {
            setSearch(e.target.value);
            setPage(0);
          }}
          className="pl-9"
        />
      </div>

      {children(visible, filtered)}

      {pageSize != null && totalPages > 1 && (
        <PaginationControls
          clampedPage={clampedPage}
          end={start + visible.length}
          itemLabel={itemLabel}
          onNext={() => setPage(clampedPage + 1)}
          onPrev={() => setPage(clampedPage - 1)}
          start={start}
          total={filtered.length}
          totalPages={totalPages}
        />
      )}

      {pageSize == null && filtered.length > maxVisible && (
        <Text variant="p" className="w-full text-center text-body text-wv-secondary">
          Showing {maxVisible} of {filtered.length} {itemLabel}. Use search to find a specific one.
        </Text>
      )}

      {filtered.length === 0 && search.trim() !== "" && (
        <Text variant="p" className="w-full text-center text-body text-wv-secondary">
          No {itemLabel} match &ldquo;{search}&rdquo;
        </Text>
      )}
    </>
  );
}

interface GetVisibleWindowArgs<T> {
  filtered: T[];
  maxVisible: number;
  page: number;
  pageSize: number | undefined;
}

function getVisibleWindow<T>({ filtered, maxVisible, page, pageSize }: GetVisibleWindowArgs<T>) {
  if (pageSize == null) {
    return {
      clampedPage: 0,
      start: 0,
      totalPages: 1,
      visible: filtered.slice(0, maxVisible),
    };
  }

  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize));
  const clampedPage = Math.min(page, totalPages - 1);
  const start = clampedPage * pageSize;
  return {
    clampedPage,
    start,
    totalPages,
    visible: filtered.slice(start, start + pageSize),
  };
}

interface PaginationControlsProps {
  clampedPage: number;
  end: number;
  itemLabel: string;
  onNext: () => void;
  onPrev: () => void;
  start: number;
  total: number;
  totalPages: number;
}

function PaginationControls({
  clampedPage,
  end,
  itemLabel,
  onNext,
  onPrev,
  start,
  total,
  totalPages,
}: PaginationControlsProps) {
  return (
    <nav
      aria-label="Pagination"
      className="flex w-full flex-col items-center justify-center gap-3 sm:flex-row"
    >
      <Text variant="p" className="text-body text-wv-secondary">
        Showing {start + 1}–{end} of {total} {itemLabel}
      </Text>
      <div className="flex items-center gap-1">
        <Button
          size="icon"
          appearance={Appearance.Tinted}
          aria-label="Previous page"
          onClick={onPrev}
          disabled={clampedPage === 0}
          className="size-11"
        >
          <Icon type="ChevronLeft" className="size-4" />
        </Button>
        <Text variant="p" className="min-w-16 text-center text-body text-wv-secondary">
          {clampedPage + 1} of {totalPages}
        </Text>
        <Button
          size="icon"
          appearance={Appearance.Tinted}
          aria-label="Next page"
          onClick={onNext}
          disabled={clampedPage >= totalPages - 1}
          className="size-11"
        >
          <Icon type="ChevronRight" className="size-4" />
        </Button>
      </div>
    </nav>
  );
}
