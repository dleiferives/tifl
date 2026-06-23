export function ReaderView(props: { storyId: string }) {
  return (
    <section>
      <h1>Reader</h1>
      <p>The keyboard reader for story <code>{props.storyId}</code> will mount here.</p>
    </section>
  );
}
