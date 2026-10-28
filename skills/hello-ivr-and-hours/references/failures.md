# Failures

| Answer                                                 | Meaning and fix                                                                                  |
| ------------------------------------------------------ | ------------------------------------------------------------------------------------------------ |
| 400 with `error.fields`                                | Each failing field is listed with its path; fix those and resend the whole body.                 |
| `audioBase64` over 700 KiB, or 400 "larger than 1 MiB" | The audio does not fit a tool call. Shorten or downsample it, or upload it in the console.       |
| 400 on `file`: "must be a RIFF/WAVE file"              | The bytes are not a WAV, or are compressed (MP3, ADPCM). Convert to uncompressed PCM.            |
| 409 on `createAnnouncement`                            | The name exists; use `replaceAnnouncement`.                                                      |
| 409 on a delete                                        | Something still names the object. The message says what; change that first (use `getFlowGraph`). |
| 400 "loops back"                                       | A time condition, IVR exit or queue failover leads back to itself.                               |
| 403 on an IVR or time condition                        | It has a paging destination, which needs `admin`.                                                |
| Dangling reference warning in `getFlowGraph`           | A destination names a number or group that does not exist.                                       |
