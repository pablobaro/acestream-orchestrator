# Stream Loop Detection

Detects live streams whose engine has stopped following the broadcast and is
replaying data it already holds, then stops them and refuses to restart them
until the source recovers.

## Why it exists

A live AceStream engine reports `live_last`: the wall-clock timestamp of the
newest data it holds. When the broadcast stops feeding it, the engine does not
fail. It keeps serving what it already has, so playback continues — on old
content, with no error anywhere. Viewers see a channel that is stuck and
running backwards in time.

Nothing else catches this. The engine is healthy, the tunnel is up, bytes are
flowing, and the proxy is happily forwarding them. Only `live_last` says the
stream left the present behind.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `STREAM_LOOP_DETECTION_ENABLED` | `false` | Enable detection. Off by default: a badly chosen threshold takes working channels off the air. |
| `STREAM_LOOP_DETECTION_THRESHOLD_S` | `3600` | How far `live_last` may fall behind now before the stream counts as looping. |
| `STREAM_LOOP_RETENTION_MINUTES` | `0` | How long a detected stream stays blocked. `0` keeps it until cleared by hand. |

There is no separate check interval: the check runs on the existing per-stream
stat tick, so detection costs nothing extra and needs no tuning.

### Choosing a threshold

- Live sport or events: 30–60 minutes, to take a stale feed off the air quickly.
- General streaming: 1–2 hours, to avoid blocking a channel over a rough patch.

Too low and a channel that recovers on its own gets blocked anyway. The
threshold is the only guard against that, so start high and tighten it.

## Behaviour

1. Every stat tick, a live stream's `live_last` is compared against the clock.
2. Past the threshold, the stream is marked, an event is recorded, and the
   stream is stopped.
3. While it is marked, requests for it are refused with
   `503 Service Unavailable` before any engine is allocated.

Step 3 is the point of the mark. Stopping alone achieves nothing: the next
client request would allocate another engine and drop the viewer straight back
into the same loop.

Streams reporting `is_live=0` are never checked — VOD has no live edge to fall
behind.

## API

### `GET /api/v1/looping-streams`

```json
{
  "stream_ids": ["content_id_1", "content_id_2"],
  "streams": {
    "content_id_1": "2026-01-08T12:00:00Z",
    "content_id_2": "2026-01-08T12:05:00Z"
  },
  "retention_minutes": 0,
  "enabled": true,
  "threshold_seconds": 3600
}
```

### `DELETE /api/v1/looping-streams/{id}`

Clears one mark so the stream can be played again. Requires an API key.
Returns `404` if the stream was not marked.

### `POST /api/v1/looping-streams/clear`

Clears every mark. Requires an API key.

```bash
curl "http://orchestrator:8000/api/v1/looping-streams"

curl -X DELETE "http://orchestrator:8000/api/v1/looping-streams/STREAM_ID" \
  -H "X-API-KEY: $API_KEY"
```

## Implementation

- `internal/proxy/stream/loopdetect.go` — the lag test and the tracker.
- `internal/proxy/stream/manager.go` — `checkLiveLag`, run from the stat loop.
- `internal/api/proxy.go` — `rejectIfLooping`, the gate ahead of engine selection.
- `internal/api/management.go` — the three endpoints above.

`live_last` reaches these from `internal/proxy/aceapi`, which parses the
engine's `livepos` event.

## Troubleshooting

**A live channel got marked.** Clear it with the DELETE endpoint and raise the
threshold. Check the recorded event for the measured lag — that number tells
you how far off the threshold was.

**Nothing is ever detected.** Confirm `STREAM_LOOP_DETECTION_ENABLED=true`, that
the stream reports `is_live=1`, and that `live_last` is present in
`GET /api/v1/streams/{id}/livepos`. An engine that never reports a live position
is never checked.

**A channel stays blocked after the source comes back.** With
`STREAM_LOOP_RETENTION_MINUTES=0` that is the configured behaviour. Clear it by
hand, or set a retention window so marks expire on their own.

## Not implemented

Configuration is read from the environment at startup. There is no endpoint or
panel page for changing these values at runtime — an earlier revision of this
document described one that does not exist.
