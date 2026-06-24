import { createEffect, Match, Show, Switch, type JSX } from "solid-js";
import { render } from "solid-js/web";
import { initializeAuthentication } from "./auth";
import { createHashRouter, routeHref, type Route } from "./router";
import { appStore } from "./store";
import { GenerationView } from "./views/generation";
import { HomeView } from "./views/home";
import { LoginView } from "./views/login";
import { PhrasesView } from "./views/phrases";
import { ReaderView } from "./views/reader";
import { SettingsView } from "./views/settings";
import { SkillsView } from "./views/skills";
import { StartView } from "./views/start";
import { TasksView } from "./views/tasks";
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
            <Match when={route().name === "settings"}><SettingsView /></Match>
            <Match when={route().name === "skills"}><SkillsView /></Match>
            <Match when={route().name === "generation"}>
              <GenerationView sessionId={(route() as Extract<Route, { name: "generation" }>).sessionId} />
            </Match>
            <Match when={route().name === "reader"}>
              <ReaderView storyId={(route() as Extract<Route, { name: "reader" }>).storyId} />
            </Match>
            <Match when={route().name === "phrases"}>
              <PhrasesView sessionId={(route() as Extract<Route, { name: "phrases" }>).sessionId} />
            </Match>
            <Match when={route().name === "tasks"}>
              <TasksView sessionId={(route() as Extract<Route, { name: "tasks" }>).sessionId} />
            </Match>
          </Switch>
        </AppLayout>
      </Show>
    </Show>
  );
}

function AppLayout(props: { route: Route; children: JSX.Element }) {
  const isCurrent = (name: Route["name"]) => props.route.name === name ? "page" : undefined;

  return (
    <>
      <header class="app-header">
        <a class="brand" href={routeHref("/")}>tifl</a>
        <nav class="app-nav" aria-label="Main navigation">
          <a href={routeHref("/")} aria-current={isCurrent("home")}>Home</a>
          <a href={routeHref("/skills")} aria-current={isCurrent("skills")}>Skills</a>
          <a href={routeHref("/settings")} aria-current={isCurrent("settings")}>Settings</a>
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
