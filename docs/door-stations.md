# Door stations and the Door group

A door station (a Grandstream GDS37xx, 2N, Akuvox or DoorBird unit) registers with
Hello like a phone, with its own SIP credentials. Hello does not provision the unit;
configure its SIP account and set its call button to dial the Door group's number.

## Door station extension

Create an extension and turn on **Door station** (Extensions page), or send
`"kind": "door"` to `POST /api/v1/extensions`. A door station:

- has devices like any extension;
- has no voicemail box, takes no pickup, page, intercom (`403`) or MESSAGE (`403`, both
  directions), and cannot be a member of a ring group, paging group, pickup group or
  queue;
- is hidden from the extensions phonebook unless you set `directoryHidden` to false;
- can call only its **targets**: the extensions and ring groups ticked on it
  (`doorTargets`: `{"extensions":[id],"ringGroups":[id]}`, at most 50), and every ring
  group marked Door. Any other destination, an outside number, a feature code or a page
  included, is answered `403`;
- can never reach a trunk, even through a callee's forward-always, a group's external
  fallback, an IVR or a transfer, except an emergency route;
- makes calls that ring with the `doorbell` ring tag and have CDR kind `door`.

An extension that is a member of a group or queue cannot become a door station (`400`
on `kind`); remove it from the group first. Turning a user into a station switches
voicemail and intercom off. Turning it back brings its voicemail box back (off until you
enable it).

## Door group

Mark a ring group **Door group** (`"isDoor": true`). Any strategy works. A new Door group
with no ring tag gets `doorbell`. Calls to it, from a station or from anyone, have CDR
kind `door` and ring the members with the doorbell tone (see [ring tags](ring-tags.md)).
Door stations can always call Door groups.

## Not here yet

The door-open code (`*50`) and the doorbell webhook for non-SIP doorbells follow.
