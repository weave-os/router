import { useCallback, useEffect, useRef, useState } from "react";

/**
 * Hook that tracks whether an element is visible in the viewport using
 * IntersectionObserver. Returns a ref callback to attach to the target
 * element and a boolean indicating visibility.
 *
 * @param options.rootMargin - Margin around the root (default: "200px").
 *   Positive values trigger observation before the element enters the viewport.
 * @param options.once - If true (default), stops observing after the first
 *   intersection. The element stays "in view" permanently after that.
 */
export function useInView(options?: {
  once?: boolean;
  rootMargin?: string;
}): [ref: (node: Element | null) => void, isInView: boolean] {
  const { once = true, rootMargin = "200px" } = options ?? {};
  const [isInView, setIsInView] = useState(false);
  const observerRef = useRef<IntersectionObserver | null>(null);
  const triggeredRef = useRef(false);

  useEffect(() => {
    return () => {
      if (observerRef.current != null) {
        observerRef.current.disconnect();
        observerRef.current = null;
      }
    };
  }, []);

  const ref = useCallback(
    (node: Element | null) => {
      if (observerRef.current != null) {
        observerRef.current.disconnect();
        observerRef.current = null;
      }

      if (once && triggeredRef.current) return;

      if (node == null) return;

      const observer = new IntersectionObserver(
        ([entry]) => {
          if (once && triggeredRef.current) return;

          const visible = entry.isIntersecting;
          setIsInView(visible);

          if (visible && once) {
            triggeredRef.current = true;
            observer.disconnect();
            observerRef.current = null;
          }
        },
        { rootMargin, threshold: 0 },
      );

      observer.observe(node);
      observerRef.current = observer;
    },
    [rootMargin, once],
  );

  return [ref, isInView];
}
