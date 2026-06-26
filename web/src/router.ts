import { createSignal, onCleanup, onMount, type Accessor } from "solid-js";

export type Route =
  | { name: "home"; path: "/" }
  | { name: "login"; path: "/login" }
  | { name: "start"; path: "/start" }
  | { name: "settings"; path: "/settings" }
  | { name: "generation"; path: string; sessionId: string }
  | { name: "debug"; path: string; sessionId: string }
  | { name: "reader"; path: string; storyId: string; sessionId?: string }
  | { name: "phrases"; path: string; sessionId: string }
  | { name: "tasks"; path: string; sessionId: string }
  | { name: "skills"; path: "/skills" }
  | { name: "not-found"; path: string };

export function createHashRouter(): Accessor<Route> {
  const [route, setRoute] = createSignal(parseHash(window.location.hash));

  const update = () => setRoute(parseHash(window.location.hash));
  onMount(() => window.addEventListener("hashchange", update));
  onCleanup(() => window.removeEventListener("hashchange", update));

  return route;
}

export function parseHash(hash: string): Route {
  const [rawPath, rawQuery = ""] = hash.replace(/^#/, "").split("?");
  const path = normalizePath(rawPath);
  const query = new URLSearchParams(rawQuery);
  const segments = path.split("/").filter(Boolean).map(decodeSegment);

  if (segments.length === 0 || (segments.length === 1 && segments[0] === "home")) {
    return { name: "home", path: "/" };
  }
  if (segments.length === 1 && segments[0] === "login") {
    return { name: "login", path: "/login" };
  }
  if (segments.length === 1 && segments[0] === "start") {
    return { name: "start", path: "/start" };
  }
  if (segments.length === 1 && segments[0] === "settings") {
    return { name: "settings", path: "/settings" };
  }
  if (segments.length === 1 && segments[0] === "skills") {
    return { name: "skills", path: "/skills" };
  }
  if (segments.length === 2 && segments[0] === "generation") {
    return { name: "generation", path, sessionId: segments[1] };
  }
  if (segments.length === 2 && segments[0] === "debug") {
    return { name: "debug", path, sessionId: segments[1] };
  }
  if (segments.length === 2 && segments[0] === "reader") {
    return { name: "reader", path, storyId: segments[1], sessionId: query.get("sessionId") || undefined };
  }
  if (segments.length === 2 && segments[0] === "phrases") {
    return { name: "phrases", path, sessionId: segments[1] };
  }
  if (segments.length === 2 && segments[0] === "tasks") {
    return { name: "tasks", path, sessionId: segments[1] };
  }
  return { name: "not-found", path };
}

export function routeHref(path: string): string {
  return `#${normalizePath(path)}`;
}

function normalizePath(path: string): string {
  const withLeadingSlash = path.startsWith("/") ? path : `/${path}`;
  const withoutTrailingSlash = withLeadingSlash.replace(/\/+$/, "");
  return withoutTrailingSlash || "/";
}

function decodeSegment(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}
