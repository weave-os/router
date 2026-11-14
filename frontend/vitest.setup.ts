import "@testing-library/jest-dom/vitest";

if (typeof window !== "undefined") {
  // jsdom has no matchMedia. This default matches nothing; tests that exercise a media query
  // install their own.
  window.matchMedia = query => ({
    addEventListener: () => {},
    addListener: () => {},
    dispatchEvent: () => false,
    matches: false,
    media: query,
    onchange: null,
    removeEventListener: () => {},
    removeListener: () => {},
  });

  // jsdom has no IntersectionObserver. Everything observed is in view at once, so viewport-gated
  // content renders as it would on a page that fits the screen.
  class InViewIntersectionObserver implements IntersectionObserver {
    readonly root = null;
    readonly rootMargin = "";
    readonly scrollMargin = "";
    readonly thresholds = [];
    private readonly callback: IntersectionObserverCallback;

    constructor(callback: IntersectionObserverCallback) {
      this.callback = callback;
    }

    disconnect() {}

    observe(target: Element) {
      this.callback(
        [{ isIntersecting: true, target } as unknown as IntersectionObserverEntry],
        this,
      );
    }

    takeRecords() {
      return [];
    }

    unobserve() {}
  }
  window.IntersectionObserver = InViewIntersectionObserver;
}
