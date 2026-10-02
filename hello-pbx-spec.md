# Hello
## Modern SIP-Only Highly Available IP PBX

**Project:** Hello  
**Backend:** Go  
**Frontend:** React + TypeScript  
**Deployment:** Containers, Docker Compose, Kubernetes  
**Architecture:** Distributed, active/active, API-first  
**Scope:** SIP-only IP telephony

---

# 1. Vision

Hello is a modern SIP-only IP PBX designed from day one for containers, automation, observability, horizontal scaling, and high availability.

Rather than building another monolithic PBX, Hello separates SIP edge/registration, call control, media, management, UI, durable state, ephemeral state, and observability.

> **Core principle:** A Hello SIP node must be disposable. Losing one SIP node must not make the PBX unavailable.

Hello is not intended to reproduce every historical feature of Asterisk or FreeSWITCH. It should provide the functionality modern SIP environments need while remaining understandable, scalable, resilient, and easy to troubleshoot.

# 2. Goals

- SIP extensions and multiple devices per extension
- SIP REGISTER and location service
- SIP over UDP, TCP, and TLS
- Internal extension calling
- SIP trunks
- Inbound and outbound routing
- Number and caller-ID manipulation
- Structured dial plans
- Ring and hunt groups
- Call forwarding, DND, hold, and transfer
- Voicemail
- CDRs
- Live calls and registrations
- SIP diagnostics and routing traces
- REST API and modern web GUI
- Docker Compose and Kubernetes
- Horizontal scaling
- Active/active SIP nodes
- HA shared state
- Metrics, logs, and traces
- Clean integration with SIP-based AI voice agents

# 3. Non-Goals

Initial releases do not need analog FXS/FXO, ISDN/PRI, telephony cards, proprietary phone protocols, Asterisk compatibility, every legacy PBX feature, full carrier-SBC functionality, WebRTC, large-scale conferencing, or mandatory transcoding.

Hello is a **SIP PBX**, not a universal legacy telephony platform.

# 4. Design Principles

1. **SIP first.** UDP/TCP/TLS initially; WSS/WebRTC later.
2. **Container native.** Fast startup, graceful shutdown, health endpoints, structured logs, and no required local persistent state.
3. **Active/active.** No idle PBX standby architecture.
4. **Stateless where possible.** PostgreSQL for durable configuration and appropriate distributed storage for ephemeral state.
5. **Separate signaling and media.** RTP is not unnecessarily coupled to SIP signaling.
6. **API first.** Everything the GUI can configure is also configurable through an API.
7. **Observable.** Operators can understand registrations, routes, trunks, failures, calls, media, and node health without immediately requiring packet captures.

# 5. High-Level Architecture

```text
                         +----------------------+
                         |      Hello UI        |
                         |   React/TypeScript   |
                         +----------+-----------+
                                    | HTTPS
                         +----------v-----------+
                         |    hello-control     |
                         |      Go REST API     |
                         +-----+-----------+----+
                               |           |
                         PostgreSQL     Shared State
                               |        / Event Bus
              +----------------+-----------+----------------+
              |                                             |
      +-------v---------+                           +-------v---------+
      |   hello-sip-1   |                           |   hello-sip-2   |
      | Registrar       |                           | Registrar       |
      | Proxy / B2BUA   |                           | Proxy / B2BUA   |
      | Call Control    |                           | Call Control    |
      +-------+---------+                           +-------+---------+
              |                                             |
              +----------------------+----------------------+
                                     |
                              SIP Entry Point
                                     |
                   +-----------------+----------------+
                   |                                  |
               SIP Phones                         SIP Trunks
                                                      |
                                                   Carrier
```

Media may be direct:

```text
Endpoint A <---------- RTP ----------> Endpoint B
```

or anchored:

```text
Endpoint A <---- RTP ----> hello-media <---- RTP ----> Endpoint B
```

# 6. Components

## 6.1 hello-control

Go management/control-plane service responsible for REST APIs, authentication/RBAC, extensions/devices, trunks, routes, dial plans, cluster configuration, CDR queries, audit, and configuration validation/distribution.

