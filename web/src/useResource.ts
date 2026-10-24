import { useEffect, useRef, useState } from "react";
import { request } from "./api";
export type Resource<T> = { data?: T; loading: boolean; error?: string };
// Generation check also rejects transports that complete after cancellation.
export function useResource<T>(
  path: string | undefined,
  version = 0,
): Resource<T> {
  const generation = useRef(0);
  const [state, setState] = useState<Resource<T>>({ loading: Boolean(path) });
  const [owner, setOwner] = useState(path);
  useEffect(() => {
    const current = ++generation.current;
    const controller = new AbortController();
    setOwner(path);
    setState({ loading: Boolean(path) });
    if (path)
      request<T>(path, { signal: controller.signal })
        .then((data) => {
          if (current === generation.current)
            setState({ data, loading: false });
        })
        .catch((error) => {
          if (current === generation.current && !controller.signal.aborted)
            setState({ loading: false, error: error.message });
        });
    return () => {
      ++generation.current;
      controller.abort();
    };
  }, [path, version]);
  return owner === path ? state : { loading: Boolean(path) };
}
