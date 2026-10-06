import { EmptyState, LinkButton, PageHeader } from "../design/azrty/components";

/** Any path the console has no page for. */
export function NotFound() {
  return (
    <section aria-labelledby="page-title">
      <PageHeader title="Page not found" />
      <EmptyState
        icon="map-pin-off"
        title="Nothing lives at this address"
        description="The link may be out of date, or the page may have moved."
        action={
          <LinkButton to="/" icon="layout-dashboard">
            Back to the dashboard
          </LinkButton>
        }
      />
    </section>
  );
}
