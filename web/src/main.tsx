import { createEffect, Match, Show, Switch, type JSX } from "solid-js";
import { render } from "solid-js/web";
import { initializeAuthentication } from "./auth";
import { createHashRouter, routeHref, sessionHref, type Route } from "./router";
import { appStore } from "./store";
import { AdminCallLogView, AdminCostView, AdminSessionView, AdminUserView } from "./views/admin";
import { DebugView } from "./views/debug";
import { HomeView } from "./views/home";
import { ImportView } from "./views/import";
import { LibraryView } from "./views/library";
import { LoginView } from "./views/login";
import { ReaderView } from "./views/reader";
import { SessionShellView } from "./views/session_shell";
import { SettingsView } from "./views/settings";
import { SkillsView } from "./views/skills";
import { StartView } from "./views/start";
import "./style.css";

function App() {
  const route = createHashRouter();

  createEffect(() => {
    if (appStore.authStatus() !== "anonymous" || route().name === "login") {
      return;
    }
    appStore.rememberReturnPath(route().path);
    window.location.hash = routeHref("/login");
  });

  return (
    <Show when={appStore.authStatus() !== "checking"} fallback={<AuthCheckingView />}>
      <Show when={appStore.authStatus() !== "anonymous" || route().name === "login"}>
        <AppLayout route={route()}>
          <Switch fallback={<NotFoundView path={route().path} />}>
            <Match when={route().name === "home"}><HomeView /></Match>
            <Match when={route().name === "login"}><LoginView /></Match>
            <Match when={route().name === "start"}><StartView /></Match>
            <Match when={route().name === "import"}><ImportView /></Match>
            <Match when={route().name === "library"}><LibraryView /></Match>
            <Match when={route().name === "settings"}><SettingsView /></Match>
            <Match when={route().name === "skills"}><SkillsView /></Match>
            <Match when={route().name === "session"}>
              <SessionShellView
                sessionId={(route() as Extract<Route, { name: "session" }>).sessionId}
                step={(route() as Extract<Route, { name: "session" }>).step}
              />
            </Match>
            <Match when={route().name === "generation"}>
              <RedirectTo to={sessionHref((route() as Extract<Route, { name: "generation" }>).sessionId, "read")} />
            </Match>
            <Match when={route().name === "debug"}>
              <DebugView sessionId={(route() as Extract<Route, { name: "debug" }>).sessionId} />
            </Match>
            <Match when={route().name === "admin"}>
              <AdminGuard route={route()}><AdminCallLogView /></AdminGuard>
            </Match>
            <Match when={route().name === "admin-cost"}>
              <AdminGuard route={route()}><AdminCostView /></AdminGuard>
            </Match>
            <Match when={route().name === "admin-session"}>
              <AdminGuard route={route()}>
                <AdminSessionView sessionId={(route() as Extract<Route, { name: "admin-session" }>).sessionId} />
              </AdminGuard>
            </Match>
            <Match when={route().name === "admin-user"}>
              <AdminGuard route={route()}>
                <AdminUserView userId={(route() as Extract<Route, { name: "admin-user" }>).userId} />
              </AdminGuard>
            </Match>
            <Match when={route().name === "reader"}>
              <Show
                when={(route() as Extract<Route, { name: "reader" }>).sessionId}
                fallback={
                  <ReaderView
                    storyId={(route() as Extract<Route, { name: "reader" }>).storyId}
                  />
                }
              >
                {(sessionId) => <RedirectTo to={sessionHref(sessionId(), "read")} />}
              </Show>
            </Match>
            <Match when={route().name === "phrases"}>
              <RedirectTo to={sessionHref((route() as Extract<Route, { name: "phrases" }>).sessionId, "read")} />
            </Match>
            <Match when={route().name === "tasks"}>
              <RedirectTo to={sessionHref((route() as Extract<Route, { name: "tasks" }>).sessionId, "tasks")} />
            </Match>
          </Switch>
        </AppLayout>
      </Show>
    </Show>
  );
}

function RedirectTo(props: { to: string }) {
  createEffect(() => {
    window.location.replace(props.to);
  });
  return (
    <section class="redirect-state" aria-busy="true">
      Opening session...
    </section>
  );
}

// AdminGuard renders admin pages only for admins; a non-admin who navigates
// directly to an /admin route sees the not-found page, so the surface is never
// exposed client-side (#24). The server enforces the same with a 404.
function AdminGuard(props: { route: Route; children: JSX.Element }) {
  return (
    <Show when={appStore.isAdmin()} fallback={<NotFoundView path={props.route.path} />}>
      {props.children}
    </Show>
  );
}

function AppLayout(props: { route: Route; children: JSX.Element }) {
  const isCurrent = (name: Route["name"]) => props.route.name === name ? "page" : undefined;
  const isAdminRoute = () => props.route.name.startsWith("admin");

  return (
    <>
      <header class="app-header">
        <a class="brand" href={routeHref("/")}>tifl</a>
        <nav class="app-nav" aria-label="Main navigation">
          <a href={routeHref("/")} aria-current={isCurrent("home")}>Home</a>
          <a href={routeHref("/import")} aria-current={isCurrent("import")}>Add story</a>
          <a href={routeHref("/library")} aria-current={isCurrent("library")}>Library</a>
          <a href={routeHref("/skills")} aria-current={isCurrent("skills")}>Skills</a>
          <a href={routeHref("/settings")} aria-current={isCurrent("settings")}>Settings</a>
          <Show when={appStore.isAdmin()}>
            <a href={routeHref("/admin")} aria-current={isAdminRoute() ? "page" : undefined}>Admin</a>
          </Show>
        </nav>
        <Show
          when={appStore.authStatus() !== "local"}
          fallback={<span class="auth-state">Local mode</span>}
        >
          <a class="auth-link" href={routeHref("/login")} aria-current={isCurrent("login")}>
            {appStore.user()?.email || "Login"}
          </a>
        </Show>
      </header>
      <main class="app-main" aria-busy={appStore.isBusy()}>
        {props.children}
      </main>
      <Show when={appStore.toast()}>
        {(toast) => (
          <aside class="toast" data-kind={toast().kind} role={toast().kind === "error" ? "alert" : "status"}>
            <span>{toast().message}</span>
            <button type="button" aria-label="Dismiss message" onClick={appStore.clearToast}>×</button>
          </aside>
        )}
      </Show>
    </>
  );
}

function AuthCheckingView() {
  return (
    <main class="auth-checking" aria-busy="true">
      <p class="brand">tifl</p>
      <p>Checking your session…</p>
    </main>
  );
}

function NotFoundView(props: { path: string }) {
  return (
    <section>
      <h1>Page not found</h1>
      <p>No route matches <code>{props.path}</code>.</p>
      <p><a href={routeHref("/")}>Return home</a></p>
    </section>
  );
}

const root = document.getElementById("root");
if (!root) {
  throw new Error("missing #root app mount");
}
render(() => <App />, root);
void initializeAuthentication();
