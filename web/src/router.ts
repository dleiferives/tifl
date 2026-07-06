import { createSignal, onCleanup, onMount, type Accessor } from "solid-js";

export type Route =
  | { name: "home"; path: "/" }
  | { name: "login"; path: "/login" }
  | { name: "start"; path: "/start" }
  | { name: "import"; path: "/import" }
  | { name: "library"; path: "/library" }
  | { name: "settings"; path: "/settings" }
  | { name: "session"; path: string; sessionId: string; step: SessionStep }
  | { name: "generation"; path: string; sessionId: string }
  | { name: "debug"; path: string; sessionId: string }
  | { name: "reader"; path: string; storyId: string; sessionId?: string }
  | { name: "phrases"; path: string; sessionId: string }
  | { name: "tasks"; path: string; sessionId: string }
  | { name: "skills"; path: "/skills" }
  | { name: "not-found"; path: string };

export type SessionStep = "read" | "tasks" | "review";

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
  if (segments.length === 1 && segments[0] === "import") {
    return { name: "import", path: "/import" };
  }
  if (segments.length === 1 && segments[0] === "library") {
    return { name: "library", path: "/library" };
  }
  if (segments.length === 1 && segments[0] === "settings") {
    return { name: "settings", path: "/settings" };
  }
  if (segments.length === 1 && segments[0] === "skills") {
    return { name: "skills", path: "/skills" };
  }
  if (segments.length >= 2 && segments.length <= 3 && segments[0] === "session") {
    const step = segments[2] ?? "read";
    if (isSessionStep(step)) {
      return { name: "session", path, sessionId: segments[1], step };
    }
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

export function sessionHref(sessionID: string, step: SessionStep = "read"): string {
  return routeHref(`/session/${encodeURIComponent(sessionID)}/${step}`);
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

function isSessionStep(value: string): value is SessionStep {
  return value === "read" || value === "tasks" || value === "review";
}
