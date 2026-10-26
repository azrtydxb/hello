# Voice prompts (text to speech)

Announcements and voicemail greetings can be written as text and turned into
audio once. The audio is stored like any uploaded WAV and plays from storage
on every call. **Announcements and the greetings you generate never call a
text-to-speech service while a call is running**, and nothing generates them
except an explicit click.

The exception is the voicemail voice ([voicemail.md](voicemail.md)): the
default greetings and the spoken `*97` menus use the model and voice saved
here, and are generated the first time a call needs them (the static ones when
you save the settings) and cached in the voicemail bucket as
`tts/hd/<sha256 of model, voice and text>.wav`. If the voice cannot be reached the
call carries on with tones and recordings.

Audio is generated through [OpenRouter](https://openrouter.ai), which offers
ElevenLabs models (for example `elevenlabs/eleven-multilingual-v2`), using
`github.com/azrtydxb/go-ai-sdk`. Hello asks for MP3, decodes it, converts it to
16-bit mono at the speech model's own sample rate (up to 48 kHz) and stores it as a WAV; playback resamples to each call.

## Set the key (once)

1. Sign in as an administrator and open **Platform, Voice (TTS)**
   (`/settings/voice`).
2. Paste the OpenRouter API key and choose the default model and voice (the
   lists come from OpenRouter's ElevenLabs models once a key is stored). Save.
3. Press **Test connection**. It checks the key with OpenRouter and spends no
   credits.

The key is sealed with AES-256-GCM under `HELLO_SECRET_KEY` and stored in the
database. It is write-only: the API and console only ever show whether a key is
set and its last four characters, and it is never logged or put in the audit
trail. **Remove key** deletes it; prompts already generated keep playing.

## Generate an announcement

On **Media, Announcements**:

- **New prompt from text** asks for a name and the text (up to 2,500
  characters), optionally a model and voice, and generates the audio once.
- The wand icon on a row opens the text editor of an existing announcement,
  including one that was uploaded (generating then replaces the audio).
  **Save text** keeps the text without generating. **Generate** or
  **Regenerate** calls OpenRouter once and replaces the stored audio.
- The Source column says Uploaded, Generated, or Text edited. Text edited means
  the text, voice or model differs from what the stored audio was generated
  from; the audio stays as it was until you regenerate.
- The editor has a player for the stored audio, so you can listen before
  using it.

Generating is idempotent per text, voice and model: asking again for the same
combination does nothing unless the button is clicked on an unchanged prompt
(an explicit "again"). Manual WAV upload and replace work as before.

## Voicemail greetings

In **Voicemail, Box settings**, each of the greeting and the unreachable
greeting has a source:

- **Upload a WAV**, saved with the box settings.
- **Use an existing announcement**, which copies its audio into the box.
- **Generate from text**, with its own **Generate greeting** button. The same
  text, voice and model never generate twice.

A box with none of these uses the **default spoken greeting** (shown as
"Default (AI voice)"); **Preview default greeting** plays it and **Revert to
default** drops a custom one. There are slots for the general greeting, the
busy greeting, the unreachable greeting and a recorded name; all of them can
also be recorded from the phone. See [voicemail.md](voicemail.md).

## Audio format

Uploads are stored as 16-bit mono PCM at their own sample rate (up to 48 kHz);
stereo is mixed to mono and 8, 24, 32-bit and float WAVs are converted.
Playback resamples to each call's rate. Compressed WAVs are refused with the
reason.

## Errors

A missing key, a rejected key (401), exhausted credits or rate limits (402,
429), an empty answer and a timeout (60 s) each fail with a clear message and
leave the stored audio untouched. Every successful generation is audited
(`generate`, without the text) and failures as `generate_failed`.

## API

| Route                                                      | Role     | Purpose                               |
| ---------------------------------------------------------- | -------- | ------------------------------------- |
| `GET/PUT /api/v1/tts/settings`                             | admin    | settings; `apiKey` is write-only      |
| `DELETE /api/v1/tts/settings/key`                          | admin    | remove the key                        |
| `POST /api/v1/tts/settings/test`                           | admin    | check the key                         |
| `GET /api/v1/tts/voices`                                   | operator | ElevenLabs models and voices          |
| `POST /api/v1/announcements/generate`                      | operator | new announcement from text            |
| `POST /api/v1/park-lots/announcement/generate`             | operator | spoken park slot clips                |
| `PATCH /api/v1/announcements/{id}`                         | operator | save text without generating          |
| `POST /api/v1/announcements/{id}/generate`                 | operator | generate or regenerate                |
| `PUT /api/v1/extensions/{id}/voicemail/greeting`           | operator | greeting from an announcement or text |
| `POST /api/v1/extensions/{id}/voicemail/greeting/preview`  | operator | the default spoken greeting, as WAV   |
| `DELETE /api/v1/extensions/{id}/voicemail/greeting/{slot}` | operator | revert a slot to the default          |

The routes that hold the key or spend credits are left out of the MCP tools.
