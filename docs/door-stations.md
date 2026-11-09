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

## Video and the camera picture

Two separate things, both optional:

**Video in the call.** A door station that offers H.264 video in its SDP (as the caller,
or when a user dials its number) has the video carried to the phone that answers, if that
phone's SDP accepts it. The anchor opens a second pair of ports for the stream and relays
its RTP and its RTCP (the phone's keyframe requests) unchanged; both SDPs carry a video
m-line with Hello's own address. A phone that does not take the video gets none, and the
station's offer is answered with a refused (port 0) video line. A call with no station
never carries video. After a takeover by another node, a transfer or a re-anchor the call
continues with audio only. Only H.264 over plain RTP is carried (no SRTP, no VP8).

**The camera picture in the console.** Give a station the address of its camera under
**Extensions**, the station's drawer, **Camera** (administrators), or with
`PUT /api/v1/extensions/{id}/camera`: an MJPEG stream (`multipart/x-mixed-replace`) or a
snapshot address (a JPEG, fetched again every 2 s), http or https, with the camera's user
name and password (HTTP basic). The password is stored sealed and never returned. Hello
fetches the picture itself (`GET /api/v1/extensions/{id}/camera/view`, viewers and above),
so the browser needs no route to the camera and never sees its credentials. An `rtsp://`
address is refused: a browser cannot show it, and Hello does not transcode.

The fetch uses the guarded client that every outbound request of an administrator's
choosing uses: loopback and link-local addresses are never reachable, and a camera on a
private network is reachable only when that network is listed in
`HELLO_DOOR_CAMERA_ALLOW` (comma-separated CIDRs such as `192.168.10.0/24`, on
hello-control). A camera outside it answers `502` naming the setting. One node shows at
most eight pictures at once (`429` above), and an MJPEG stream ends after ten minutes.

## Grandstream WP door systems

A WP8x6 or WP810/820/822/825 lists the stations that can call its extension (a station that
names the extension, or a ring group it is in, or any station when the extension is in a
Door group) as door systems in its configuration: up to ten entries from `P32000` in steps
of five (service type 0, GDS; account 1; the station's name and number). The access
password `P32004` is never written: Hello sends the sealed door-open code itself when the
answering user dials `*50`, so the code never reaches a phone's configuration. These
P-values follow the official WP8x6 (1.0.3.39) and WP810/822/825 (1.0.11.83) templates;
whether a WP then shows the station's video and an Open door key for a station with no
access password is for a WP on the bench to confirm. The WP856 and the Yealink, Poly,
Snom and Fanvil phones get none.
