import { act, renderHook } from "@testing-library/react";

import { useInView } from "./useInView";

let observerCallback: IntersectionObserverCallback;
let observerOptions: IntersectionObserverInit | undefined;
const mockDisconnect = vi.fn();
const mockObserve = vi.fn();
const mockIntersectionObserver = vi.fn(function (
  cb: IntersectionObserverCallback,
  opts?: IntersectionObserverInit,
) {
  observerCallback = cb;
  observerOptions = opts;
  return { disconnect: mockDisconnect, observe: mockObserve, unobserve: vi.fn() };
});

function triggerIntersection(isIntersecting: boolean) {
  act(() => {
    observerCallback([{ isIntersecting } as IntersectionObserverEntry], {} as IntersectionObserver);
  });
}

describe("useInView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    globalThis.IntersectionObserver =
      mockIntersectionObserver as unknown as typeof IntersectionObserver;
  });

  test("returns false initially", () => {
    const { result } = renderHook(() => useInView());
    expect(result.current[1]).toBe(false);
  });

  test("returns true after intersection", () => {
    const { result } = renderHook(() => useInView());
    const node = document.createElement("div");

    act(() => {
      result.current[0](node);
    });
    triggerIntersection(true);

    expect(result.current[1]).toBe(true);
  });

  test("once: true (default) disconnects observer after first trigger", () => {
    const { result } = renderHook(() => useInView());
    const node = document.createElement("div");

    act(() => {
      result.current[0](node);
    });
    triggerIntersection(true);

    expect(mockDisconnect).toHaveBeenCalled();
    expect(result.current[1]).toBe(true);

    triggerIntersection(false);
    expect(result.current[1]).toBe(true);
  });

  test("once: false toggles with visibility", () => {
    const { result } = renderHook(() => useInView({ once: false }));
    const node = document.createElement("div");

    act(() => {
      result.current[0](node);
    });

    triggerIntersection(true);
    expect(result.current[1]).toBe(true);

    triggerIntersection(false);
    expect(result.current[1]).toBe(false);
  });

  test("cleanup on unmount", () => {
    const { result, unmount } = renderHook(() => useInView());
    const node = document.createElement("div");

    act(() => {
      result.current[0](node);
    });

    unmount();
    expect(mockDisconnect).toHaveBeenCalled();
  });

  test("handles null node", () => {
    const { result } = renderHook(() => useInView());
    const node = document.createElement("div");

    act(() => {
      result.current[0](node);
    });
    mockDisconnect.mockClear();

    act(() => {
      result.current[0](null);
    });

    expect(mockDisconnect).toHaveBeenCalled();
  });

  test("skips observer creation when already triggered in once mode", () => {
    const { result } = renderHook(() => useInView());
    const firstNode = document.createElement("div");
    const secondNode = document.createElement("div");

    act(() => {
      result.current[0](firstNode);
    });
    triggerIntersection(true);

    mockIntersectionObserver.mockClear();
    mockObserve.mockClear();

    act(() => {
      result.current[0](secondNode);
    });

    expect(mockIntersectionObserver).not.toHaveBeenCalled();
    expect(mockObserve).not.toHaveBeenCalled();
  });

  test("custom rootMargin is forwarded", () => {
    const { result } = renderHook(() => useInView({ rootMargin: "50px" }));
    const node = document.createElement("div");

    act(() => {
      result.current[0](node);
    });

    expect(observerOptions).toEqual({ rootMargin: "50px", threshold: 0 });
    expect(mockIntersectionObserver).toHaveBeenCalledWith(expect.any(Function), {
      rootMargin: "50px",
      threshold: 0,
    });
  });

  test("re-creates observer when options change", () => {
    const { rerender, result } = renderHook(
      ({ rootMargin }: { rootMargin: string }) => useInView({ rootMargin }),
      { initialProps: { rootMargin: "100px" } },
    );
    const node = document.createElement("div");

    act(() => {
      result.current[0](node);
    });

    expect(observerOptions?.rootMargin).toBe("100px");
    mockDisconnect.mockClear();

    rerender({ rootMargin: "300px" });

    act(() => {
      result.current[0](node);
    });

    expect(mockDisconnect).toHaveBeenCalled();
    expect(observerOptions).toEqual({ rootMargin: "300px", threshold: 0 });
  });
});
