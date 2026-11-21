# o1 — a short writeup

o1 — https://o1.work.gd

What: an uptime monitor for small teams. It probes HTTP endpoints, opens and resolves incidents,
alerts over email, webhooks, Slack and Telegram, and publishes a public status page per
workspace. Go and PostgreSQL behind, React in front; live.

Why: I wanted to own a product, not a ticket queue, and picked a problem I could feel — teams
learn their site is down from their customers.

The decisions were mine. With budget for one more surface I chose the public status page over
billing: users asked "is it down?" far more than "how much?". Billing came a month later and
nobody minded.

The tradeoff worth naming is retention. Deleting raw checks is irreversible, so a partition drops
only when hourly rollups cover it and the counts match; otherwise the job defers. I spent days on
a guard for something invisible, because its failure mode is silent.

Demo: demo-viewer@o1.work.gd / o1-demo-viewer-2026 (read-only).