It is **not** in the real-time media path. Loss of the control plane must not terminate calls or prevent already-configured SIP nodes from processing normal calls.

## 6.2 hello-sip

Real-time Go SIP service responsible for UDP/TCP/TLS listeners, REGISTER, digest authentication, registrar/location service, INVITE, ACK, CANCEL, BYE, OPTIONS, REFER, required SUBSCRIBE/NOTIFY, UPDATE/re-INVITE, SIP transactions/timers, dialog management, routing, trunk selection, B2BUA behavior, NAT-aware signaling, and call events.

Use a mature Go SIP library instead of implementing RFC 3261 parsing and transactions from scratch. `emiago/sipgo` is a strong initial candidate, hidden behind a Hello-owned abstraction.

## 6.3 hello-media

Optional, independently scalable media service. Prefer direct RTP when appropriate. Anchor media for NAT traversal, topology hiding, recording, RTP statistics, announcements, music on hold, voicemail, transcoding, conferencing, and AI audio integration.

Do not build an entire RTP engine simply for ownership. Define an interface capable of controlling a proven media relay first.

## 6.4 hello-ui

React + TypeScript application for administration, provisioning, routing, monitoring, active calls, HA, CDRs, audit, and diagnostics. The UI consumes the public management API.


# 7. Data Architecture

## 7.1 PostgreSQL

PostgreSQL is authoritative for durable state:

- Users and roles
- Extensions
- Devices
- SIP credentials
- Trunks
- Inbound/outbound routes
- Dial plans
- Ring/hunt groups
- Voicemail configuration
- Forwarding rules
- Schedules
- Cluster configuration
- CDRs
- Audit events
- API credentials

Production deployments must support HA PostgreSQL.

## 7.2 Valkey / Redis

Potential ephemeral/distributed state:

- Registration/contact locations
- Node membership
- Short-lived locks
- Rate limits
- Cache invalidation
- Presence
- Event fan-out
- Selected transient routing/dialog metadata

Do **not** blindly store every SIP transaction in Redis. Latency, consistency, ordering, expiry, ownership, and failure semantics must be explicit.

## 7.3 Events

Canonical events include:

```text
extension.registered
extension.unregistered
call.created
call.ringing
call.answered
call.transferred
call.ended
trunk.up
trunk.down
node.joined
node.draining
node.left
voicemail.created
configuration.changed
```

Start behind an internal event abstraction. Redis/Valkey Streams may be enough initially; NATS can be supported later.

# 8. Registrar and Location Service

Any healthy SIP node may receive REGISTER.

Track at least:

```text
AOR
Contact URI
Source address/port
Transport
Expiry
User-Agent
Path/Route data
Receiving node
Last update
```

Support multiple contacts per AOR.

```text
Extension 101
  + desk-phone
  + mobile-softphone
  + second-office-phone
```

Routing policy decides whether contacts ring simultaneously, sequentially, or according to another policy.

# 9. Extensions and Devices

Separate extension identity from devices.

```text
Extension: 101
Name: Pascal

Devices:
  desk-phone-01
  softphone-01
```

Device properties include SIP username, generated secret, allowed transports, registration policy, codecs, NAT policy, caller-ID policy, maximum simultaneous calls, and optional source-IP restrictions.

# 10. Security

SIP security:

- Digest authentication
- TLS
- Configurable IP ACLs
- REGISTER rate limiting
- INVITE rate limiting
- Failed-auth throttling
- Per-device permissions
- Trunk source validation

Management security:

- Local users initially
- Secure sessions/tokens
- RBAC
- API tokens
- Audit trail

Future:

- OIDC
- LDAP/AD
- External identity providers

Secrets must never appear in logs or API responses after creation.

# 11. Dial Plan and Routing

Avoid exposing a programming-language-like dial plan to normal administrators. Use structured rules.

```text
Outbound Route: UAE Mobile

Match:
  ^05[0-9]{8}$

Transform:
  +971${number_without_first_zero}

Trunks:
  1. carrier-primary
  2. carrier-backup
```

Rules support:

- Prefix and regex matching
- Number rewriting
- Caller-ID rewriting
- Schedules
- Extension groups
- Trunk priority
- Failover based on SIP response
- Concurrency limits
- Emergency routing policies

