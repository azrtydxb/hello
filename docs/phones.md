# Connect two SIP phones to the lab

This guide takes you from a running lab to two phones calling each other: one
desk phone or softphone per extension, registered with Hello over SIP/UDP. It
takes about ten minutes.

You need the lab host's LAN IP address (here `192.168.1.10`) and two SIP
phones or softphones on the same network that can reach it.

## 1. Start the lab with a reachable SIP address

Each SIP node writes its advertised address into every SIP message, and phones
send their replies there. The defaults (`hello-sip-1:5060`) only resolve
inside Docker, so advertise the host's LAN IP instead:

```sh
HELLO_SIP1_ADVERTISED=192.168.1.10:5060 \
HELLO_SIP2_ADVERTISED=192.168.1.10:5062 \
docker compose -f deploy/docker-compose/compose.yaml up -d --build --wait
```

hello-sip-1 answers on UDP port 5060 and hello-sip-2 on UDP port 5062.

## 2. Create two extensions and their devices

Open the UI at `http://192.168.1.10:8080` and sign in as `admin` with the lab
password `hello-lab-admin`. Then:

1. Under **Extensions**, create `101` (Alice) and `102` (Bob).
2. Under **Devices**, create a device for extension 101 with SIP username
   `alice-desk`. Copy the secret from the dialog straight away: it is shown
   once, and only rotating it shows a new one.
3. Do the same for extension 102 with SIP username `bob-desk`.

The same steps work through the API; see `/api/v1/openapi.json`.

## 3. Configure each phone

| Phone setting                | Value                                               |
| ---------------------------- | --------------------------------------------------- |
| SIP server / registrar       | `192.168.1.10`                                      |
| Port                         | `5060` (or `5062` to use the second node)           |
| Transport                    | UDP                                                 |
| SIP domain / realm           | `hello.lab`                                         |
| User ID / username / auth ID | the device's SIP username, e.g. `alice-desk`        |
| Password                     | the device secret                                   |
| Outbound proxy               | none, or the same address and port as the registrar |
| Registration expiry          | 60–3600 seconds                                     |

Point the two phones at different ports to see a call cross both SIP nodes.

## 4. Check and call

The UI's **Registrations** page lists both phones within a few seconds, with
the node each registered through. Dial `102` from Alice's phone: Bob's phone
rings, and **Active Calls** shows the call while it is up. After you hang up,
**Call History** shows the call record.

## When it does not work

- **The phone shows "registration failed" or 403.** Check the username, secret
  and domain `hello.lab`. Ten failed attempts from one IP within five minutes
  lock that IP out for the rest of the window.
- **The phone registers, but calls never ring or drop after about 30 seconds.**
  The advertised address is not reachable from the phone. Restart the lab with
  the `HELLO_SIP*_ADVERTISED` values from step 1.
- **No audio.** Phase 1 does not relay media. The two phones send RTP directly
  to each other, so they must be able to reach each other's IP.
