import { useEffect, useRef } from "react";

/**
 * Returns a boolean ref that indicates whether the component is mounted.
 */
export function useIsMounted() {
  const isMounted = useRef(true);

  useEffect(() => {
    isMounted.current = true;

    return () => {
      isMounted.current = false;
    };
  }, []);

  return isMounted;
}