## Explainable Routing

Every call should expose a routing trace:

```text
Call 01J...
Source: extension 101
Destination: 0501234567

1. Internal extension lookup -> no match
2. Route "UAE Mobile" -> match
3. Rewrite -> +971501234567
4. carrier-primary -> healthy
5. INVITE -> 503
6. Failover permitted for 503
7. carrier-backup -> 100
8. carrier-backup -> 180
9. carrier-backup -> 200
10. Call established
```

This trace must be visible in the GUI.

# 12. SIP Trunks

Support:

- Registration-based trunks
- IP-authenticated trunks
- Digest credentials
- UDP/TCP/TLS
- Multiple destinations
- DNS/SRV where applicable
- OPTIONS health checks
- Priority/failover

Track reachability, OPTIONS latency, registration state, active calls, failed calls, SIP response distribution, configured capacity, and current utilization.

# 13. Inbound Routing

Match on:

- DID/called number
- Source trunk
- Source IP
- SIP domain
- Selected SIP headers
- Schedule

Destinations:

- Extension
- Ring group
- Hunt group
- Voicemail
- Announcement
- External number
- SIP URI
- AI voice agent

# 14. Ring and Hunt Groups

Strategies:

- Ring all
- Sequential
- Round robin
- Longest idle
- Weighted

Example:

```text
Sales
Members: 101, 102, 103
Strategy: ring-all
Timeout: 20s
Failure destination: voicemail 200
```

# 15. Call Features

Target functionality:

- Blind transfer
- Attended transfer
- Hold
- Forward always
- Forward busy
- Forward no-answer
- DND
- Caller ID
- Call waiting
- Simultaneous ringing
- Ring groups
- Hunt groups
- Voicemail
- BLF/presence
- Call pickup

Features should be added incrementally with interoperability tests.

# 16. Codecs and Media

Initially prioritize pass-through:

- G.711 PCMU
- G.711 PCMA
- G.722
- Opus where appropriate

Codec policy is configurable. Transcoding belongs in the media tier.

Direct media:

```text
Phone A ---------------- RTP ---------------- Phone B
```

Anchored media:

```text
Phone A ---- RTP ---- hello-media ---- RTP ---- Phone B
```

Media nodes advertise health, address, capabilities, current sessions, and capacity.


# 17. High Availability

HA is a **core product capability**, not a deployment afterthought and not something Hello gets merely because it runs on Kubernetes.

## 17.1 HA Level 1 — Management HA

Failure of `hello-control` or `hello-ui` does not interrupt telephony.

Existing calls continue, registrations continue, and new calls use the last valid configuration available to the SIP nodes.

## 17.2 HA Level 2 — Registration HA

If one SIP node fails, endpoints can register or re-register through another healthy SIP node.

Registration/location information must therefore be available cluster-wide where routing requires it.

## 17.3 HA Level 3 — New-Call HA

Loss of a SIP node must not prevent surviving nodes from handling:

- New internal calls
- Incoming trunk calls
- Outgoing trunk calls
- New registrations
- Re-registrations

**HA Levels 1–3 are mandatory for the first production-ready release.**

## 17.4 HA Level 4 — In-Call HA

An established call survives complete loss of the SIP node controlling its dialog.

This is materially harder.

```text
Phone A
   |
hello-sip-1
   |
Phone B

hello-sip-1 dies
       X

The RTP session should remain where possible,
and hello-sip-2 must be capable of handling
future BYE/re-INVITE/UPDATE/session refreshes.
```

This requires deliberate design around:

- Dialog replication
- Transaction state
- Call-ID
- Local/remote tags
- CSeq
- Route sets
- Record-Route
- Contact
- SDP
- Session timers
- Mid-dialog requests
- Media state
- Ownership changes

Level 4 is an advanced capability. Do not claim seamless in-call HA until specific failure scenarios are implemented and automatically tested.

## 17.5 Active/Active SIP

All healthy SIP nodes carry production traffic:

