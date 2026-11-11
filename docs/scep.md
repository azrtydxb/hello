# Device certificates (SCEP) and 802.1X

Hello can give each phone its own client certificate and the 802.1X settings
that use it, so a phone can join a port or Wi-Fi network that RADIUS protects
with EAP-TLS, without anyone copying keys to the phone. For networks that use
passwords instead (PEAP, EAP-MD5), Hello writes the identity and password.

Everything here is off until you switch it on for a phone group.

## How it works

- **Device CA.** Hello keeps one active CA that signs the phones'
  certificates. Generate it in Hello (RSA 3072, self-signed), or import an
  intermediate CA of your enterprise PKI with its RSA key, so your RADIUS
  server already trusts what Hello issues. The key is sealed with
  `HELLO_SECRET_KEY` and never shown again. SCEP needs an RSA CA key: the
  phone encrypts its request to the CA certificate (RFC 8894).
- **SCEP server.** The provisioning listener answers SCEP (RFC 8894) at each
  phone's own provisioning URL followed by `scep`
  (`https://<prov host>/p/<token>/scep`). The token says which phone asks, so
  a phone can only ever get a certificate for itself. It answers only over
  HTTPS, only for an enabled phone bound to a device, and only while the
  phone's group has SCEP on; anything else is the same empty `404` as an
  unknown path.
- **One-time challenge.** Every phone gets its own challenge password in its
  configuration. A first enrolment must carry it; the enrolment uses it up and
  the next configuration carries a new one. A phone retrying the same request
  (same key) gets the same certificate back instead of a second one. Discard a
  challenge by hand with **New SCEP challenge** on the phone's page
  (`rotatePhoneSCEPChallenge`).
- **Renewal.** A phone renews before its certificate expires by signing the
  request with that certificate (SCEP RenewalReq); no challenge is needed, but
  the certificate must be a live one Hello issued to the same phone from the
  active CA.
- **What is issued.** The subject is `CN=<MAC>` (12 lowercase hex digits),
  whatever the phone asks for; extended key usage client authentication; the
  CRL distribution point is `http://<prov host>/p/scep-ca.crl`; the lifetime
  is the group's (default 365 days, 30 to 1825), never past the CA's own end.
  RSA keys under 2048 bits and EC keys other than P-256 and P-384 are refused.
- **Revocation.** Revoke a certificate on the Certificates tab
  (`revokeSCEPCertificate`); it goes on the CRL and can no longer renew.
  Removing a phone revokes its certificates. The CRL is signed per request and
  valid for 24 hours.

The CA certificate is at `http://<prov host>/p/scep-ca.crt` (PEM; DER with
`Accept: application/pkix-cert`) and the CRL at `/p/scep-ca.crl`. Both are
public and served over plain HTTP too, so a RADIUS server fetches them without
a token. Point the RADIUS server's EAP-TLS trust at that CA and its CRL check
at that URL, and match the certificate's common name to the identity (use
`{mac}` as the identity of an EAP-TLS profile).

## Setting it up

1. **Phones > Certificates**: generate or import the device CA.
2. Add an **802.1X profile**: method, identity (`{mac}` and `{MAC}` are filled
   in per phone), password for PEAP and EAP-MD5 (sealed, write-only), and
   optionally the CA the RADIUS server's certificate chains to and the server
   name the phone checks.
3. In the **Phone groups** table, switch SCEP on for a group and choose its
   wired profile.
4. The phones take it at their next fetch, or at once with
   `HELLO_PROV_AUTO_RESYNC` (or **Resync** on the group).

Try a new 802.1X profile on a group with one test phone first. A wrong
profile can take a phone off the network, and then it cannot fetch the fix:
it needs a port without 802.1X, or a factory reset.

The same steps through the API or MCP: `setSCEPCA`, `createDot1XProfile`,
`setPhoneGroupSecurity`; see the `hello-phones` skill.

## Phones and keys

Keys come from Grandstream's published configuration templates
(config-template-10212026.zip). They are not yet confirmed on a phone (see
Hardware checks).

