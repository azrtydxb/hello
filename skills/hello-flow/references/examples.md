# Example batches

All examples are sent first with `"dryRun": true`, then again without it.
`baseRevision` is the `configRevision` you read the graph at.

## After hours to voicemail

A schedule-driven time condition in front of the main number: open hours ring
the sales group, the rest goes to the voicemail of extension 100.

```json
{
  "baseRevision": 41,
  "dryRun": true,
  "ops": [
    {
      "ref": "tc",
      "operationId": "createTimeCondition",
      "body": {
        "name": "office-hours",
        "scheduleId": 3,
        "open": { "kind": "ring_group", "target": "sales" },
        "closed": { "kind": "voicemail", "target": "100" }
      }
    },
    {
      "operationId": "updateInboundRoute",
      "pathParams": { "id": "5" },
      "expect": "6c1f0e2a9b7d4e10",
      "body": {
        "destinationKind": "time_condition",
        "destination": "${tc.name}"
      }
    }
  ]
}
```

The request body of every operation is the one its own tool takes (here the
time condition tool of the `hello-ivr-and-hours` skill); a batch op uses it
unchanged.

## Verify

```text
traceFlow from=trunk:1 number=0201234567 at=2026-11-09T20:00:00+01:00
```

The last step must be the voicemail of 100; at 10:00 on a weekday it must be
the sales group.
