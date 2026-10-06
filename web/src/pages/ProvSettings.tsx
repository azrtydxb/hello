import { PageHeader } from "../design/azrty/components";
import { PhonesTabs } from "./phones/ui";

/** Stub: filled in by the Phones UI task. */
export function ProvSettings() {
  return (
    <section aria-labelledby="page-title">
      <PageHeader eyebrow="Directory" title="Provisioning settings" />
      <PhonesTabs />
    </section>
  );
}