```text
                    +------------+
Phone ----------->  | hello-sip-1 |
                    +------+-----+
                           |
                        shared
                         state
                           |
                    +------+-----+
Phone ----------->  | hello-sip-2 |
                    +------------+
```

## 17.6 Node Membership

Track:

- Node ID
- Advertised SIP addresses
- Supported transports
- Health
- Load
- Active calls
- Registrations
- Media capability
- Software version
- Configuration revision
- Start time

Lifecycle states:

```text
JOINING
READY
DRAINING
UNHEALTHY
OFFLINE
```

## 17.7 Graceful Draining

Before planned shutdown:

1. Mark node `DRAINING`.
2. Stop assigning new calls.
3. Optionally stop accepting new registrations.
4. Preserve existing dialogs.
5. Wait for active calls to end.
6. Apply configured drain timeout if required.
7. Exit.

This is mandatory for safe maintenance, autoscaling, and rolling upgrades.

## 17.8 Dependency Failure and Split Brain

Explicitly define behavior for:

- PostgreSQL unavailable
- PostgreSQL failover
- Valkey unavailable
- Valkey failover
- Network partition
- SIP node isolated from shared state
- Stale configuration
- Duplicate registration updates
- Delayed/out-of-order events
- Media-node failure
- Load-balancer failure

Fail predictably rather than silently operating with unknown cluster state.

# 18. SIP Traffic Distribution

SIP must not simply be hidden behind an ordinary HTTP ingress.

Supported patterns should include:

- DNS SRV
- Multiple A/AAAA records
- SIP-aware load balancers
- L4 load balancing
- Direct node addresses

Correctly preserve and reason about:

- Via
- Contact
- Record-Route
- Route
- Source/destination addressing
- Advertised addresses
- Mid-dialog routing
- NAT behavior

Each SIP node must distinguish its **bind address** from its **advertised SIP address**.

# 19. Kubernetes Architecture

Example:

```text
Kubernetes Cluster

hello namespace
|
+-- hello-control Deployment (2+ replicas)
|
+-- hello-ui Deployment (2+ replicas)
|
+-- hello-sip (2+ instances)
|
+-- hello-media (optional, scalable)
|
+-- PostgreSQL HA
|
+-- Valkey HA
|
+-- metrics / telemetry
```

Requirements:

- SIP networking is documented explicitly.
- Do not assume standard Kubernetes HTTP ingress is suitable for SIP.
- Use pod anti-affinity/topology spreading for redundant SIP and control-plane replicas.
- Support graceful termination/draining.
- Readiness must prevent traffic reaching a joining or draining node.
- Persistent storage is limited to components that actually require it.
- Rolling upgrades must be tested with active calls.
- Kubernetes failure behavior must match Hello's explicit HA guarantees.

# 20. Docker Compose

Development and small-install topology:

```text
hello-ui
hello-control-1
hello-control-2
hello-sip-1
hello-sip-2
postgres
valkey
```

Optional:

```text
hello-media
prometheus
grafana
otel-collector
```

A developer must be able to launch a complete two-SIP-node Hello lab with one command.


# 21. Web UI

Primary navigation:

```text
Dashboard
Extensions
Devices
Trunks
Routes
Dial Plans
Ring Groups
Voicemail
Active Calls
Call History
Cluster
Diagnostics
System
```

## Dashboard

Show:

- Registered extensions
- Active calls
- Calls today
- Healthy/unhealthy trunks
- SIP nodes
- Media nodes
- Recent failures
- Calls per second
- Concurrent sessions

## Cluster Page

Example:

```text
HELLO CLUSTER

Node          State       SIP       Calls    Registrations   Version
hello-sip-1   Healthy     Ready       12          241        0.1.0
hello-sip-2   Healthy     Ready        9          228        0.1.0
hello-sip-3   Draining    Ready        2           96        0.1.0

PostgreSQL    Healthy
Valkey        Healthy

Configuration revision: 1284
```

## Active Call View

```text
101 -> +971501234567
State: Connected
Duration: 00:04:13
SIP node: hello-sip-2
Trunk: carrier-primary
Media: Direct
Codec: PCMA
```

## Diagnostics

Provide:

