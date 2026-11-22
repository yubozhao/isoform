# A refused request leaves a line on the relay

2026-11-20. On Bo's phone a pass ended with the word `part-relay-403`. The pinned relay's log had no 403 in that minute and the computer's own relay had no line from the phone all day, so nothing could say which hop had answered: the relay, or the tunnel in front of it.

The relay now writes one line whenever it answers a device 401 or 403:

    refused POST /v1/requests: Forbidden (403), Device lacks the required scope

- It holds the method, the route, the status and the relay's own fixed reason. Nothing of the credential, the device or the body.
- A route that ends in an id is written without it (`/v1/uploads/...`). A path that is not a plain lower-case route is written as `another route`.
- It covers the browser-origin refusal, a missing or unknown device credential, a missing scope, an envelope for another device, and the device routes that answer 401 when the device is gone.

How to read it: a phone that reports 401 or 403 while the relay wrote no `refused` line was answered by something in front of the relay.

Code: `relay/internal/httpapi/upload_log.go` (`refuse`, `refusalLine`, `routeName`).
