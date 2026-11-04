import { isUnavailable, useRouterDashboardDataSource } from "../../data";
import { Text } from "../atoms/Text";
import { Page } from "../Page";
import { PageHeader } from "../PageHeader";
import { RouterUnavailableCard } from "../RouterCharts/RouterUnavailableCard";

export interface RouterIndividualsPageProps {
  /** Host-rendered individuals table, shown when the data source serves individuals. */
  children?: React.ReactNode;
}

export function RouterIndividualsPage({ children }: RouterIndividualsPageProps) {
  const { individuals } = useRouterDashboardDataSource();

  return (
    <Page
      header={
        <PageHeader
          left={
            <Text tabIndex={-1} variant="h4" as="h2" className="truncate">
              Individuals
            </Text>
          }
          leftColumnClassName="truncate"
        />
      }
    >
      <Page.Section>
        {isUnavailable(individuals) ? (
          <RouterUnavailableCard title="Individuals" unavailable={individuals} />
        ) : (
          children
        )}
      </Page.Section>
    </Page>
  );
}