- SIP routing trace
- Registration inspection
- Trunk OPTIONS status
- Sanitized SIP message trace
- Node state
- Configuration revision
- Call failure explanation

Troubleshooting should be a first-class Hello feature.

# 22. CDRs

Record:

- Internal call/correlation ID
- SIP Call-ID where appropriate
- Source
- Destination
- Original destination
- Rewritten destination
- Start time
- Ring time
- Answer time
- End time
- Duration
- Billable/connected duration
- Selected route
- Selected trunk
- SIP node
- Media mode/node
- Codec
- Final SIP status
- Termination side
- Failure reason

# 23. Observability

Use structured JSON logs, correlation IDs, OpenTelemetry, and Prometheus-compatible metrics.

Example metrics:

```text
hello_sip_registrations
hello_active_calls
hello_calls_total
hello_call_failures_total
hello_sip_requests_total
hello_sip_responses_total
hello_trunk_status
hello_node_ready
hello_route_decision_seconds
hello_media_sessions
```

Never expose SIP passwords, Authorization headers, or sensitive media in normal telemetry.

# 24. API

Version the API.

Example resources:

```text
/api/v1/extensions
/api/v1/devices
/api/v1/trunks
/api/v1/routes/inbound
/api/v1/routes/outbound
/api/v1/ring-groups
/api/v1/voicemail
/api/v1/calls
/api/v1/cdrs
/api/v1/registrations
/api/v1/cluster/nodes
/api/v1/diagnostics
/api/v1/events
```

Use WebSocket or SSE for live UI updates.

Publish OpenAPI documentation.

# 25. AI Voice-Agent Integration

An AI voice agent should appear to Hello as an ordinary SIP service:

```text
IP Phone
   |
   v
Hello
   |
   +---- extension 101 ---- IP Phone
   |
   +---- extension 102 ---- IP Phone
   |
   +---- extension 500 ---- AI Voice Agent
                                |
                             SIP/RTP
```

Hello does not need to know which LLM, speech stack, or application powers the agent.

This allows the same architecture to support:

- Home/lab demos
- Enterprise voice assistants
- Contact-center assistants
- Healthcare assistants
- Later carrier/IMS-facing integrations

# 26. Repository Structure

```text
hello/
├── cmd/
│   ├── hello-control/
│   └── hello-sip/
├── internal/
│   ├── api/
│   ├── auth/
│   ├── cluster/
│   ├── config/
│   ├── cdr/
│   ├── events/
│   ├── registrar/
│   ├── routing/
│   ├── sip/
│   ├── trunks/
│   └── telemetry/
├── migrations/
├── web/
├── deploy/
│   ├── docker-compose/
│   ├── kubernetes/
│   └── helm/
├── test/
│   ├── integration/
│   ├── interoperability/
│   └── failure/
├── docs/
├── go.mod
└── README.md
```

Start as a modular monorepo. Do not prematurely turn every package into a separate microservice.

# 27. Testing Strategy

## Unit Tests

Cover:

- Routing
- Number rewriting
- Authentication
- Trunk selection
- Registration expiry
- Configuration validation
- SIP/call state machines

## Integration Tests

Test real SIP flows against running Hello, PostgreSQL, and Valkey.

## Interoperability Tests

Use SIPp plus common physical SIP phones, softphones, and real external SIP trunks.

## HA / Failure Injection

Automate:

- Kill SIP node during REGISTER
- Kill SIP node during ringing
- Kill SIP node during connected call
- Kill/restart control plane
- PostgreSQL failover
- Valkey failover
- Network partition
- Media node loss
- Load-balancer loss
- Rolling upgrades
- Graceful draining

## Load Tests

Measure:

- Registrations per second
- Calls per second
- Concurrent dialogs
- OPTIONS load
- CDR/event throughput
- Routing latency

**Every HA promise must have an automated failure test.**


# 28. Implementation Phases

## Phase 0 — Foundation

Build:

- Monorepo
- Go project structure
- React/TypeScript UI
- PostgreSQL migrations
- Configuration framework
- Structured logging
- Metrics
- Dockerfiles
- Docker Compose
- CI pipeline

