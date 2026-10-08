# Worked examples

## Test before and after

`testRouting` body for an outbound call from extension 101:

```json
{ "from": "101", "number": "0471123456" }
```

For an inbound call on trunk 3, with the carrier's Request-URI host:

```json
{ "from": "trunk:3", "number": "+3225551234", "sipDomain": "pbx.example.com" }
```

A decision reads like:

```json
{
  "decision": {
    "kind": "outbound",
    "route": "Belgian mobiles",
    "trunks": ["carrier-a", "carrier-b"],
    "number": "+32471123456"
  },
  "trace": [
    { "n": 1, "text": "0471123456 is not an extension" },
    { "n": 2, "text": "route Emergency: prefix 112 does not match" },
    { "n": 3, "text": "route Belgian mobiles: prefix 04 matches" }
  ]
}
```

## A registration trunk

`createTrunk`:

```json
{
  "name": "carrier-a",
  "mode": "registration",
  "username": "3225550000",
  "password": "from the carrier",
  "realm": "sip.carrier-a.example",
  "destinations": [{ "host": "sip.carrier-a.example", "port": 5060 }]
}
```

Ask the user for the password; never invent one, and do not repeat it back.

## Belgian mobiles out of carrier A, then B

`createOutboundRoute`:

```json
{
  "name": "Belgian mobiles",
  "matchKind": "prefix",
  "match": "04",
  "trunks": [1, 2],
  "numberTransform": { "strip": 1, "prefix": "+32" }
}
```

Then `reorderOutboundRoutes` with every outbound route id, this one placed
before any broader catch-all:

```json
{ "ids": [5, 9, 2, 3] }
```

## The main number to the reception group

`createInboundRoute`:

```json
{
  "name": "Main number",
  "didKind": "exact",
  "did": "+3225551234",
  "destinationKind": "extension",
  "destination": "600"
}
```

## A sequential hunt group

`createRingGroup`:

```json
{
  "name": "Support",
  "strategy": "sequential",
  "ringTimeout": 20,
  "failureKind": "voicemail",
  "failureTarget": "600",
  "members": [
    { "extensionId": 7, "position": 1, "weight": 1, "delay": 0 },
    { "extensionId": 8, "position": 2, "weight": 1, "delay": 0 }
  ]
}
```

Check the member fields against the tool's input schema; it is the API's
schema and is authoritative.
