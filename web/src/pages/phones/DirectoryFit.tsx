import { useEffect, useState } from "react";
import type { Id } from "../../api";
import { getPhoneDirectory, type PhoneDirectory } from "../../api/provSettings";
import { Alert } from "../../design/azrty/components";

/**
 * Warns when the phone's model cannot hold all of its address book: the
 * contacts past the model's capacity are cut from the end of the delivery
 * order (extensions, company, the group's named books).
 */
export function DirectoryFit({ phoneId }: { phoneId: Id }) {
  const [dir, setDir] = useState<PhoneDirectory | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    getPhoneDirectory(phoneId, controller.signal)
      .then(setDir)
      .catch(() => undefined); // a missing directory is not an error to show
    return () => controller.abort();
  }, [phoneId]);

  if (!dir?.supported) return null;
  if (dir.dropped > 0) {
    return (
      <Alert tone="warn" title="Some contacts do not fit on this phone">
        The address book holds {dir.delivered} contacts but this model takes{" "}
        {dir.capacity}. The last {dir.dropped} in the delivery order are left
        out; the extensions and company books come first. Remove a book from the
        phone&apos;s group or shorten one.
      </Alert>
    );
  }
  return null;
}
