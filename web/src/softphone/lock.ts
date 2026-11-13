// One phone per browser profile (S-18): the tab that holds a Web Lock is the
// phone, the others show a read-only chip and may ask for the lock. Without
// the Web Locks API the tab is the phone (a fresh registration replaces the
// old binding through reg-id, so the worst case is a brief double ring).

export const PHONE_LOCK = "hello-softphone";

export interface PhoneLock {
  /** Try to become the phone. Resolves true when this tab holds the lock. */
  acquire(): Promise<boolean>;
  /**
   * Queue for the lock; `granted` runs when this tab becomes the phone (the
   * holder closed or released). Safe to call while already queued.
   */
  request(granted: () => void): void;
  release(): void;
  held(): boolean;
}

interface LockApi {
  request(
    name: string,
    opts: { ifAvailable?: boolean; mode?: "exclusive" },
    cb: (lock: unknown) => Promise<void> | void,
  ): Promise<void>;
}

/** A PhoneLock over navigator.locks, or a trivially granted one without it. */
export function createPhoneLock(
  locks: LockApi | undefined = (
    globalThis.navigator as { locks?: LockApi } | undefined
  )?.locks,
): PhoneLock {
  let free: (() => void) | null = null;
  let held = false;
  let queued = false;
  // The callback of a granted lock stays pending until release(): that is
  // what holds the lock.
  const hold = (lock: unknown): Promise<void> | void => {
    if (!lock) return;
    held = true;
    return new Promise<void>((resolve) => {
      free = () => {
        held = false;
        free = null;
        resolve();
      };
    });
  };
  return {
    acquire() {
      if (!locks) {
        held = true;
        return Promise.resolve(true);
      }
      if (held) return Promise.resolve(true);
      return new Promise<boolean>((resolve) => {
        locks
          .request(
            PHONE_LOCK,
            { ifAvailable: true, mode: "exclusive" },
            (l) => {
              resolve(!!l);
              return hold(l);
            },
          )
          .catch(() => resolve(false));
      });
    },
    request(granted) {
      if (!locks) {
        held = true;
        granted();
        return;
      }
      if (held || queued) return;
      queued = true;
      locks
        .request(PHONE_LOCK, { mode: "exclusive" }, (l) => {
          queued = false;
          const p = hold(l);
          granted();
          return p;
        })
        .catch(() => {
          queued = false;
        });
    },
    release() {
      free?.();
      held = false;
    },
    held: () => held,
  };
}
