import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { useRef, useState } from "react";

import { getChartMarkId, useRestoreChartMarkFocus } from "./useRestoreChartMarkFocus";

/** Remounts every mark whenever one loses focus, the way Recharts does on a highlight change. */
function RemountingMarks({ onClickMark }: { onClickMark?: (index: number) => void }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [remountKey, setRemountKey] = useState(0);
  const { handleBlur, handleFocus } = useRestoreChartMarkFocus(containerRef);

  return (
    <>
      <div
        ref={containerRef}
        onBlur={event => {
          handleBlur(event);
          setRemountKey(count => count + 1);
        }}
        onFocus={handleFocus}
      >
        {[0, 1, 2].map(index => (
          <span
            key={`${remountKey}-${index}`}
            data-chart-mark={getChartMarkId("ai", index)}
            onClick={() => onClickMark?.(index)}
            role="button"
            tabIndex={0}
          >
            {`mark ${index}`}
          </span>
        ))}
      </div>
      <button>after</button>
    </>
  );
}

describe("useRestoreChartMarkFocus", () => {
  it("lets Tab move through remounting marks and out of the chart", async () => {
    const user = userEvent.setup();
    render(<RemountingMarks />);

    for (const name of ["mark 0", "mark 1", "mark 2", "after"]) {
      await user.tab();
      await waitFor(() => expect(screen.getByRole("button", { name })).toHaveFocus());
    }
  });

  it("does not pull focus back after a pointer press elsewhere", async () => {
    const user = userEvent.setup();
    render(<RemountingMarks />);

    await user.tab();
    await waitFor(() => expect(screen.getByRole("button", { name: "mark 0" })).toHaveFocus());
    await user.click(document.body);

    await new Promise(resolve => requestAnimationFrame(resolve));
    expect(document.body).toHaveFocus();
  });

  it("clicks a mark while another mark has keyboard focus", async () => {
    const user = userEvent.setup();
    const onClickMark = vi.fn();
    render(<RemountingMarks onClickMark={onClickMark} />);

    await user.tab();
    await waitFor(() => expect(screen.getByRole("button", { name: "mark 0" })).toHaveFocus());
    await user.click(screen.getByRole("button", { name: "mark 2" }));

    expect(onClickMark).toHaveBeenCalledWith(2);
  });
});