## Phase 1 — Minimum PBX

Build:

- SIP/UDP
- REGISTER
- Digest authentication
- Registrar
- Extensions
- Devices
- Internal calling
- Basic CDRs
- Live registrations
- Live calls

**Demo acceptance:** two physical SIP phones register with Hello and call each other.

## Phase 2 — Trunks and Routing

Build:

- Inbound trunks
- Outbound trunks
- Structured routes
- Number rewriting
- Caller-ID policy
- OPTIONS health
- Trunk failover
- Routing trace

## Phase 3 — HA

Build:

- Two active SIP nodes
- Shared registration/location state
- Node membership
- Health/readiness
- Graceful draining
- Redundant control plane
- Configuration revisions/cache invalidation
- Failure-injection test suite

**Acceptance:** killing one SIP container does not prevent new registrations or new calls through surviving nodes.

## Phase 4 — PBX Features

Build:

- Transfers
- Hold
- Forwarding
- DND
- Ring groups
- Hunt groups
- Voicemail
- Presence/BLF

## Phase 5 — Media

Build/integrate:

- Media-node abstraction
- Media anchoring
- NAT handling
- Announcements
- Voicemail media
- Recording
- RTP metrics
- Optional transcoding

## Phase 6 — Kubernetes

Build:

- Helm chart
- HA PostgreSQL deployment guidance
- HA Valkey deployment guidance
- Anti-affinity/topology rules
- SIP networking patterns
- Graceful rolling upgrades
- Autoscaling where appropriate

## Phase 7 — Advanced HA

Research and implement Level-4 dialog survival.

Define exact supported failure cases before describing it as seamless in-call HA.

# 29. Initial Production Acceptance Criteria

A serious first milestone is complete when:

1. `docker compose up` starts a two-SIP-node Hello cluster.
2. Two physical SIP phones can register.
3. Either SIP node may receive a registration.
4. Phones can call each other.
5. A SIP trunk can place and receive calls.
6. Routes and rewriting are configurable through the GUI.
7. Active registrations and calls are visible.
8. Every call has a routing trace and CDR.
9. Killing one SIP node leaves the cluster capable of accepting new registrations and calls.
10. A drained node exits without unnecessarily terminating existing calls.
11. The API can perform all core GUI configuration.
12. Metrics and health endpoints expose cluster status.
13. Failure behavior is covered by automated tests.

# 30. Engineering Rules

- Keep the real-time SIP path small and predictable.
- Do not make unnecessary synchronous database calls during SIP transaction handling.
- Cache configuration locally with explicit revision/invalidation semantics.
- Never rely on local disk for cluster-critical state.
- Do not conflate control-plane HA with call survival.
- Do not force RTP through Hello unless a feature requires it.
- Do not build custom SIP/RTP protocol machinery when a mature implementation meets the requirement.
- Treat graceful draining and failure injection as core features.
- Validate, revision, and audit configuration changes.
- Make failures operator-visible and explainable.
- Prefer deterministic behavior over hidden "smart" behavior.
- Avoid premature microservice decomposition.
- Keep external dependencies behind Hello-owned interfaces.
- Design every stateful subsystem with explicit ownership and failure semantics.

# 31. Definition of Hello

Hello should ultimately feel less like a traditional PBX appliance and more like a modern distributed communications platform:

```text
                 HELLO

          SIP Control Plane
                 |
     +-----------+-----------+
     |           |           |
 Registrar   Call Control   Routing
     |           |           |
     +-----------+-----------+
                 |
             SIP Fabric
          /      |       \
       Phone    Trunk    AI Agent
```

The user experience should hide unnecessary SIP complexity while retaining enough visibility that an engineer can understand exactly what happened to a call.

The combination of:

- Go
- SIP-only scope
- container-native operation
- active/active architecture
- API-first management
- explainable routing
- first-class diagnostics
- explicit HA behavior
- optional distributed media

is the core identity of **Hello**.

---

# 32. Guiding Statement

> **Hello is a modern, container-native SIP PBX where signaling nodes are disposable, routing is explainable, management is API-driven, and high availability is designed into the call architecture rather than bolted onto it afterward.**
