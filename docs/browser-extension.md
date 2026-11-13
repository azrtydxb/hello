# Browser extension

The Hello browser extension (Chrome and Edge, Manifest V3) turns phone links on
the sites you choose into click-to-call, shows a notification with the caller's
name when your phone rings, and shows the state of the call you placed. It uses
the same click-to-call as the [dial page](click-to-call.md): Hello rings your
phone first and dials the number when you answer (spec
`.procoder/specs/integrations.md`, I-55). It is also the click-to-dial path for
CRMs and helpdesks that have no native Hello integration (Pipedrive, Zoho, the
PSA tools).

## Install

The package is built into the Hello web image. Open **My phone, Dial** in the
console; the **Browser extension** card links it.

1. Download `hello-extension.zip` and unpack it.
2. Open `chrome://extensions` (`edge://extensions`), turn on Developer mode and
   choose **Load unpacked**.
3. Open the extension's settings, enter the address of your Hello server
   (https only, or `http://localhost`), allow access to it when the browser
   asks, and choose **Sign in**.

Sign-in opens Hello's own consent page in a browser window. The extension asks
for `calls events read` and nothing else; a `user` may grant all three for their
own extensions. Signing out in the extension's settings revokes the grant at
the server; the person can also revoke it under their approved apps.

### Managed browsers (signed package)

When the image was built with the signing key, `hello-extension.crx` is offered
next to the zip. Chrome and Edge install a crx only through policy
(`ExtensionInstallForcelist` or `ExtensionInstallAllowlist`) with an update
manifest that an administrator hosts; the extension id for that policy is on the
card and below. Without the key only the zip is built; the extension and its id
work the same.

## The fixed id and signing key

A Chromium extension id is derived from its public key. The manifest carries a
fixed public key (`web/extension/manifest.json`, `key`), so the id is the same
for every build, every browser profile and every Hello installation:

```
kengpjalagamnmmkljldjhhgonmbdcph
```

Hello's OAuth server pre-registers the public client `hello-extension` with the
one redirect address `https://kengpjalagamnmmkljldjhhgonmbdcph.chromiumapp.org/`
(`internal/oauth/preregistered.go`). A Go test recomputes the id from the
manifest key, and a web test does the same, so the manifest, the server and the
build cannot drift apart.

The private half of that key signs the crx. It is never in git or in an image:
the image build reads it from the `extension_key` Docker build secret, which CI
fills from the repository secret `HELLO_EXTENSION_KEY` (the PEM). The build
refuses a key that is not the private half of the manifest key, because the id
would change. Locally:

```sh
cd web
HELLO_EXTENSION_KEY_FILE=/path/to/key.pem node extension/tools/build.mjs dist/downloads
```

Changing the key changes the id, so every installation would have to be
re-registered; keep the key.

## What it does

- **Phone links.** On the sites you list (the CRM domains), a click on a `tel:`
  or `callto:` link opens a box the page cannot style or read: "Call
  +32 11 111 222 from 101?" with Call and Cancel. Nothing is dialled until you
  click Call, and only a real click is caught, not one made by a script. Sites
  you did not list are untouched; `tel:` there goes to the browser's own
  handler (see "Use Hello for phone links" on the dial page). `callto:` is not
  a protocol the web may register, so this extension is the only place Hello
  handles it.
- **Numbers in text.** Optionally, on the same sites, numbers in the text are
  turned into links. It looks for international numbers (`+32 11 11 12 22`) and
  separated national numbers; dates, prices and ids are left alone. It skips
  links, inputs, code and editable text.
- **Call state.** The box shows the call as Hello reports it (your phone is
  ringing, dialling, connected, why it failed) and offers Hang up. The toolbar
  popup shows the same and has a field to dial a number.
- **Incoming calls.** `call.ringing` from the [event stream](integrations.md)
  becomes a desktop notification with the caller's name and number, for your own
  extensions. The service worker keeps one stream open, resumes it with
  `Last-Event-ID` after a drop, and an alarm every 30 seconds reopens it when the
  browser put the worker to sleep.

## Permissions and privacy

| Permission                           | Why                                                                                  |
| ------------------------------------ | ------------------------------------------------------------------------------------ |
| `identity`                           | `launchWebAuthFlow` for the sign-in                                                  |
| `storage`                            | settings, and the tokens (this browser profile only; revoked on sign-out)            |
| `notifications`                      | the incoming-call notification                                                       |
| `alarms`                             | wakes the worker to reopen the event stream                                          |
| `scripting`                          | registers the page script on the sites you allowed                                   |
| optional `https://*/*`, `http://*/*` | asked one site at a time on the settings page; the Hello server and each listed site |

There is no `<all_urls>` grant and no always-on content script: the page script
is registered only for sites you listed and the browser granted. The extension
sends numbers only to your Hello server. The server enforces everything a
person may do: own extensions only, the click-to-call limits and refusals in
[click-to-call.md](click-to-call.md), emergency numbers refused.

## Limits and verifying

- Firefox is not supported (Manifest V3 service workers differ and
  `launchWebAuthFlow` redirect addresses are not `chromiumapp.org`).
- The Chrome Web Store and Edge Add-ons listings are not planned (spec decision
  1); the package is self-hosted.
- Sites with a strict Content-Security-Policy are fine: the page script runs in
  an isolated world and its box lives in a closed shadow root.
- Screen-pop shows the name Hello already knows (extensions, phonebooks, the
  caller's own contacts). A CRM contact lookup is the CRM connectors' job.
