# Language: transcription hints and output language

Two independent, per-job-overridable language settings.

## Transcription language hint

`Job.TranscribeLanguageHint` (request field `languageHint`, server default
`TRANSCRIBE_LANGUAGE_HINT` env, default `"en,sl"`) biases Whisper toward the
right language instead of guessing across everything it knows.

Whisper's OpenAI-compatible endpoint takes a single-value `language` field —
it doesn't support "one of these N languages", only "force this exact one".
So `transcribeOnce` (`backend/internal/analysis/litellm.go`) branches on the
shape of the hint:

- **Exactly one language** (e.g. `"sl"`): sent as the `language` field,
  forcing Whisper to decode into that language.
- **A candidate list** (the default `"en,sl"`, or any comma-separated list):
  there's no multi-language `language` value, so instead a `prompt` field is
  sent — "The speech may be in English or Slovenian." — Whisper's documented
  mechanism for softly steering language detection without forcing it. This
  is a real, non-obvious API constraint worth remembering if you're touching
  this code: sending a comma-joined string as `language` doesn't work, and
  there's no error telling you why not.
- **No hint**: neither field is sent — fully open auto-detect.

## Output language

`Job.OutputLanguage` (request field `outputLanguage`, server default
`OUTPUT_LANGUAGE` env, default `"sl"`) controls the language the VLM writes
*generated* text in: the overview story, every segment's description, and
chat replies.

A small code→name map feeds one added line — "Respond in {language}." — into
each of the three LiteLLM system prompts (`overviewSystemPrompt`, the
`AnalyzeSegment` prompt, `chatSystemPrompt`). This line is phrased so only
the natural-language *values* the VLM writes translate — the JSON *keys* in
`AnalyzeOverview`'s response contract (`{"story": ..., "segments": [...]}`)
stay structurally English, since the backend parses those keys literally.

## Defaults and overrides

Both settings default from server-wide env vars
(`TRANSCRIBE_LANGUAGE_HINT="en,sl"`, `OUTPUT_LANGUAGE="sl"` — see
[configuration.md](configuration.md)) and can be overridden per job at create
time via `languageHint`/`outputLanguage` on either `CreateJobRequest` or
`CreateJobFromImmichRequest`. The frontend exposes this as two small controls
next to the file picker / Immich import box: "Transcribe: Auto (English/
Slovenian) | English only | Slovenian only" and "Reply in: Slovenian |
English".
