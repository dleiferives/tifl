export function GenerationView(props: { sessionId: string }) {
  return (
    <section>
      <h1>Generating session</h1>
      <p>Generation progress for session <code>{props.sessionId}</code> will appear here.</p>
    </section>
  );
}
