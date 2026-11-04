import { WorkspaceFrame } from "./WorkspaceFrame";

export interface SidebarLayoutProps extends React.PropsWithChildren {
  sidebar: React.ReactNode;
}

export function SidebarLayout({ children, sidebar }: SidebarLayoutProps) {
  return (
    <div className="surface-navigation flex h-full w-full flex-row items-stretch justify-center gap-1 overflow-hidden p-1 sm:gap-0 sm:p-0">
      <div className="surface-navigation relative z-20 flex h-full min-w-0 shrink-0">{sidebar}</div>

      <WorkspaceFrame className="relative min-h-0 min-w-0 flex-1 rounded-md sm:rounded-l-xl sm:rounded-r-none">
        {/* The workspace's left edge is its only on-screen edge, so the shadow casts sideways onto the sidebar. */}
        <div
          aria-hidden="true"
          className="pointer-events-none absolute inset-0 z-[21] rounded-[inherit] shadow-wv-workspace"
        />
        <div className="surface-workspace relative h-full min-h-0 min-w-0 overflow-auto rounded-[inherit] border-[0.5px] border-wv-subtle dark:border-transparent">
          {children}
        </div>
      </WorkspaceFrame>
    </div>
  );
}

SidebarLayout.Loading = function SidebarLayoutLoading() {
  return <main className="surface-navigation flex h-full w-full" />;
};
