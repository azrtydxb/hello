---
name: hello-setup
description: Set up users on a Kuvryn Hello PBX through its MCP server - create extensions, SIP devices, voicemail boxes and auto-provisioned desk phones, and tell the administrator which show-once secrets to collect in the console. Use when someone asks to add a person, a number, a phone or a softphone to Hello, to move a phone to another extension, or to change voicemail settings.
license: Apache-2.0
metadata:
  hello-version: "v1"
---

# Hello setup

Hello is a clustered SIP PBX. Its management API is exposed to you as MCP
tools named after the API's operations; this skill covers the directory side:
extensions (the numbers people dial), devices (SIP accounts that register
for an extension), voicemail, and phones that provision themselves.

## Connecting

- MCP server: `https://<hello host>/mcp` (on kw: `https://hello.kw.watteel.lab/mcp`),
  Streamable HTTP. The server advertises its OAuth authorization server; your
  client opens a browser for consent.
- Scopes: `read` to look, `write` to create and change. Ask for both.
  `secrets` is never needed: tools that return show-once secrets are not
  exposed over MCP, and secret values in results are replaced by a marker.
- Your user's role bounds what you may do: a `viewer` can only read, an
  `operator` can make every change in this skill.

## The model in one paragraph

An **extension** is a number and a name (`createExtension`). A **device** is a
SIP username and secret that registers on behalf of one extension
(`createDevice`); an extension can have several devices, and all of them ring.
A **phone** is a physical desk phone known by its MAC address, vendor and model
(`createPhone`); it is bound to an extension and owns one device, and it
fetches its configuration (including that device's secret) from Hello's
provisioning service. Voicemail belongs to the extension
(`getExtensionVoicemail`, `updateExtensionVoicemail`).

## Workflows

### Add a person with a softphone

1. `listExtensions` and check the number is free.
2. `createExtension` with the number and the person's name.
3. `createDevice` for the new extension's id with a SIP username such as
   `alice-laptop`. The result's secret is withheld from you.
4. Tell the administrator: open **Devices** in the Hello console and rotate
   the device's secret to see it (it is shown once there), then enter the SIP
   username, secret and the SIP server address in the softphone.
5. After they configure it, `listRegistrations` should show the device; if
   not, use the hello-troubleshoot skill.

### Add a desk phone that provisions itself

1. Make sure the extension exists (`listExtensions`, else `createExtension`).
2. `listPhones` to check the MAC is not already known.
3. `createPhone` with the MAC, vendor, model and the extension id. Hello creates
   (or binds) the device and generates a provisioning token. The provisioning
   URL in the result is withheld from you.
4. Tell the administrator how the phone finds Hello: the DHCP options or
   vendor redirect from `getProvSettings`, or the provisioning URL shown on the
   phone's page in the console (**Phones**).
5. After the phone boots, `listPhoneFetches` for the phone shows its fetches and
   `listRegistrations` shows it registered.

### Move a phone or device to another extension

- Phone: `updatePhone` with the new extension id. This rebinds the phone and
  rotates its device's secret; the phone picks the new secret up at its next
  fetch, so nobody has to type anything.
- Bare device: `updateDevice` with the new extension id. The SIP credentials
  stay the same.

### Voicemail

`getExtensionVoicemail` reads the box; `updateExtensionVoicemail` sets the
PIN and the e-mail address for notifications (an empty address turns e-mail
off). Greetings are audio uploads and are done in the console.
`listVoicemailBoxes` shows every box with its message counts.

### Remove someone

`deleteExtension` deletes the extension and its devices. Delete their phones
first with `deletePhone` if the hardware should be forgotten too. Ask before
deleting: there is no undo.

## What only the administrator can do

These values are shown once, to a person, in the console, never to you:

- a device's SIP secret (Devices: create or rotate);
- a phone's provisioning URL and token (Phones: add, rotate token, re-arm);
- a phone's web admin password (Phones: reveal).

Say clearly which page and which button, and do not ask the user to paste
secrets into the conversation.

## References

- [Tools for setup](references/tools.md) - every tool this skill uses, with its scope.
- [Worked examples](references/examples.md) - request bodies for the workflows above.
- [Failures](references/failures.md) - what the common errors mean and what to do.
