# Tools for setup

Tool names are the API's operation ids. Read tools need the `read` scope and
the `viewer` role; change tools need `write` and `operator`.

| Tool                       | Scope | What it does                                                                             |
| -------------------------- | ----- | ---------------------------------------------------------------------------------------- |
| `listExtensions`           | read  | Every extension, ordered by number.                                                      |
| `getExtension`             | read  | One extension with its forwarding, DND and recording settings.                           |
| `createExtension`          | write | Create an extension (number of 2 to 10 digits, and a name).                              |
| `updateExtension`          | write | Change number, name, external number, DND, forwarding, voicemail and recording defaults. |
| `deleteExtension`          | write | Delete an extension and its devices.                                                     |
| `listDevices`              | read  | Every device, ordered by SIP username.                                                   |
| `getDevice`                | read  | One device; never its secret.                                                            |
| `createDevice`             | write | Create a device for an extension; its secret is withheld from MCP results.               |
| `updateDevice`             | write | Enable, disable or move a device to another extension.                                   |
| `deleteDevice`             | write | Delete a device.                                                                         |
| `listRegistrations`        | read  | Every registered contact in the cluster.                                                 |
| `getExtensionVoicemail`    | read  | One extension's voicemail box.                                                           |
| `updateExtensionVoicemail` | write | Set the voicemail PIN and notification e-mail.                                           |
| `listVoicemailBoxes`       | read  | Every box with message counts.                                                           |
| `listPhones`               | read  | Every provisioned phone, ordered by MAC.                                                 |
| `getPhone`                 | read  | One phone.                                                                               |
| `createPhone`              | write | Add a phone by MAC, vendor and model and bind it to an extension.                        |
| `updatePhone`              | write | Change a phone; a new extension or device rebinds it and rotates that device's secret.   |
| `deletePhone`              | write | Delete a phone; its device stays.                                                        |
| `listPhoneFetches`         | read  | The phone's provisioning fetches, newest first.                                          |
| `listProvTemplates`        | read  | Built-in and stored provisioning templates.                                              |
| `getProvSettings`          | read  | Provisioning URLs, CA fingerprint and DHCP option values.                                |
