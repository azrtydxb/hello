interface PlaceholderProps {
  title: string;
  phase: number;
}

export function Placeholder({ title, phase }: PlaceholderProps) {
  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">{title}</h1>
      <p className="muted">Arrives in Phase {phase}.</p>
    </section>
  );
}
