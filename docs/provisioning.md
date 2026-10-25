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

### Grandstream

GXP, GRP, GXV and WP. The phone tries `cfg<mac>`, `cfg<mac>.bin` (both
`404`) and then `cfg<mac>.xml`, the P-value file with the account.

### Snom

D series. Booted, the phone asks for `snom<model>.htm` and
`snom<model>-<MAC>.htm`; Hello sets its setting server to the per-device URL
with `{mac}`, which the phone fills in with its MAC on every re-check.

### Fanvil

X series. The model's common file (`F0V00X5U0000.cfg` for the X5U) and
`<mac>.cfg` with the account.

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
within about ten seconds (the node re-reads subscribed mailboxes). Watch
`hello_mwi_notify_total{kind,result}`.

The built-in templates provision the subscription and the voicemail number:

| Vendor      | Setting written                                                           | Status                                   |
| ----------- | ------------------------------------------------------------------------- | ---------------------------------------- |
| Grandstream | `P99 = 1` (Subscribe for MWI), `P33` voicemail number (`*97` default)     | Grandstream template keys (WP8x6, WP822) |
| Yealink     | `account.1.subscribe_mwi = 1`, `voice_mail.number.1`                      | verify on hardware                       |
| Poly        | `msg.mwi.1.subscribe`, `msg.mwi.1.callBack`                               | verify on hardware                       |
| Snom        | `user_mailbox` (the phone subscribes to it)                               | verify on hardware                       |
| Fanvil      | `SIP1 MWINum`; no subscribe key confirmed, unsolicited `NOTIFY` covers it | verify on hardware                       |

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

| Value       | Fields                                                                                |
| ----------- | ------------------------------------------------------------------------------------- |
| `.Phone`    | `MAC`, `MACUpper`, `Model`, `Label`, `AdminPassword`                                  |
| `.Line`     | `Username`, `AuthName`, `Password`, `DisplayName`, `Label`, `Domain`, `VoicemailCode` |
| `.Server`   | `Host`, `Port`, `Transport` (`udp`), `Expiry` (seconds)                               |
| `.BLF`      | a list of `Number`, `Label`, `URI`                                                    |
| `.Prov`     | `URL` (this phone's), `CAURL`, `CACertPEM`, `ResyncSeconds`                           |
| `.Firmware` | `URL`, `Version`, or nil when no firmware is pinned                                   |
| `.Time`     | `Zone`, `NTP`                                                                         |

Functions: `xml` (escape), `upper`, `lower`, `default`, `add`, `div`. For
example, a key/value phone:

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
  will tie a user's personal contacts to the phones of that extension;
  personal books are not delivered yet.
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
  back is dropped. When the caller hangs up, every leg ends. A page also
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
  with `allowIntercom` off (`PATCH /api/v1/extensions/{id}`) rings normally.
- **CDRs.** A page or intercom call has `kind` `page` or `intercom`; the
  call trace lists each page member's result (`answered`, `busy`, `dnd`,
  `no_answer`, `no_registration`).
- **Security.** Auto-answer turns a phone into a listening device, so Hello
  adds these headers only on the legs it creates for a page or an intercom
  call. `Alert-Info`, `Call-Info` and `Answer-After` headers in anything a
  phone or trunk sends are never forwarded, forwarded and transferred calls
  never carry them, and a trunk dialling a page or intercom code gets `403`.
  Provisioned phones accept auto-answer requests from any source: the
  vendors offer no restriction to the server.
- **Provisioning.** The built-in templates enable what each vendor
  documents:

  | Vendor       | Key written                                                                                           | Status                                                                                                                    |
  | ------------ | ----------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
  | Grandstream  | `P298 = 1`, Allow Auto Answer by Call-Info/Alert-Info (the plain account Auto Answer, P90, stays off) | the setting is in the WP8xx Administration Guide, the P-value is from GXP/GRP templates and is to be confirmed on a WP822 |
  | Yealink      | `features.intercom.allow = 1`, `features.intercom.tone = 1`, `features.intercom.mute = 0`             | Administrator Guide, "Intercom"; Alert-Info value to be confirmed                                                         |
  | Poly         | `voIpProt.SIP.alertInfo.1.value = "Auto Answer"`, class `ringAutoAnswer`                              | to be confirmed on a VVX                                                                                                  |
  | Snom, Fanvil | none: enable auto answer by Call-Info/Alert-Info on the phone                                         | no documented provisioning key found                                                                                      |

  Grandstream publishes no WP8xx config template any more, and neither the
  WP8xx guide nor its release notes give P-values for the speaker volume or
  the warning beep (the beep before an auto answer is on by default). Set
  them in a copied template if you need other values. The WP822 has a PTT
  button instead of programmable keys: dial the page code on the handset.

- **Troubleshooting.** A phone that ignores the headers rings as an
  ordinary call and the CDR shows `no_answer` for it: check the key above on
  that phone. Hello decides the `Alert-Info` value from the phone's
  `User-Agent` at registration (`auto-answer` for Grandstream and unknown
  phones).
- **Multicast.** This is Hello's server paging, through Hello, so it works
  across subnets. The phones' own multicast paging (LAN only, no server)
  is separate and not yet provisioned.

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
