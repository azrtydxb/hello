import { PageHeader } from "../design/azrty/components";
import { PhonesTabs } from "./phones/ui";

/** Stub: filled in by the Phones UI task. */
export function ProvFirmware() {
  return (
    <section aria-labelledby="page-title">
      <PageHeader eyebrow="Directory" title="Firmware" />
      <PhonesTabs />
    </section>
  );
}
