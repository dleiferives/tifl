import { routeHref } from "../router";

export function HomeView() {
  return (
    <section>
      <h1>Home</h1>
      <p>Your sessions and study controls will live here.</p>
      <p>
        <a class="button-link" href={routeHref("/login")}>Open login placeholder</a>
      </p>
    </section>
  );
}
