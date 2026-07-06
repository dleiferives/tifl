# TIFL audio service

This is the standalone audio manager service for TTS generation. It exposes an
OpenAI-style speech endpoint and routes requests to configured providers. This
first slice ships one local provider: `espeak-ng`, with `ffmpeg` encoding WAV
output to MP3.

Install runtime tools:

```bash
brew install espeak-ng ffmpeg
```

or on Debian/Ubuntu:

```bash
sudo apt-get install espeak-ng ffmpeg
```

Run it:

```bash
go run ./audio/cmd/audio
```

Build it:

```bash
make audio
```

Generate speech:

```bash
curl -sS http://127.0.0.1:8010/v1/audio/speech \
  -H 'Content-Type: application/json' \
  -d '{"model":"tts-1","input":"hello from TIFL","voice":"en-us","response_format":"mp3"}' \
  --output /tmp/tifl-tts.mp3
```

List available eSpeak voices:

```bash
curl -sS 'http://127.0.0.1:8010/v1/audio/voices?language=en'
```

Useful environment variables:

| variable | default | meaning |
| --- | --- | --- |
| `AUDIO_ADDR` | `127.0.0.1:8010` | listen address |
| `AUDIO_API_KEY` | empty | optional bearer token required for generation/listing |
| `AUDIO_MAX_CONCURRENCY` | `2` | max concurrent synthesis processes |
| `AUDIO_REQUEST_TIMEOUT_SECONDS` | `30` | per-request synthesis timeout |
| `AUDIO_MAX_INPUT_CHARS` | `5000` | max request input length |
| `AUDIO_ESPEAK_PATH` | `espeak-ng` | eSpeak binary path |
| `AUDIO_ESPEAK_DEFAULT_VOICE` | `en` | fallback eSpeak voice |
| `AUDIO_FFMPEG_PATH` | `ffmpeg` | ffmpeg binary path |
| `AUDIO_MP3_BITRATE` | `48k` | MP3 bitrate |
| `AUDIO_DEFAULT_PROVIDER` | `espeak-ng` | provider used for `auto`, `tts-1`, and blank model |
