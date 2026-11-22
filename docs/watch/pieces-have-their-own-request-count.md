# Pieces have their own request count on the relay

2026-11-19. Relay only.

## What happened

Bo opened phone build 20261119040114 at 08:15. The phone sent parts in 256 KiB pieces for the first
time: 4 parts at once, 11 pieces each. In 3.5 minutes one part reached the computer. Each piece was
sent 3 to 4 times, and the tunnel log showed 24 requests the phone cancelled.

## Why

The relay let one device make 60 authenticated requests a minute, all routes together. A part is
11 requests now instead of 1, and the app makes about 50 requests of its own when it opens, so the
minute was used up in seconds. From then on the relay answered 429 to the remaining pieces and to
the app's briefs and chat. The phone takes a refused piece as a refusal of the part and stops the
part's other pieces. The relay does not log statuses, so the 429s are read from the code and the
request counts, not seen.

Two hourly counts were also too low for a backlog: 60 uploads an hour (Bo had over 100 parts
waiting) and 4000 pieces an hour.

## The rule now

- `/v1/uploads/...` (whole uploads, pieces, progress) count in their own budget: 1200 a minute per
  device. They never use the 60 a minute the other routes share.
- 600 uploads an hour and 16000 pieces an hour per device.
- What the relay keeps is still bounded by bytes: 48 waiting uploads per device and the store's
  byte quotas. Those are unchanged.

## Guard

`TestUploadPiecesDoNotUseUpTheDeviceRequestBudget`: 110 pieces in one minute all store, and the
device's other routes still get their 60.

## To take effect

The pinned relay on the computer has to be rebuilt and restarted, and the desktop rebuilt for the
relay it carries. No phone build is needed for this part.
