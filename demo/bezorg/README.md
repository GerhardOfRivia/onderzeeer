# bezorg to onderzeeer

Bezorg writes each payload next to its final name with a `.bezorg-partial`
suffix, renames it into place when the transfer checks out, and then writes one
`<name>.bezorg-done.json` manifest for the whole job:

```json
{
  "version": 1,
  "job_id": "4f2a91c0b7e3",
  "external_id": "batch-7",
  "remote": "/outgoing/scene",
  "completed_at": "2026-11-23T12:00:00Z",
  "files": [{"path": "scene.tif", "size": 6, "sha256": "..."}]
}
```

`path` is relative to the manifest. `onderzeeer.yaml` in this directory watches
for `*.bezorg-done.json`, checks the listed files, and stores `external_id` on
that one job. Payload files and `*.bezorg-partial` do not start jobs.

```bash
bezorg fetch --config /etc/bezorg/incoming.yaml
onderzeeerd --web-listen 127.0.0.1:8080
onderzeeer start demo/bezorg/onderzeeer.yaml incoming
```

Agents follow `GET /api/v1/queues/{queueID}/events` or a webhook for
`job.succeeded` and `job.failed`. The curl examples and the signature check are
in the main README under **bezorg and agents**.
