# The computer reaches its own relay here

2026-11-16. A recording part (2.8 MB) reached the relay from the phone, and the
computer then failed to fetch it: the relay ran on the same computer, the
computer asked for the upload through the relay's public tunnel, and the tunnel
answered 504 after 60 seconds. The phone was told "not yet" and nothing was
lost, but nothing arrived either.

Two rules since then (`wails-gui/mobile_relay_local.go`):

1. **A relay on this computer is reached on this computer.** With
   `ISOFORM_RELAY_LOCAL_URL=http://127.0.0.1:<port>` set, everything the
   computer asks of the relay goes to that address: its connection, a phone's
   uploads, attachments, the health check. Only plain http on `127.0.0.1` or
   `localhost` is accepted, so the computer's credential cannot be sent to
   another machine by this setting. The address phones are given in a pairing
   invitation stays the public one (`ISOFORM_RELAY_URL`).
   With Isoform's own remote access the relay address is already loopback and
   the setting is not needed.
2. **A download from the relay is tried again and goes on from where it
   stopped.** A 5xx, 429, a refused connection or a cut body is followed by
   another request after 1 s, 2 s, 4 s, up to 15 s apart, for as long as the
   phone's request is given (two minutes). A later request asks only for the
   missing bytes (`Range` with `If-Range`), and one request may run 40 seconds.
   When the time is over the phone still reads 503, which it does not count as
   a refusal; its own retry takes over.

A missing upload (404) ends at once, as before. The part's checksum is checked
after the download, as before.

Not covered: an upload larger than what two minutes can carry on a slow link
still ends in 503 and starts from zero on the phone's next try (what was read
is not kept between requests from the phone).
