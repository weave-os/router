"use client";

import {
  createContext,
  type HTMLAttributes,
  type PropsWithChildren,
  useCallback,
  useContext,
  useState,
} from "react";

interface WorkspaceFrameContextValue {
  setWorkspaceFrame: (frame: HTMLDivElement | null) => void;
  workspaceFrame: HTMLDivElement | null;
}

const WorkspaceFrameContext = createContext<WorkspaceFrameContextValue | null>(null);

export function WorkspaceFrameProvider({ children }: PropsWithChildren) {
  const [workspaceFrame, setWorkspaceFrame] = useState<HTMLDivElement | null>(null);

  return (
    <WorkspaceFrameContext.Provider value={{ setWorkspaceFrame, workspaceFrame }}>
      {children}
    </WorkspaceFrameContext.Provider>
  );
}

export function WorkspaceFrame({ children, ...props }: HTMLAttributes<HTMLDivElement>) {
  const sharedWorkspaceFrame = useContext(WorkspaceFrameContext);
  const [workspaceFrame, setWorkspaceFrame] = useState<HTMLDivElement | null>(null);
  const frameContext = sharedWorkspaceFrame ?? { setWorkspaceFrame, workspaceFrame };
  const registerFrame = frameContext.setWorkspaceFrame;
  const onFrameRef = useCallback(
    (frame: HTMLDivElement | null) => registerFrame(frame),
    [registerFrame],
  );

  return (
    <WorkspaceFrameContext.Provider value={frameContext}>
      <div {...props} ref={onFrameRef}>
        {children}
      </div>
    </WorkspaceFrameContext.Provider>
  );
}

export function useWorkspaceFrame() {
  return useContext(WorkspaceFrameContext)?.workspaceFrame ?? null;
}
