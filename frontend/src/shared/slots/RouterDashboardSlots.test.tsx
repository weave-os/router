import { render, screen } from "@testing-library/react";

import {
  RouterDashboardSlot,
  RouterDashboardSlotName,
  RouterDashboardSlotsProvider,
} from "./RouterDashboardSlots";

test("renders host content for its slot only", () => {
  render(
    <RouterDashboardSlotsProvider
      slots={{ [RouterDashboardSlotName.DashboardAside]: <p>Credits panel</p> }}
    >
      <RouterDashboardSlot name={RouterDashboardSlotName.DashboardAside} />
      <RouterDashboardSlot
        fallback={<p>No plan</p>}
        name={RouterDashboardSlotName.SettingsSections}
      />
    </RouterDashboardSlotsProvider>,
  );

  expect(screen.getByText("Credits panel")).toBeInTheDocument();
  expect(screen.getByText("No plan")).toBeInTheDocument();
});
