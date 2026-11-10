# Phone LDAP directory

Hello serves a read-only LDAP directory for desk phones. A phone searches the
same address books it is already delivered as an XML directory (the
extensions book, the company book, its group's named books, and the personal
book of the user linked to its extension), over LDAPS. The XML directory stays
delivered alongside.

LDAP is off until you turn it on twice: on the server, and per phone group.

## Turn it on

1. **Server.** Set `HELLO_LDAP_ENABLED=true` on hello-control with a
   certificate pair (`HELLO_LDAP_TLS_CERT`, `HELLO_LDAP_TLS_KEY`). It also
   needs `HELLO_PROV_PUBLIC_URL`: phones are given that host. With the Helm
   chart:

   ```yaml
   control:
     provisioning:
       publicUrl: https://prov.example.com
     ldap:
       enabled: true
       tlsSecret: hello-ldap-tls # tls.crt and tls.key, e.g. from cert-manager
   ```

   The certificate must be valid for the provisioning host name and signed by
   the CA the phones trust (below). The chart adds a `<release>-ldap` Service
   on port 636.

2. **Phone group.** Open the phone group, switch on **LDAP directory** and
   save (`PUT /api/v1/phone-groups/{id}/ldap`). Each phone of the group gets
   its LDAP settings at its next fetch (or resync it from the group).

3. **Phone page.** Shows the bind DN, when the phone last queried ("Directory
   not queried" until it has) and a **Rotate LDAP password** button.

## Settings

| Variable                      | Default             | Meaning                                                            |
| ----------------------------- | ------------------- | ------------------------------------------------------------------ |
| `HELLO_LDAP_ENABLED`          | `false`             | Serve LDAP.                                                        |
| `HELLO_LDAP_ADDR`             | `:636`              | LDAPS listener (TLS 1.2 minimum).                                  |
| `HELLO_LDAP_STARTTLS_ADDR`    | unset               | Optional listener (389) that offers StartTLS and refuses all else. |
| `HELLO_LDAP_BASE`             | `dc=hello,dc=local` | Base DN.                                                           |
| `HELLO_LDAP_ANON`             | `false`             | Allow an anonymous bind (extensions and company books only).       |
| `HELLO_LDAP_TLS_CERT`, `_KEY` | unset               | Server certificate pair, required when enabled.                    |
| `HELLO_LDAP_PUBLIC_PORT`      | `636`               | The port phones are given (the Service port, if it differs).       |

Plain LDAP is never served. On the StartTLS listener every operation before
TLS gets `unwillingToPerform`, except StartTLS and the Root DSE read.

## What a phone sees

- **Bind:** simple bind only, DN `uid=<phone id>,ou=phones,<base>`, with a
  generated 32-character password. SASL and anonymous bind are refused unless
  `HELLO_LDAP_ANON` is set. 5 failed binds in 10 minutes per DN and per
  source address answer `busy`.
- **Entries:** `cn=<opaque id>,ou=phonebook,<base>`, class `inetOrgPerson`,
  attributes `cn`, `givenName`, `sn`, `displayName`, `o`, `telephoneNumber`
  (work and other numbers), `mobile`, `homePhone`. The DN does not carry the
  name. Passwords, bind DNs and other attributes are never returned.
- **Searches:** `and`, `or`, `not`, equality, substrings, presence (approximate
  match is equality); scopes base, one level and subtree. At most 200 entries
  (`sizeLimitExceeded` with the first 200), 10 s, filter depth 10, 50 filter
  elements (`adminLimitExceeded`), paged results (RFC 2696) with pages of at
  most 100. Telephone numbers match without spaces, dashes and brackets.
- **Writes** (add, modify, delete, modifyDN, compare) are refused.
- Logs carry counts and result codes only, never DNs, attributes or filters.

## Passwords

The password is sealed under `HELLO_SECRET_KEY`, because the phone's
configuration carries it. It is created the first time a phone of an enabled
group needs it. `POST /api/v1/phones/{id}/ldap/password` (or the button)
creates a new one, shows it once and queues a check-sync. The old password
keeps working until the phone has fetched its new configuration (at most 7
days), then stops.

If you change `HELLO_LDAP_BASE`, resync the phones: the DN and base they hold
are rendered from it.

## Trust the server (LDAPS)

Phones must trust the CA that signed the LDAP certificate. Hello's
provisioning already hands out its CA (`/p/ca.crt`):

| Vendor      | How the phone gets the CA                                                                                       |
| ----------- | --------------------------------------------------------------------------------------------------------------- |
| Yealink     | `static.trusted_certificates.url`, already written by the template. Nothing more to do.                         |
| Poly        | `device.sec.TLS.customCaCert1`, already written. Confirm on a VVX that the LDAP client uses that list (verify). |
| Grandstream | WP8x6: `P8433`, already written. WP856 and WP8xx: upload the CA in the web UI (Security, Trusted CA).           |
| Snom        | `<certificates>` in the settings file, already written.                                                         |
| Fanvil      | `CACertURL`, already written.                                                                                   |

A certificate from a public CA needs no step.

## Vendor keys and what is confirmed

The built-in templates write these keys only to a phone of a group with LDAP
on; a model with no LDAP client keeps its XML book.

| Vendor      | Keys                                                                                                                                                                                                                       | Status                                                                                                                                                                                                                                                   |
| ----------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Yealink     | `ldap.enable`, `host`, `port`, `base`, `user`, `password`, `max_hits`, `name_filter`, `number_filter`, `name_attr`, `numb_attr`, `display_name`, `version`                                                                 | Names from the Administrator Guide. `ldap.tls_mode = 2` (LDAPs) and `call_in_lookup`, `call_out_lookup`: verify, needs hardware.                                                                                                                         |
| Poly        | `dir.corp.address`, `port`, `transport`, `baseDN`, `user`, `password`, `scope`, `filterPrefix`, `pageSize`, `attribute.N.*`                                                                                                | Names from the UC Software Administrator Guide. `transport="TLS"` and the attribute set: verify, needs hardware.                                                                                                                                         |
| Grandstream | `P8020` to `P8032` (server, port, base, user, password, attributes, filters, transport)                                                                                                                                    | **Verify, needs hardware.** The references on hand do not list the LDAP P-values for the WP8x6, WP856 or WP8xx; the numbers are unconfirmed. Do not enable LDAP for a Grandstream group before checking them against the model's configuration template. |
| Snom        | `ldap_server` (`ldaps://`), `ldap_port`, `ldap_base`, `ldap_username`, `ldap_password`, `ldap_max_hits`, `ldap_search_filter`, `ldap_number_filter`, `ldap_name_attributes`, `ldap_number_attributes`, `ldap_display_name` | Verify, needs hardware (the TLS spelling).                                                                                                                                                                                                               |
| Fanvil      | the `<LDAP CONFIG MODULE>` block                                                                                                                                                                                           | Verify, needs hardware (key names).                                                                                                                                                                                                                      |

`TestLDAPKeys` (internal/prov) and `TestLDAPAsVendors` (test/integration) read
each vendor's rendered keys back and run the phone's bind and search against
the real listener; they confirm that Hello writes and serves what the
templates say, not that a phone accepts the keys.

## Failure modes

- PostgreSQL down: bind answers `unavailable`; a search serves the last
  rendered set from Valkey if there is one, else `unavailable`.
- Valkey down: sets are rendered from PostgreSQL per request and the bind
  limit counts in memory.
- A phone ignoring the settings shows "Directory not queried" on its page.
