# Provision phones automatically

Assign a phone's MAC address to an extension and the phone configures
itself: it finds Hello, fetches its vendor's files over HTTPS with a token
of its own, and registers through Kamailio. This is the preferred path for
Yealink, Poly, Grandstream, Snom and Fanvil phones; any other brand works
with a [generic template](#write-a-generic-template). The manual,
per-setting path stays in [phones.md](phones.md).

Values below are kw's: provisioning is published at
`https://prov.hello.kw.watteel.lab` (`HELLO_PROV_PUBLIC_URL`), and phones
register with Kamailio at `192.168.10.101:30508`. The console's
**Phones → Settings** shows the same values for any deployment, computed by
`GET /api/v1/prov/settings`.

## How a phone finds Hello

A phone gets its provisioning URL one of three ways:

| Way                                           | When to use it                                                  | Token in the URL                       |
| --------------------------------------------- | --------------------------------------------------------------- | -------------------------------------- |
| [DHCP option 66](#dhcp-option-66-160-and-43)  | phones on a network whose DHCP server you control               | handed out once, by trust on first use |
| [Vendor redirect service](#redirect-services) | phones shipped to another site; Snom, Yealink, Grandstream      | registered with the vendor by Hello    |
| [Manual URL entry](#manual-url-entry)         | a single phone, or a vendor with no redirect API (Poly, Fanvil) | typed into the phone's web UI          |

Every way ends the same: the phone fetches
`https://prov.hello.kw.watteel.lab/p/<token>/<file>`, Hello renders the
file for that phone, and the phone registers.

## Adding phones

In the console, **Directory → Phones → Add phone**: the MAC (any common
format), the vendor and model, the extension, optionally a device of that
extension, a label and BLF keys. Or through the API:

```sh
curl -sb cookies.txt -H 'Content-Type: application/json' \
  -d '{"mac":"80:5e:c0:12:34:56","vendor":"yealink","model":"T54W","extensionId":12,"blf":["102","103"],"enabled":true}' \
  https://hello.kw.watteel.lab/api/v1/phones
```

- Without a device, Hello creates one for the extension. With one, its SIP
  secret is **rotated**: Hello stores only digest hashes of old secrets, so
  it seals a new one it can hand to the phone (`secretRotated: true`).
  Anything else registered with the old secret must be reprovisioned.
- The response shows the phone's per-device URL **once**
  (`provisioningUrl`). Keep it only if you will type it into the phone.
- Every phone gets a random 20-character admin password for its web UI,
  set by the built-in templates. **Reveal** (audited) shows it; **Rotate**
  sets a new one at the next fetch.
- A new phone is **armed** for one DHCP hand-off (below).

### CSV import

**Phones → Import** (or `POST /api/v1/phones/import?dryRun=true`, then
without `dryRun`) takes one phone per line:

```text
mac,vendor,model,extension,label,blf
805ec0123456,yealink,T54W,101,Reception,102;103
0004f2abcdef,poly,VVX 450,102,,
```

Columns: `mac,vendor,model,extension[,label][,blf]`; the header row is
optional; BLF numbers are separated by `;` or spaces. The dry run checks
every row (MAC, vendor, model, extension, duplicates in the file and in
Hello) and changes nothing. The import creates every phone in one
transaction, or none if any row fails. It creates a device per phone.

## DHCP option 66, 160 and 43

Give the phones' network this value; it is the same for every vendor:

| Vendor      | DHCP option                     | Value                                      |
| ----------- | ------------------------------- | ------------------------------------------ |
| Yealink     | option 66 (option 43 also read) | `http://prov.hello.kw.watteel.lab/p/boot/` |
| Poly        | option 160 (read before 66), 66 | `http://prov.hello.kw.watteel.lab/p/boot/` |
| Grandstream | option 66                       | `http://prov.hello.kw.watteel.lab/p/boot/` |
| Snom        | option 66                       | `http://prov.hello.kw.watteel.lab/p/boot/` |
| Fanvil      | option 66                       | `http://prov.hello.kw.watteel.lab/p/boot/` |

It is plain HTTP on purpose: a factory-new phone does not yet trust Hello's
CA, so its first contact cannot be HTTPS. Nothing secret is served there
except the one-time token of the hand-off below.

### MikroTik RouterOS

RouterOS 7, for a phone network `192.168.20.0/24`:

```routeros
/ip dhcp-server option
add name=hello-prov-66 code=66 value="'http://prov.hello.kw.watteel.lab/p/boot/'"
add name=hello-prov-160 code=160 value="'http://prov.hello.kw.watteel.lab/p/boot/'"
/ip dhcp-server option sets
add name=hello-phones options=hello-prov-66,hello-prov-160
/ip dhcp-server network
set [find address=192.168.20.0/24] dhcp-option-set=hello-phones
```

### ISC dhcpd and Kea

ISC dhcpd (`dhcpd.conf`):

```text
option poly-boot-server code 160 = text;
subnet 192.168.20.0 netmask 255.255.255.0 {
  option tftp-server-name "http://prov.hello.kw.watteel.lab/p/boot/";  # option 66
  option poly-boot-server "http://prov.hello.kw.watteel.lab/p/boot/";  # option 160
}
```

Kea DHCPv4 (`kea-dhcp4.conf`, inside `"Dhcp4"`):

```json
"option-def": [
  { "name": "poly-boot-server", "code": 160, "type": "string", "space": "dhcp4" }
],
"subnet4": [{
  "id": 20,
  "subnet": "192.168.20.0/24",
  "option-data": [
    { "name": "tftp-server-name", "data": "http://prov.hello.kw.watteel.lab/p/boot/" },
    { "name": "poly-boot-server", "data": "http://prov.hello.kw.watteel.lab/p/boot/" }
  ]
}]
```

Option 43 is only needed for a Yealink network where option 66 is taken by
something else; give it the same URL as a string.

### Trust on first use

Option 66 gives every phone the same URL, so a DHCP phone has no token yet.
It gets one by trust on first use:

1. The phone asks the boot path for its vendor's common file. Hello answers
   with no secrets: the CA install, the re-check settings and the boot URL.
2. The phone asks for its per-MAC file. If that MAC belongs to a phone in
   Hello that is **armed**, Hello answers once with a bootstrap file that
   carries only the CA and the phone's per-device HTTPS URL (token
   included), no SIP secret and no admin password, and **disarms** the
   phone in the same transaction (`boot_handoff` in the fetch log).
3. From then on the phone fetches over HTTPS with its token and gets its
   account.

A later boot request for a disarmed MAC gets an empty `404`, is logged as
`boot_reclaim` and flags the phone **boot reclaimed**: either the phone was
factory-reset, or something else is claiming its MAC. If it was a reset,
**Re-arm** the phone (`POST /api/v1/phones/{id}/rearm`, audited): that
rotates its token at once and arms it for one more hand-off.

Set `HELLO_PROV_BOOT_CIDRS` to the phone networks to refuse hand-offs to any
other source (`boot_denied`; the phone stays armed). Phones provisioned by
a redirect service or a typed URL already have their token: their first
HTTPS fetch disarms them, so the boot path never hands their token out.

## Install Hello's CA certificate

On kw the provisioning host's certificate comes from the cluster's own
`cluster-ca`, which no phone trusts out of the box. Phones must install it
before their first HTTPS fetch:

- **DHCP phones** get it from the boot path automatically (Yealink by URL;
  Poly and Grandstream receive the certificate itself in the boot file).
- **Everyone else** installs it once by hand from
  `http://prov.hello.kw.watteel.lab/p/ca.crt` (PEM) or
  `http://prov.hello.kw.watteel.lab/p/ca.der` (DER), in the phone's web UI
  (paths per vendor below). Check the fingerprint against the SHA-256 the
  console shows under **Phones → Settings** before you trust it.

The CA URL is plain HTTP for the same reason as the boot path. The rendered
configs keep installing it at every re-check, so a renewed certificate
reaches the phones on its own.

## Manual URL entry

Paste the per-device URL from **Add phone** (or **Rotate token**) into the
phone's web UI, after installing the CA. Logged in as the phone's admin:

| Vendor      | Provisioning URL                                                                               | CA certificate                                                        |
| ----------- | ---------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| Yealink     | Settings → Auto Provision → Server URL; then Auto Provision Now                                | Security → Trusted Certificates → upload `ca.crt`                     |
| Poly        | Settings → Provisioning Server: Server Type `HTTPS`, Server Address the URL                    | Settings → Network → TLS → CA certificate (custom CA 1)               |
| Grandstream | Maintenance → Upgrade and Provisioning: Config Upgrade Via `HTTPS`, Config Server Path the URL | Maintenance → Security Settings → Trusted CA Certificates             |
| Snom        | Advanced → Update → Setting URL (the URL as shown, ending in `{mac}`)                          | Advanced → Certificates (custom-CA upload unconfirmed for all models) |
| Fanvil      | System → Auto Provision: Server Type `HTTPS`, Server URL the URL                               | System → Security → Trusted Certificates (unconfirmed)                |

Menu names vary a little between firmware versions.

## Vendor notes

### Yealink

Model T54W and the other T4x/T5x models. Files: `y000000000000.boot`
(optional, answered `404`), the model's common file `y0000000000XX.cfg`
(`XX` is the model ID, `96` for the T54W), then `<mac>.cfg` with the
account; `<mac>-local.cfg` and `<mac>-contact.xml` are answered `404`
(`-contact.xml` is the phone's local contact backup, which Hello's template
does not switch on; the address book is a remote phonebook, see
[Address books](#address-books)). The User-Agent carries the MAC, which
Hello checks against the phone's.

### Poly

VVX (UC Software) and Edge E / CCX (PolyOS). The phone asks for
`<mac>.cfg`, which Hello serves as a per-MAC master naming
`<mac>-hello.cfg`; a phone that falls back to `000000000000.cfg` gets the
same. The phone's own uploads (`<mac>-phone.cfg`, logs, and a `PUT` of
`<mac>-directory.xml`) are accepted and discarded; a `GET` of
`<mac>-directory.xml` is the phone's address book.

`device.dhcp.bootSrvUseOpt` is `Static` in the boot file and the hand-off:
the phone then ignores every DHCP option for the provisioning server and
keeps `device.prov.serverName`. Poly's configuration examples (BootServer-
Static.cfg) and FusionPBX write the values as the words `Default`, `Custom`
and `Static`; the number `2` Hello used before is in no document.

### Grandstream

GXP, GRP, GXV and WP. The phone tries `cfg<mac>`, `cfg<mac>.bin` (both
`404`) and then `cfg<mac>.xml`, the P-value file with the account.

Firmware: Grandstream treats P192 as a firmware server _directory_ and
appends the model's own file name (`wp810fw.bin` for the WP822,
`wp8x6fw.bin` for the WP836), so the file sets P192 to
`.Firmware.Dir` without its scheme (`host/p/<token>/fw`, no trailing
slash; Grandstream's firmware upgrade guide says not to include a leading
`http://`, and the HTTPS example is `x.x.x.x/xampp`), and the phone
fetches `/p/<token>/fw/<file>`, which Hello serves from the pinned file of
that name. Pin the firmware under the exact file name the phone asks for.
P6767 (firmware upgrade via) carries the scheme instead: `2` for HTTPS and
`1` for HTTP, taken from the URL (`UpgradeVia`; Grandstream's codes are
0 TFTP, 1 HTTP, 2 HTTPS, 3 FTP, 4 FTPS); P232 and P233 (file prefix and postfix) stay empty;
P238 is `0`, so the phone checks for firmware at every boot. These values
come from the Grandstream WP8xx administration guides (Maintenance →
Upgrade and Provisioning) and are unconfirmed on a phone, so verify them.
The phone also asks for `ring<N>.bin` and `ring<N>.ogg` in that directory;
Hello answers those with an empty `404` (`not_found`, no block counted).

Config server: P237 (Config Server Path) is likewise `host/p/<token>`, no
scheme and no trailing slash, and P212 (Config Upgrade Via) is `2` for the
HTTPS device path (`1` if Hello is ever served over HTTP). The SIP device
provisioning guide describes P237 as the config server's FQDN or IP, the
phone appends `/cfg<mac>.xml`, and the firmware guide says server paths take
no scheme; the protocol is only ever chosen by the via setting. The
templates use the `noscheme` and `urlvia` functions for this. The previous
form (a scheme and a trailing slash in P237) worked on phones in the field;
if a model is found that needs the scheme, copy the template and put
`{{xml .Prov.URL}}` back.

Time zone: P64 and P246 (Self-Defined Time Zone) carry a POSIX TZ string
(`CET-1CEST,M3.5.0,M10.5.0/3`); the guide's syntax is
`std offset dst [offset],start[/time],end[/time]`. Whether a given model
reads the string from P64 or only from P246 is unconfirmed (verify).

### Snom

D series. Booted, the phone asks for `snom<model>.htm` and
`snom<model>-<MAC>.htm`; Hello sets its setting server to the per-device URL
with `{mac}`, which the phone fills in with its MAC on every re-check.

### Fanvil

X series. The model's common file (`F0V0X5U00000.cfg` for the X5U,
`F0V00X600000.cfg` for the X6; the low-end X, H and i models use
`f0<model>hw1.100.cfg`) and `<mac>.cfg` with the account. The phone's
auto-provision settings take the server and the path apart: `Flash Server IP`
is the bare host, `Flash File Name` the path ending in `$mac.cfg`, `Flash
Protocol` `5` for HTTPS, and `Flash Mode` `2` makes it re-check every `Flash
Interval` hours. A pinned firmware goes in `Auto Image Url`, the file's full
URL. These key names follow Fanvil's X-series configuration files; they are
unconfirmed on a phone, so verify them.

### Still to verify on a phone

No vendor document confirms these; the phone emulator models the documented
or most likely behaviour and the settings stay as they are until a phone
shows otherwise.

- Poly `device.prov.serverName` with a scheme and a trailing slash, and a
  full URL in the master's `APP_FILE_PATH`.
- Grandstream: `P237` without a scheme on every model (the guides say so,
  and the protocol codes `P212`, `P6767` and `P330` are unconfirmed on a
  phone), the BLF key numbers, and P64 against P246 for the zone string.
- Snom `firmware` is the firmware's full URL from firmware 10.1.38; older
  firmware ignores it (`firmware_status`, an XML file naming the URL, is the
  way there). `admin_mode_password` is the keypad PIN, not the web password.
- Fanvil `CACertURL`, the web password, the BLF keys and the phonebook keys.
- Time zones on a phone: see [Time zones](#time-zones); the Fanvil keys
  (`Time Zone`, `DST Type` and the rule fields) and the Poly daylight-saving
  keys are unconfirmed on hardware.

## Time zones

`HELLO_PROV_TIMEZONE` is an IANA name (`Europe/Brussels`), which no vendor
accepts. Hello derives each vendor's form from the IANA rules in the binary
(`internal/prov/zones.go`, `ZoneInfo`, for the year of the render; the
rules are read from the zone database, not copied from a table). A daylight
rule is the nth weekday of a month at a wall-clock hour, which covers the
zones in use; a zone whose offset changes any other way is sent as its
standard offset with daylight saving off, and an unknown name sends no zone
at all, leaving the phone's own.

| Vendor      | Setting                                                                                                                                                                                                                 | Europe/Brussels                                        |
| ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| Yealink     | `local_time.time_zone`, `time_zone_name` (a label, sent when known), `summer_time` 0/1, `dst_time_type` 1, `start_time`, `end_time` (month/week/weekday/hour, week 5 = last, weekday 7 = Sunday), `offset_time` minutes | `+1`, `Belgium(Brussels)`, `3/5/7/2`, `10/5/7/3`, `60` |
| Grandstream | `P64` and `P246`, a POSIX TZ string                                                                                                                                                                                     | `CET-1CEST,M3.5.0,M10.5.0/3`                           |
| Poly        | `tcpIpApp.sntp.gmtOffset` seconds, `daylightSavings.*` (month, date, time, dayOfWeek 1 = Sunday, `lastInMonth`)                                                                                                         | `3600`, March, last Sunday, 2                          |
| Fanvil      | `Time Zone` in quarter hours, `Time Zone Name`, `DST Type` and the rule fields (Sunday = 0)                                                                                                                             | `4`, `UTC+1`                                           |
| Snom        | `timezone`, a country code and the offset, for the zones in `snomCodes` (the country sets the daylight rules)                                                                                                           | `BEL+1`                                                |

Sources: Yealink administrator guide (Time and Date) and Nethesis
Tancredi `yealink.tmpl` (`summer_time`, `dst_time_type`, `start_time` and
`end_time` as `3/5/7/2`), Grandstream's Self-Defined Time Zone syntax in the
GXP/GRP/WP administration guides, Poly UC Software Administrator Guide
(`tcpIpApp.sntp`) with FusionPBX's polycom template, Tancredi
`fanvil-X5.tmpl` and `snom.tmpl` (`BEL+1`, `USA-5`). Yealink labels outside
the few in `yealinkNames` are not sent; the rules make the switch correct
without them. Unconfirmed on a phone: Fanvil's DST keys, Snom zones outside
the table, and Grandstream models that read only one of P64 and P246.

## Voicemail indicator (MWI)

Hello tells a phone about waiting voicemail with RFC 3842 `message-summary`.
A phone that sends `SUBSCRIBE` with `Event: message-summary` for its own
mailbox is answered `200` and gets an immediate `NOTIFY` with the counts
(`Messages-Waiting`, `Voice-Message`), then another on every new, heard or
deleted message. The subscription must come from a device of the mailbox's
own extension (anything else is `403`), `Expires` is held between 60 and
3600 seconds, and an event Hello does not serve gets `489 Bad Event`.
Contacts that hold no subscription get an unsolicited `NOTIFY` with the same
body instead, never both. Hello no longer sends MWI as a SIP `MESSAGE`.

A subscription lives on the node that accepted it. After a node restart or
failover the phone re-subscribes on its next refresh and gets the current
counts at once. Messages heard or deleted through the API reach the phone
at once: hello-control sends a PostgreSQL `NOTIFY hello_mwi` with the box id,
every SIP node listens, and the node holding the subscription sends the
`NOTIFY`. A node also re-reads subscribed mailboxes every minute and after
its listener reconnects, in case an event was lost. Watch
`hello_mwi_notify_total{kind,result}`.

The built-in templates provision the subscription and the voicemail number:

| Vendor      | Setting written                                                       | Status                                   |
| ----------- | --------------------------------------------------------------------- | ---------------------------------------- |
| Grandstream | `P99 = 1` (Subscribe for MWI), `P33` voicemail number (`*97` default) | Grandstream template keys (WP8x6, WP822) |
| Yealink     | `account.1.subscribe_mwi = 1`, `voice_mail.number.1`                  | verify on hardware                       |
| Poly        | `msg.mwi.1.subscribe`, `msg.mwi.1.callBack`                           | verify on hardware                       |
| Snom        | `user_mailbox` (`*97` default)                                        | key documented; unverified on hardware   |
| Fanvil      | `SIP1 MWINum` (`*97` default); no subscribe key written               | unverified on hardware                   |

Sources. Snom: Service Hub, D-series settings, `user_mailbox` (service.snom.com,
"Identities - Login Information: Mailbox"): the account name of the mailbox
tied to the SIP identity, needed when the MWI message does not carry the
mailbox URI. Snom documents no separate subscribe setting; the phone lights
its lamp from a subscription or from the unsolicited `NOTIFY` Hello sends to
contacts without one, and Hello accepts a `message-summary` `SUBSCRIBE` to the
voicemail code `*97` as the extension's own mailbox. Fanvil: `SIP1 MWINum` is
the voicemail number key already used by the template. Fanvil's subscribe-MWI
key could not be found in any public document, so Hello does not guess one;
Fanvil phones get MWI from the unsolicited `NOTIFY`. Neither vendor has been
tested on hardware.

## Remote resync and reboot (check-sync)

A phone can be told to re-fetch its configuration, or to reboot, without
touching it. Hello sends an out-of-dialog `NOTIFY` with `Event: check-sync`
to every registered contact of the phone's device, through the same path as
a call to it (the registration's `Path`, so the edge proxy reaches the
phone's NAT flow). Hello never sends `Event: reset`; a factory reset stays
on the phone, and the Grandstream template does not enable "Allow SIP Reset".

| Vendor      | Resync                    | Reboot                   | Status                                            |
| ----------- | ------------------------- | ------------------------ | ------------------------------------------------- |
| Yealink     | `check-sync;reboot=false` | `check-sync;reboot=true` | verify on hardware                                |
| Snom        | `check-sync;reboot=false` | `check-sync;reboot=true` | verify on hardware                                |
| Poly        | `check-sync`              | `check-sync;reboot=true` | verify on hardware                                |
| Grandstream | `check-sync`              | `check-sync;reboot=true` | WP8x6 documented; the reboot form is to verify    |
| Fanvil      | `check-sync`              | `check-sync;reboot=true` | `resync` is the other documented event; to verify |
| Generic     | `check-sync`              | `check-sync;reboot=true` |                                                   |

Grandstream phones challenge the `NOTIFY` with digest when `P4428 = 1`
(NOTIFY authentication), which the template writes; Hello answers the
challenge with the device's stored digest hash, preferring SHA-256, only in
the device's own realm (a challenge in any other realm is not answered and the
push reports `auth_failed`). The template also writes `P95030 = 0`, so a plain
check-sync re-provisions without a reboot (WP8x6, firmware 1.0.3.20 and
later).

**Console.** Phone page: Resync and Reboot (reboot asks for confirmation) and
the last pushes with their result. Phones page: a resync and a reboot button
per row, "Resync all" and "Reboot all", and the newest result per phone.
Phone groups page: resync and reboot for every phone in a group. Operators
and admins can push; viewers cannot.

**API.** `POST /api/v1/phones/{id}/resync` and `.../reboot` (202, the queued
push), `POST /api/v1/phone-groups/{id}/resync|reboot` and
`POST /api/v1/phones/resync|reboot` (202, `{queued, skipped}`; phones with no
device or disabled are skipped), `GET /api/v1/phones/{id}/pushes` and
`GET /api/v1/phone-pushes` (the newest per phone). A phone with no enabled
device answers `409`. Every request is audited (`resync` or `reboot` on the
phone, the group, or `all`).

**How it runs.** The request is a row in `phone_pushes`, and hello-control
sends PostgreSQL `NOTIFY hello_checksync`. Any hello-sip node claims it (a
row is claimed once), at most `HELLO_PROV_PUSH_RATE` claims per minute across
the cluster (default 20), so a push to 500 phones takes 25 minutes at the
default; raise the rate for big sites. A claim that stays "sending" for two
minutes (its node died) is written off as `failed`.

**Results.**

| Result           | Meaning                                                                                         |
| ---------------- | ----------------------------------------------------------------------------------------------- |
| `pending`        | Queued, waiting for a node or for the rate                                                      |
| `sending`        | Claimed; the NOTIFY is out                                                                      |
| `ok`             | The phone answered 2xx. The phone's next fetch in the fetch log confirms it                     |
| `not_registered` | No registration, or the phone answered `481` or `404`; the change waits for the periodic resync |
| `timeout`        | No answer ("push not acknowledged"); the change lands at the periodic resync                    |
| `auth_failed`    | The phone's challenge was in an unknown realm, or it rejected the credentials                   |
| `failed`         | Any other answer or a send error                                                                |

A phone registered from two contacts gets the NOTIFY on both, and the result
is the best of them (`ok`, then `auth_failed`, `timeout`, `failed`,
`not_registered`). Watch `hello_checksync_total{event,result}`.

**Automatic resync.** With `HELLO_PROV_AUTO_RESYNC=true` (default off)
hello-control fingerprints what each bound phone's configuration renders from
(the account, server, BLF keys, address book assignment, firmware pin, time
settings and the template that resolves for it) every 30 seconds. A change
that is the same at two scans in a row queues a resync for that phone, so a
burst of edits pushes once; pushes still go out at `HELLO_PROV_PUSH_RATE`. A
phone seen for the first time is only recorded. Automatic pushes show as
"(automatic)" and are audited as `auto_resync` by `system`.

## Redirect services

A vendor redirect service sends a factory-new phone to Hello from anywhere
on the internet. With credentials set, adding, rotating or deleting a phone
registers, re-registers or unregisters its MAC with its per-device URL, in
the background with retries; the inventory shows the status (`pending`,
`registered`, `failed: …`).

| Vendor             | Support                                                                                                                    |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| Snom SRAPS         | registers the per-device URL (access key ID and secret)                                                                    |
| Yealink RPS / YMCS | registers the per-device URL (RPS access key and secret; YMCS OAuth2 client, which also needs the phone's serial number)   |
| Grandstream GDMS   | adds the device (MAC and serial number) to your GDMS site; set the site's provisioning server to the boot URL once in GDMS |
| Poly, Fanvil       | manual: paste the per-device URL into the vendor's portal (Poly ZT, Fanvil FDPS); neither has a public API                 |

Enter credentials under **Phones → Redirect services** (write-only, sealed
with `HELLO_SECRET_KEY`, checked on save), or set them in the deployment,
which takes precedence and shows as read-only. On kw that is the optional
`hello-prov-redirect` secret (deploy/kuvryn-sync/README.md, Secrets), mapped
to `HELLO_PROV_SNOM_*`, `HELLO_PROV_YEALINK_*`, `HELLO_PROV_YMCS_*` and
`HELLO_PROV_GDMS_*`. Without credentials nothing is called and DHCP and
manual entry work as before.

## Write a generic template

Built-in templates cover the five vendors above; **Copy to edit** makes an
editable copy that wins over the built-in. For any other phone, add a
`generic` template (**Phones → Templates**): a model glob (`*`, `GXP21??`),
a priority, and one or more files, each a file-name pattern (`{mac}`,
`{MAC}`, `{model}`) and a Go `text/template` body in the phone's format.

Templates see only these values:

| Value       | Fields                                                                                                                            |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `.Phone`    | `MAC`, `MACUpper`, `Model`, `Label`, `AdminPassword`                                                                              |
| `.Line`     | `Username`, `AuthName`, `Password`, `DisplayName`, `Label`, `Domain`, `VoicemailCode`                                             |
| `.Server`   | `Host`, `Port`, `Transport` (`udp`), `Expiry` (seconds)                                                                           |
| `.BLF`      | a list of `Number`, `Label`, `URI`                                                                                                |
| `.Prov`     | `URL` (this phone's), `CAURL`, `CACertPEM`, `ResyncSeconds`                                                                       |
| `.Firmware` | `URL` (the file), `Dir` (its directory, no trailing slash), `Version`, `Via` (1 HTTP, 2 HTTPS), or nil when no firmware is pinned |
| `.Time`     | `Zone`, `NTP`, `TZ` (the zone per vendor, see [Time zones](#time-zones))                                                          |

Functions: `xml` (escape), `upper`, `lower`, `default`, `add`, `mul`, `div`,
`urlhost` (`https://h:8/p/x/` gives `h:8`) and `urlpath` (the same gives
`p/x/`, no leading slash), for a phone that takes the server and the path in
separate settings; `noscheme` (`https://h:8/p/x/` gives `h:8/p/x`) and
`urlvia` (`2` for HTTPS, else `1`), for Grandstream's scheme-less server
paths. For example, a key/value phone:

```text
sip.server = {{.Server.Host}}:{{.Server.Port}}
sip.user = {{.Line.Username}}
sip.password = {{.Line.Password}}
sip.display_name = {{.Line.DisplayName}}
{{- range $i, $k := .BLF}}
key.{{add $i 1}}.blf = {{$k.Number}}
{{- end}}
provisioning.url = {{.Prov.URL}}
provisioning.interval = {{.Prov.ResyncSeconds}}
```

Saving validates the template: it must parse, use only these values, and
render a sample phone within 100 ms and 256 KiB; errors name the file and
line. **Preview** on a phone shows exactly what it would get, with the
secrets and the token masked.

## Phone emulator: adding a URL-bearing setting

Every setting that carries a URL, path, server, file name or scheme code is a
chance for a phone to build a request Hello does not serve. Grandstream's
P192 is a firmware _directory_ the phone appends its own file name to, so a
full file URL made it ask for `fw/wp810fw.bin/wp810fw.bin`; a trailing slash
made another phone ask for `/p/boot//cfg<mac>.xml`. Both rendered valid files.

`internal/prov/phone_emulator_test.go` guards this. For each vendor it holds a
small model of how the phone derives requests from its rendered config (the
base, the file names it appends, the scheme setting, the enum codes, per the
vendor's documentation, cited in the comments). `TestPhoneEmulator` boots a
phone of each vendor through the real handler: the DHCP boot path, the
hand-off, the per-device files, the pinned firmware and the CA, with and
without a pinned firmware, and asserts every URL the phone derives answers
`200` with the right content. `TestPhoneEmulatorCatchesKnownBugs` proves it
fails on the old Grandstream firmware setting and similar, on a scheme in a
Grandstream server path, an IANA time zone name, and a Poly boot server option
that is not `Static`.

When you add or change a built-in setting that carries a URL, path, server, file
name or scheme code (a phonebook URL, a ringtone, a wallpaper, a language file,
a syslog server):

1. Put it in the vendor's template, with the vendor document it comes from.
2. Add what the phone does with it to that vendor's `emu<Vendor>` function:
   fetch the URL it builds with `must`, `get` or `firmware`, and check the body.
   Say in the comment whether the phone appends a file name, a prefix or a
   suffix, and whether the value takes a scheme.
3. If its name does not match the vendor's pattern in `urlKey`, extend the
   pattern. A flagged setting that no step takes fails the test with
   "does not model", so a new one cannot ship unchecked.
4. Add a file name the phone requests to `TestPhoneEmulatorFileNames`.

Mark what no document confirms as "verify" in the comment and in this
chapter's vendor notes; do not guess.

## Token rotation and "token exposed"

The token in the per-device URL is the phone's credential.

- **Rotate token** (`POST /api/v1/phones/{id}/rotate-token`) issues a new
  URL. By default the old token keeps working until the phone first fetches
  with the new one, or for `HELLO_PROV_TOKEN_GRACE` (7 days), so a phone in
  service is not cut off; with `?immediate=true` the old token stops at
  once. A phone set up by redirect is re-registered with the new URL; a
  phone set up by hand needs the new URL typed in.
- **Token exposed**: a per-device request over plain HTTP is refused and
  flags the phone, because its token crossed the network in clear. Rotate
  it (immediately) and fix the phone's URL to `https://`. The one plain-HTTP
  hand-off of trust on first use does not set this flag.

## Address books

Hello keeps the contact data that phones show as a directory, and serves it
to each phone in its vendor's remote phonebook XML over the provisioning
endpoint, with the phone's own token. Manage it under **Phonebooks** and
**Phones > Groups** in the console, or through the API below.

- **Books.** The **company** book and the **extensions** book exist once.
  The extensions book is computed from the extensions (name and number) and
  is read-only; hide an extension from it with the switch on the Phonebooks
  page or `directoryHidden` on the extension
  (`PATCH /api/v1/extensions/{id}`). **Named** books are created by an admin
  (at most 100). An entry has a first name, last name, company (any one is
  enough), one to three numbers labelled `work`, `mobile`, `home` or
  `other`, and a note; a book holds at most 5,000 entries.
- **Who can do what.** Viewers read books, entries and groups. Operators
  also change the entries of the company and named books and import and
  export CSV. Admins also create, rename and delete books, and manage phone
  groups.
- **Phone groups** decide which books a phone is delivered: the extensions
  and company books (on by default) and up to five named books, in order. A
  phone with no group gets the extensions and company books. Put a phone in
  a group in its form (**Phone group**), with `groupId` on
  `POST /api/v1/phones` or `PATCH /api/v1/phones/{id}`.
- **Link a user to an extension** with `extensionId` on
  `PATCH /api/v1/users/{id}` (admin; one user per extension). It is what
  ties a user's personal contacts to the phones of that extension (see
  _Personal contacts_ below).
- **Check a number.** `GET /api/v1/phonebooks/lookup?number=&callee=`
  (admin) says which entry would name a caller: the number is reduced to
  its lookup key (`+41 79 123 45 67`, `0041791234567`, `079 123 45 67` and
  `sip:+41791234567@host` are one key) and looked up in the extensions
  book, the named books of the groups of the callee's phones (`callee` is
  an extension number; leave it out to skip them) and the company book, in
  that order. An exact match on the key wins; failing that, the last nine
  digits match when exactly one entry has them (`ambiguous` names nobody).
  Anonymous, feature codes (`*72`), short codes and non-numbers are
  `unresolvable`. Personal books are never read. How `+` and a leading `0`
  are read depends on `HELLO_DIAL_INTL_PREFIX` (default `00`, the digits a
  dialled international number starts with) and `HELLO_DIAL_COUNTRY_CODE`
  (default unset; `41` or `+41` makes `079 123 45 67` the same number as
  `+41 79 123 45 67`). A number is stored with its key when it is saved, so
  after changing either, re-import or re-save the entries that matter.
- **CSV.** On the Phonebooks page, **Import CSV** checks the file first
  (every failing row with its row, column and reason) and imports only a
  clean file; **Export CSV** downloads the book. Over the API,
  `POST /api/v1/phonebooks/{id}/import` takes a multipart `file` with the
  header `first_name,last_name,company,work,mobile,home,other,note` (UTF-8
  with or without a byte order mark, comma or semicolon, up to 2 MiB and
  5,000 rows). `dry_run=true` reports every failing row and writes nothing;
  an import is all or nothing, and `mode=replace` swaps the book's content
  instead of appending. `GET /api/v1/phonebooks/{id}/export` writes the same
  format; a cell starting with `=`, `+`, `-` or `@` gets a leading
  apostrophe so a spreadsheet does not evaluate it, and the import removes
  it again.

### How phones get the directory

A phone's directory is the union of its books in order (extensions,
company, the group's named books); a number already in an earlier book is
dropped from a later one. The built-in templates point each phone at it, so
a phone that already provisions from Hello picks it up on its next
configuration fetch; nothing is typed into the phone.

The directory is `GET https://<provisioning host>/p/<token>/pb/<n>.xml`
(slot `0` is every book merged; slots `1` to `5` are the books separately,
for Yealink). It follows the same rules as the other per-device files: the
token, HTTPS, the phone and its device enabled, the rate limit per source
address, an `ETag` (a `304` on `If-None-Match`) and an empty `404` for any
denial, and the fetch shows in the phone's fetch log with the kind
`phonebook`. The slot is mapped to books through the token's phone, so a
phone cannot ask for another group's book. The output is sorted by last
name, first name and number, so it is byte-identical for the same data.
Text is XML-escaped, control characters are dropped and a name is cut to
the vendor's limit.

A phone with no books is given no directory settings. A directory above
2 MiB is not served (an empty `404`, with a log line), so the phone keeps
what it has.

**Caching.** Each rendered directory is kept in Valkey under
`hello:pb:render:<vendor>:<hash>` for an hour (five minutes when it holds a
personal book, and then under a key of that phone alone), so every replica
answers a repeat fetch without reading the books. The hash covers the vendor,
the slot and the _version_ of the phone's delivered set: each book carries a
version that every entry, import or rename of it bumps in the same
transaction, the set names its books in order (so a phone group change is a
new set), and the extensions book is fingerprinted by how many extensions
there are, how many are hidden and the latest change. A change anywhere
therefore names a new key and the old render is simply never asked for
again; nothing is invalidated by hand, and a Valkey flush or outage costs
a render from PostgreSQL, never a stale directory.

### Personal contacts

Any signed-in user whose account is linked to an extension keeps their own
contacts on **My contacts** (`/me/contacts`): add, edit, delete, CSV import
and export, up to 1,000. The personal book is the last book of the
directory of the phones bound to that user's extension, after the
extensions, company and group books (a number an earlier book has is not
repeated), and shows at the phone's next refresh. Over the API:
`GET|POST /api/v1/me/contacts`, `PUT|DELETE /api/v1/me/contacts/{id}`,
`POST /api/v1/me/contacts/import` and `GET /api/v1/me/contacts/export`,
with the same body and CSV format as a shared book.

- The owner is always the caller. No route takes a user id, and no route
  reads another user's contacts, an administrator's included; the book does
  not appear in the book list, the phonebook routes or the lookup check, and
  the MCP tools do not cover it.
- A service account has no contacts (`403`, code `no_user`). A user with no
  linked extension gets `403` with code `no_extension`, and the page tells
  them to ask an administrator.
- Unlinking the user (or deleting them) stops the delivery at the phone's
  next fetch; the contacts are kept for a later relink, and deleted with the
  user.
- The audit log records that a user created, changed, deleted or imported
  contacts, with the user and the number of imported rows, never a contact.
- A viewer's API token can only read, so a viewer changes contacts from the
  console (a session); an operator's token can write.

|                  | Yealink                                                                                                                                               | Poly                                                                      | Grandstream                                                              | Snom                             | Fanvil                                                                        |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- | ------------------------------------------------------------------------ | -------------------------------- | ----------------------------------------------------------------------------- |
| Format           | `YealinkIPPhoneDirectory`                                                                                                                             | `directory` / `item_list`                                                 | `AddressBook`                                                            | `SnomIPPhoneDirectory`           | `IPPhoneDirectory`                                                            |
| Settings written | `features.remote_phonebook.enable = 1`, `remote_phonebook.data.N.url` / `.name` (N 1 to 5; unused ones empty), `features.remote_phonebook.flash_time` | `dir.local.readonly = 1`; the phone requests `<mac>-directory.xml` itself | `P330 = 3` (HTTPS), `P331` (host and path), `P332` (minutes), `P333 = 1` | `dkey_directory = url <URL>`     | `<PHONEBOOK CONFIG MODULE>` with `Remote Phonebook1 URL`, `Name` and `Update` |
| Refresh          | `flash_time` (seconds)                                                                                                                                | at boot and at the provisioning re-check                                  | `P332`                                                                   | when the directory key is opened | `Update` (minutes)                                                            |
| Sources          | up to 5 (a sixth book and later are merged into the fifth)                                                                                            | one merged file                                                           | one merged file                                                          | one merged file                  | one merged file                                                               |
| Name limit       | 99                                                                                                                                                    | 40                                                                        | 64                                                                       | 64                               | 64                                                                            |
| Capacity         |                                                                                                                                                       |                                                                           | 1,000 entries (WP8x6)                                                    |                                  |                                                                               |

Where a vendor holds one number per entry (Poly, Snom, Fanvil), an entry
with several numbers becomes one record per number; Snom and Fanvil add the
label to the name (`Tom Smith (mobile)`). Past 1,000 entries a Grandstream
directory keeps the first 1,000 of the delivery order (extensions, company,
group books), so earlier books win, and logs it.

Grandstream WP810 and WP822 firmware (1.0.11.x) treats `P331` as a
directory and appends `/phonebook.xml`, requesting
`/p/<token>/pb/0.xml/phonebook.xml`; the WP836 uses it as the whole file
URL. The endpoint serves the same book for `pb/<n>.xml`,
`pb/<n>.xml/phonebook.xml` and `pb/<n>/phonebook.xml`, so one `P331` value
works for both.

**Not yet confirmed on a phone.** The keys come from the vendor guides
(Yealink Auto Provisioning Guide, "remote phonebook"; Poly UC Software
Administrator Guide, "contact directory"; Grandstream GXP/GRP/WP phonebook
XML and provisioning guides, P330 to P333; snom wiki, "XML directory" and
`dkey_directory`; Fanvil auto-provisioning guide, remote phonebook) and have
not been checked on real phones yet. Treat these as **verify**: the Poly
reload behaviour after a change, the Yealink `flash_time` range (the phone
may only accept a minimum of an hour and ignore a shorter interval), the
Grandstream P-values (P331 without the `https://` scheme, P333), the Snom
`dkey_directory` setting, and the whole Fanvil section name and keys. If a
phone shows no directory, look at its fetch log: no `phonebook` request means
the key is wrong for that model; correct it in a copy of the built-in
template (the directory itself does not change). `HELLO_PROV_BOOK_REFRESH`
(default `15m`, at least `1m`) sets the refresh interval where the phone
has such a setting.

## Paging and intercom

Hello can page a group of phones, or call one phone, so that the phones
answer by themselves on the speaker.

- **Paging groups.** An admin defines a group (Paging groups in the
  console, or `/api/v1/paging-groups`): a name, a page code such as `*60`,
  its member extensions (up to 200), the extensions allowed to page (empty
  means any internal extension) and a priority. Dial the code from any
  phone registered on Hello. Hello forks the call to every registered phone
  of every member with `Call-Info: <sip:host>;answer-after=0` and an
  `Alert-Info` auto-answer value, and the phones answer on the speaker.
  Audio is one way, from the caller to the members: whatever a member sends
  back is dropped. Until the first member answers, the caller hears Hello's
  page tone (two short beeps, repeated) as early media instead of ringback;
  it stops when the page connects. When the caller hangs up, every leg
  ends. A page also
  ends after `HELLO_PAGE_MAX_SECONDS` (120), and the caller hears busy when
  no member answers within 10 seconds.
- **Who is skipped.** Members on DND, members in a call (a page never
  interrupts a call), members that are not registered, and the caller's own
  extension. An `emergency` group ignores DND but still never interrupts a
  call, and preempts a running `normal` page of the same group; otherwise a
  second caller on a group that is being paged hears busy. At most
  `HELLO_PAGE_FANOUT` (200) legs per page and `HELLO_PAGE_CONCURRENT` (5)
  pages per node; beyond that the caller hears busy.
- **Intercom.** The feature code `*80` followed by an extension number
  (`*80200`) calls that extension's phones with the same headers and
  two-way audio. It respects DND, never answers a phone that is in a call
  (that phone rings normally), and does not follow forwarding. An extension
  with `allowIntercom` off rings normally; set it with the Allow intercom
  switch on the extension's page or `PATCH /api/v1/extensions/{id}`.
- **Testing a group.** The Test button on a paging group (or
  `POST /api/v1/paging-groups/{id}/test`, admin only, body
  `{"extension": "200"}`, default the first member) pages that one member
  as a real page would, plays the tone for three seconds and hangs up. The
  result lists each registered contact, what it did and the auto-answer
  headers sent, which is how to check a phone's setting without a second
  person. A member on DND (in a normal group) or in a call is reported, not
  rung. A test needs Valkey and a running `hello-sip` node, and answers
  `504` when none takes it within 20 seconds. It writes no CDR.
- **CDRs.** A page or intercom call has `kind` `page` or `intercom`. A page
  also has one row per member (table `cdr_page_legs`) with the member's
  extension, the contact's device and its result (`answered`, `busy`, `dnd`,
  `no_answer`, `no_registration`), returned as `pageLegs` by
  `GET /api/v1/cdrs/{id}` and listed on the call's detail page; the call
  trace repeats them.
- **Security.** Auto-answer turns a phone into a listening device, so Hello
  adds these headers only on the legs it creates for a page or an intercom
  call. `Alert-Info`, `Call-Info` and `Answer-After` headers in anything a
  phone or trunk sends are never forwarded, forwarded and transferred calls
  never carry them, and a trunk dialling a page or intercom code gets `403`.
  Provisioned phones accept auto-answer requests from any source: the
  vendors offer no restriction to the server.
- **Provisioning.** The built-in templates enable what each vendor
  documents. Everything below is from the vendor's own documentation and is
  **not yet verified on hardware** (the lab test marks each as "needs
  hardware"):

  | Vendor      | Key written                                                                                                                                                                                   | Source                                                                                                                                                                                                                  |
  | ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
  | Grandstream | `P298 = 1` Allow Auto Answer by Call-Info/Alert-Info (the plain Auto Answer, P90, stays off); WP8x6 only: `P26072 = 1` warning beep before an auto answer, `P26071 = 0` do not mute on answer | Grandstream config templates `wp810_822_825_config_1.0.11.83` and `wp8x6_config_1.0.3.39` (config-template_09302026); WP8x6 Administration Guide, "Call Settings". Barge by header (`P26019`) stays at its default, off |
  | Yealink     | `features.intercom.allow = 1`, `features.intercom.tone = 1`, `features.intercom.mute = 0`, `features.intercom.barge = 0`                                                                      | Yealink SIP IP Phones Administrator Guide, "Intercom"; the `Alert-Info: Ring Answer` value is still to be confirmed                                                                                                     |
  | Poly        | `voIpProt.SIP.alertInfo.1.value = "Auto Answer"`, class `ringAutoAnswer`                                                                                                                      | Poly UC Software Administrator Guide, `voIpProt.SIP.alertInfo`                                                                                                                                                          |
  | Snom        | `answer_after_policy = idle`: answer an intercom call by itself only on an idle phone; Hello sends `Alert-Info: <http://www.notused.com>;info=alert-autoanswer;delay=0`                       | Snom Service Hub, "Intercom - Paging feature" (the header form) and `answer_after_policy` (off, idle, always, not_busy; D series); the PBX must pass the header unaltered                                               |
  | Fanvil      | none: enable Intercom/auto answer on the phone                                                                                                                                                | Fanvil's published guides give no provisioning key for it that could be confirmed; Hello sends `Call-Info: answer-after=0` and `Alert-Info: info=Auto Answer`, unverified                                               |

  The Grandstream speaker volume for pages has no P-value in these
  templates (only the PTT emergency channel volume, `P22181`); the warning
  beep is the setting above.

- **Troubleshooting.** A phone that ignores the headers rings as an
  ordinary call and the CDR shows `no_answer` for it: check the key above on
  that phone. Hello decides the `Alert-Info` value from the phone's
  `User-Agent` at registration (`auto-answer` for Grandstream and unknown
  phones).

### Multicast paging and push-to-talk (phone groups)

A phone group can have **multicast paging channels** (`PUT
/api/v1/phone-groups/{id}/multicast`, or the group's dialog under Phones,
Groups): a label, an IPv4 multicast address and port, a codec (PCMU, PCMA or
G.722), a role (listen, send or both) and a priority (1 to 25), at most ten,
no listen address twice, and none in `224.0.0.0/24`. Every phone of the group
is provisioned to listen on the channels that listen and to send to those
that send. The audio is the phones' own: Hello is not in the media path, so
it works only on a LAN where multicast is delivered (IGMP snooping and the
router must pass it), it does not cross subnets, and multicast has no
authentication, so anyone on the LAN can send to the group. For paging across
subnets use a server paging group.

| Vendor       | Keys written                                                                                                                                                                                | Source                                                                                                                     |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| Grandstream  | `P22208 = 1` multicast paging, `P1568` codec (0 PCMU, 8 PCMA, 9 G.722; the first channel's), `P1569` onward address:port and label pairs in priority order, ten slots (unused ones cleared) | config templates `wp8x6_config_1.0.3.39`, `wp810_822_825_config_1.0.11.83`; WP8x6 Administration Guide, "Multicast Paging" |
| Yealink      | `multicast.codec`, `multicast.receive_priority.enable = 1`, `multicast.listen_address.N.ip_address` and `.label`, `multicast.paging_address.N.ip_address`, `.label`, `.channel`             | Yealink Administrator Guide, "Multicast Paging"; unverified                                                                |
| Poly         | `ptt.pageMode.enable`, `.address` and `.port` of the first listening channel, `.codec`, `ptt.pageMode.group.N.label` and `.subscribed`                                                      | Poly UC Software Administrator Guide, "Group Paging"; unverified                                                           |
| Snom, Fanvil | none                                                                                                                                                                                        | no provisioning key found                                                                                                  |

**Grandstream paging button.** The WP822 and the WP8x6 report "not configured
on the phone" when you press the PTT/paging button until multicast paging has
a listening address (the button sends to it). The WP8x6 side key also needs
the PTT/Paging feature on: Hello writes `P22556 = 1` and the side key
function `P82317 = 0` (PTT/Paging). A WP822 has no PTT channel settings in
its template and gets the multicast keys only.

**Push-to-talk (WP8x6 only)** (`PUT /api/v1/phone-groups/{id}/ptt`): when on,
Hello writes `P22177 = 1`, the PTT/group paging address `P22183` (default
`224.0.1.116:5001`, one address for every channel), the default channel
`P22184` (1, 2, 24 or 25, and it must transmit), accept while busy `P22179`,
the codec `P22180` (PCMU or G.722) and, for each channel 1 to 25 at
`P82001 + 5 (n - 1)`, Available, Transmit, Subscribe, Join and Name (channels
not defined are switched off). Pressing the button talks on the default
channel, and every phone of the group that subscribes to it hears it.
Source: the WP8x6 config template and Administration Guide, "PTT/Paging";
unverified on a phone.

- **Multicast from a paging group.** A paging group can name one listening
  G.711 channel of a phone group (the paging group's "Also page a multicast
  channel", `multicastChannelId`). Dialling its code then also sends the
  caller's audio to the channel's address, converted to the channel's G.711
  law, so the phones that listen on the channel hear the page with the codec
  they are set to. A group with a channel may have no members: the caller is
  connected at once (multicast has no answer to wait for) and hears no tone.
  The packets leave `hello-sip` on the interface `HELLO_PAGE_MULTICAST_IFACE`
  (default the system's route) with `HELLO_PAGE_MULTICAST_TTL` (1) hops, so
  that node must be on the phones' LAN: in Kubernetes, host networking or a
  secondary interface on that VLAN.

## Call waiting and busy handling

Each extension has a **Call waiting** switch (`callWaiting`, on by default;
the extension's page in the console or `PATCH /api/v1/extensions/{id}`).

- **On.** A call to an extension that is in a call still rings its phones,
  and the phone shows its own call-waiting indication and tone.
- **Off.** A call to an extension that is in a call does not ring: the caller
  gets the extension's forward-busy target when one is set, otherwise the
  extension's voicemail when it has one, otherwise `486 Busy Here`. An
  extension that is not busy rings as usual.
- **Ring groups and hunt groups** skip a member that is in a call and has
  call waiting off, as they skip a DND member; a member with call waiting on
  is still rung. A group left with no member follows its failure destination.
- **Paging and intercom.** Paging already skips every member in a call (a
  page never interrupts a call), whatever the switch says. An intercom call
  to an extension in a call rings it as a normal call instead of
  auto-answering, whatever the switch says; the call-waiting flag is not
  consulted on that path. Call parking and pickup do not exist in Hello yet;
  when they do, they must not consult the flag either (a pickup answers a
  call that is already ringing).
- **How busy is decided.** An extension is busy while any connected call of
  the cluster has it as caller, as the dialled number, or as the extension
  whose phone answered (a ring group member, a forward target). Every node
  publishes its calls to Valkey with a heartbeat and a TTL (the same live
  call record the dashboard and paging use), so the state is cluster-wide,
  is released when the call ends (BYE, CANCEL, failure, takeover) and
  expires by itself when the owning node dies. Nothing extra is counted. If
  Valkey is unreachable the extension is treated as idle and rings.
- **Feature codes.** `*71` turns call waiting on and `*70` turns it off for
  the extension of the phone that dials it, like `*78` and `*79` do for DND
  (also mid-call as DTMF). The defaults are added by migration 00026 to
  existing installs unless the code is already used; change them under
  System, Feature codes.
- **Phone settings.** The built-in templates write the phone's own switch
  and tone from the flag, so the phone and Hello agree. **A change reaches a
  phone at its next resync** (use Resync on the Phones page, or wait for the
  resync interval); a toggle by feature code does the same. All keys are
  from the vendor's documentation or published configuration templates and
  **none is confirmed on hardware yet** (lab item 26):

  | Vendor      | Keys written (on / off)                                                                                                                                                                                               | Source                                                                                                                                                                                                                                       |
  | ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
  | Grandstream | `P26063` account 1 call waiting: 1 / 2 (WP856: 2 / 1, its template reads it the other way round); `P91` phone-wide: 0 / 1; `P186` call-waiting tone: 0 / 1 (WP8x6; the WP822 template has no such key and ignores it) | `wp8x6_config_1.0.3.39` ("Enable Call Waiting", 0 Default, 1 Yes, 2 No) and `wp810_822_825_config_1.0.11.83` ("Disable Call Waiting", 0 Default, 1 No, 2 Yes) in config-template_09302026: the wording is inverted, the values mean the same |
  | Yealink     | `call_waiting.enable` 1 / 0, `call_waiting.tone` 1 / 0                                                                                                                                                                | Yealink SIP IP Phones Administrator Guide, "Call Waiting"; the same keys in third-party provisioning templates                                                                                                                               |
  | Snom        | `call_waiting idx=1` `on` / `off` (`visual` would show it without a tone)                                                                                                                                             | Snom settings reference and the Tancredi snom template                                                                                                                                                                                       |
  | Poly        | `call.callWaiting.enable` 1 / 0, `call.callWaiting.ring` `beep`                                                                                                                                                       | Poly UC Software Administrator Guide; provisioning templates for VVX                                                                                                                                                                         |
  | Fanvil      | `P1 CallWaiting` 1 / 0 and `P1 CWaiting Tone` 1 / 0 in `<CALL FEATURE MODULE>`                                                                                                                                        | Fanvil X-series exported configuration (a real phone export; the XML form is `CallWaiting` and `CWaitingTone`)                                                                                                                               |

  The WP822 and WP836 call-waiting tone is the phone's own
  (`P347` and its gain `P1555`, left at their defaults); the lab check
  listens for it with the flag on and expects silence with it off.

## Security model and its limits

What protects the phones' secrets:

- Per-device files are served only over HTTPS, only for the token's phone,
  only when the phone and its device are enabled, and only for a file name
  carrying that phone's MAC (if any). Every denial is an empty `404`.
- Requests are rate-limited per source IP (60 a minute; 10 denials in 10
  minutes block the IP for 10 minutes) and per phone (30 files an hour),
  cluster-wide.
- Every request is in the fetch log with the token redacted (`/p/****/…`);
  the ingress access log is off. No secret, token or redirect credential is
  ever logged.
- The boot path never serves a SIP secret or an admin password.

What it does not protect against:

- **Trust on first use trusts the first asker.** Whoever on the phone
  network first requests an armed phone's per-MAC boot file, with its MAC,
  gets the token. Limit it with `HELLO_PROV_BOOT_CIDRS`, add phones shortly
  before plugging them in, and treat **boot reclaimed** on a phone you did
  not reset as an incident: re-arm it, which rotates the token.
- **The token crosses plain HTTP once** in that hand-off, and the CA is
  fetched over plain HTTP. Someone who can intercept the phone network then
  can take the token or substitute a CA; compare the CA fingerprint when you
  install it by hand.
- **The token is a bearer credential.** Anyone holding the URL gets the
  phone's SIP secret and admin password. Show it only once, rotate it when
  it may have leaked, and rotate immediately when it is flagged exposed.
- The phone's own storage holds its secret in clear, as with any SIP phone.
