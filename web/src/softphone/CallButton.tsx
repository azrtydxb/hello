// "Call" from other pages (S-18): places the call from the softphone when it is
// on, otherwise opens the dock so the user can switch it on. It renders nothing
// where there is no softphone (a user without an extension, a test).
import { Button } from "../design/azrty/components";
import { useSoftphone } from "./SoftphoneProvider";

export interface CallButtonProps {
  number: string;
  /** Who is called, for the accessible name. */
  label?: string;
  size?: "sm" | "md";
}

export function CallButton({ number, label, size = "sm" }: CallButtonProps) {
  const phone = useSoftphone();
  if (!phone || !phone.supported || !number) return null;
  const busy = phone.state.reg === "connecting" || phone.starting;
  return (
    <Button
      variant="secondary"
      size={size}
      icon="phone"
      disabled={busy}
      aria-label={`Call ${label ?? number} from this browser`}
      title={
        phone.enabled
          ? `Call ${number} from this browser`
          : "Switch the browser phone on to call from here"
      }
      onClick={() => {
        phone.dial(number);
      }}
    >
      Call
    </Button>
  );
}
