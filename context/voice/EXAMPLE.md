---
platform: linkedin
date: 2026-05-14
note: example only — replace with one of your actual posts
---

Cloudflare's edge outage yesterday was a reminder that CDN architecture has
a single point of failure nobody talks about: the control plane. When their
routing decisions go bad, 12% of requests start returning 522s — not because
the origin is down, but because the edge doesn't know where to send them.

The fix they shipped in under two hours was a circuit breaker on the routing
layer. It's the kind of thing that sounds obvious in retrospect and would have
required a redesign if it hadn't been built into the original architecture.

Most coverage focused on the impact. The interesting part is how fast they
diagnosed: 18 minutes from first alert to root cause, which is faster than
most teams take to open an incident ticket.
