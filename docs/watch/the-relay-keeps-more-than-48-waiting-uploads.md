# The relay keeps more than 48 waiting uploads for a phone (2026-11-22)

## What happened

On Nov 22 at 11:47 Bo's phone came back after 16.5 hours with about 255 parts waiting. In two
minutes of wake 6 parts got through and 53 pieces of 11 other parts were refused, every one
with `limit=waiting_uploads count=48 cap=48`.

A phone could have 48 uploads on the relay at once: whole ones the computer has not fetched and
ones that have begun and are not whole. Each keeps its place for 48 hours. The evening before,
each wake handed 14 to 15 parts to iOS, and then the phone went silent. Those half-sent uploads
kept their places. The phone asked the relay about 14 uploads at 11:47 and got 404 for all of
them: the places were held by other uploads than the ones it was starting.

## What changed (relay)

`KeptUploads` is 512 instead of 48. The bytes stay the real bound: 192 MiB for one phone, about
200 parts of a watch recording. The count now only keeps a flood of tiny uploads out. Nothing
waiting is evicted, as before.

## What is left

The phone still ends its pass at the first "relay full" answer, and starts new parts before
finishing the ones it has begun. That is the phone PR that follows: begun parts first, and a
full relay stops only the starting of new ones.
