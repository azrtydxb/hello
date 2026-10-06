import { EmptyState, PageHeader } from "../design/azrty/components";

interface PlaceholderProps {
  title: string;
  phase: number;
}

/** A nav entry whose page is not built yet. */
export function Placeholder({ title, phase }: PlaceholderProps) {
  return (
    <section aria-labelledby="page-title">
      <PageHeader title={title} />
      <EmptyState
        icon="hammer"
        title="Not built yet"
        description={`Arrives in Phase ${phase}.`}
      />
    </section>
  );
}