| What         | Models                                                                                             | Keys                                                                                                                                                            |
| ------------ | -------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| SCEP         | WP816, WP826, WP836 (wp8x6_config_1.0.3.39); GRP2601 to GRP2604 (grp260x_config_1.0.7.74)          | `P8647` enable, `P8648` server URL, `P8653` challenge, `P8654 = 1` (2048-bit key), `P8655` applicant name (the MAC). SCEP off for the group writes `P8647 = 0`. |
| Wired 802.1X | GRP26xx (grp26xx_config_1.0.15.23, grp261x_2x_3x_5x_7x_config_1.0.13.127, grp260x_config_1.0.7.74) | `P7901` mode (1 EAP-MD5, 2 EAP-TLS, 3 PEAP), `P7902` identity, `P7903` password, `P8439` RADIUS CA.                                                             |

What Hello leaves alone:

- `P8652` (CA fingerprint): its format is not documented; the phone takes the
  CA from the SCEP server over HTTPS it already trusts.
- `P8662` (renew 20 days before expiry) and `P8663` (scope of use) stay at the
  phone's defaults.
- A group without a wired profile does not write `P7901`, so Hello never
  switches off 802.1X on a port that needs it. Removing the profile from a
  group therefore leaves the phones as they are.
- An EAP-TLS profile is written only to a phone whose group has SCEP on and
  whose model enrols (only the GRP260x of the desk phones); `P8440` (client
  certificate) is not written.
- The WP810/820/822/825 and WP856 templates have no SCEP keys, and the WP
  handsets have no wired port. Yealink, Poly, Snom and Fanvil get nothing yet:
  their 802.1X and SCEP keys are not confirmed from vendor documentation here.

## Wi-Fi (enterprise networks)

Wi-Fi profiles (phone-feature-parity S-23) reference an 802.1X profile by id
for WPA2/WPA3-Enterprise networks; the profile is not copied. Every profile is
in the render data as `.Dot1X.Profiles`, with the identity filled in for the
phone, the password opened, and the WP8x6 values already worked out:
`GSWifiEAP` for `P83004` (1 EAP-TLS, 2 PEAP), `GSWifiCAType` for `P83016` (3
with a CA, else 1; never 0, which skips verification) and `Usable` (false for
EAP-TLS on a phone without SCEP). Phase 2 of PEAP is MSCHAPv2 (`P83013 = 0`).
A Wi-Fi profile stores the profile id; a template finds it with
`{{range .Dot1X.Profiles}}{{if eq .ID $wifi.Dot1XID}}...`. Deleting an 802.1X
profile a group uses is refused; the Wi-Fi profiles table references
`dot1x_profiles (id)` with `ON DELETE RESTRICT` and the delete check names its
profiles too.

## Configuration

`HELLO_SCEP` (default `true`) is the switch for the whole deployment:
`false` answers every SCEP, CA and CRL request `404` and stops writing SCEP
settings, whatever the groups say. Nothing needs to be exposed beyond the
provisioning listener's `/p/` path, which the chart and kw already route.

## Monitoring

- `hello_scep_requests_total{op,result}`: `op` is `GetCACaps`, `GetCACert`,
  `PKIOperation`, `unknown` or `any`; `result` is `ok`, `issued`, `off`,
  `refused`, `bad_request`, `no_challenge`, `bad_renewal`, `bad_csr`,
  `weak_key`, `undecryptable` or `unsupported`.
- Every SCEP request is in the phone's fetch log with kind `scep` and result
  `scep_issued` or `scep_refused` (token redacted); every issued certificate
  is in the audit log as `scep_enrol` or `scep_renewal`.
- A refused enrolment counts towards the source's denial limit, like a wrong
  token.

## Hardware checks

Still to confirm on a WP836 and a GRP2602 with a RADIUS server (FreeRADIUS):

- the phone enrols with `P8647`/`P8648`/`P8653` and stores the certificate;
  which URL form it requests (`/scep?operation=...` or
  `/scep/pkiclient.exe?operation=...`; both are answered);
- which content cipher it uses for the request (Hello decrypts DES, 3DES and
  AES; it answers with AES-128-CBC, which GetCACaps advertises) and whether it
  accepts that answer;
- that it renews with RenewalReq before expiry, and what it does once its
  challenge is used;
- that EAP-TLS on Wi-Fi (`P83004 = 1`) and on the GRP wired port
  (`P7901 = 2`) presents the SCEP certificate, with `P8663` at its default;
- the format `P8652` expects, after which Hello can write the fingerprint;
- PEAP and EAP-MD5 on the GRP wired port with `P7903` as the password.
