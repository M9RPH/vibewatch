# Vibewatch integration test lab

The v0.9 reliability suite complements Go unit tests with real Docker lifecycle regressions.

`make test-integration` builds a dependency-free scratch fixture and verifies:

- health warm-up (`503 -> 200`) so Docker/custom-health grace changes can be regression tested;
- the concrete `network_mode: container:<id>` lifecycle that previously broke Gluetun-style sidecars;
- explicit dependent recreation binds to the newly recreated parent container ID.

`sudo make test-netem` is optional and Linux-only. It creates two temporary network namespaces and applies `tc netem` delay of about 50 ms RTT without modifying normal host interfaces. Set `VIBEWATCH_NETEM_LOSS=1%` (or another netem value) to add packet loss.

These tests intentionally exercise Docker/network primitives without requiring a production registry or touching real Vibewatch-managed containers. They are safe to run on a dedicated development/CI Docker daemon. A future full end-to-end suite can layer the authenticated Vibewatch HTTP API and disposable Watchtower worker on top of these fixtures.
