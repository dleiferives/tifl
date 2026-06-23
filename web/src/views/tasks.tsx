export function TasksView(props: { sessionId: string }) {
  return (
    <section>
      <h1>Tasks</h1>
      <p>Tasks for session <code>{props.sessionId}</code> will mount here.</p>
    </section>
  );
}
