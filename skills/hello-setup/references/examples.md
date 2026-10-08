# Worked examples

Bodies are shown as the tool's `body` argument; path parameters (such as the
extension or phone id) are separate arguments named as in the API.

## Alice gets extension 101 and a softphone

`createExtension`:

```json
{ "number": "101", "name": "Alice Martin" }
```

The result carries the new extension's id, say `7`. Then `createDevice`:

```json
{ "extensionId": 7, "sipUsername": "alice-laptop" }
```

Tell the user: "Device alice-laptop exists. In the Hello console open
Devices, choose alice-laptop and rotate its secret to see it once; enter it
with SIP username alice-laptop in the softphone."

## Bob's Yealink T54W on extension 102

`createPhone`:

```json
{
  "mac": "80:5e:c0:12:34:56",
  "vendor": "yealink",
  "model": "T54W",
  "extensionId": 8,
  "label": "Bob's desk"
}
```

Then `getProvSettings` and tell the user which DHCP option (66 or 43) to set
or that the vendor redirect is configured, so the phone finds Hello on boot.

## Bob moves to extension 110

`updatePhone` for Bob's phone:

```json
{ "extensionId": 12 }
```

The phone's device secret is rotated and the phone fetches it at its next
check; no one types anything.

## Voicemail e-mail for 101

`updateExtensionVoicemail` for extension 7:

```json
{ "email": "alice@example.com" }
```
