import { render } from "solid-js/web";
import { createSignal, onMount } from "solid-js";

// Scaffold entry point. The real app (reader, home/session list, tasks, settings)
// is built out per context/frontend-architecture.md and context/reader-mode.md.
function App() {
  const [health, setHealth] = createSignal("checking…");

  onMount(async () => {
    try {
      const res = await fetch("/healthz");
      setHealth(res.ok ? "ok" : `error ${res.status}`);
    } catch {
      setHealth("unreachable");
    }
  });

  return (
    <main style={{ "font-family": "system-ui, sans-serif", "max-width": "40rem", margin: "4rem auto", padding: "0 1rem" }}>
      <h1>tifl</h1>
      <p>Thinking In Foreign Languages — client scaffold.</p>
      <p>
        Server health: <strong>{health()}</strong>
      </p>
    </main>
  );
}

const root = document.getElementById("root");
if (root) {
  render(() => <App />, root);
}
