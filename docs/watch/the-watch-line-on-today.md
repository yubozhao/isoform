# Today's watch line

Design: phone-watch v1.1.0 rules 18 and 19, phone-duo v1.0.0 rule 12, phone-watch v1.2.0 rule 7
(all in `docs/handoff-2026-11-18/`).

One line under Today's date, scrolling with the page, never pinned. It opens the Watch tab. It
is always there when a watch is paired (`WatchTodayLineText`, `WatchTodayLine`):

- plain: "Your watch is listening · every hour is on your computer";
- plain: "6 hours from your watch are going to your computer", while they are going;
- amber: "Your watch isn't listening · since 9:40 AM";
- amber: "25 hours from your watch are waiting on this phone", only when they cannot go (the last
  send did not get through and 30 minutes have passed);
- amber: "1 hour from your watch couldn't be sent";
- amber, before all the others: "Closed Eyes until 12:04 PM · nothing is seen or heard", or
  "… until you end it …". With no watch paired the line shows only during Closed Eyes.

When two are true, the one that stops listening comes first. The wearer's own stop for the day
reads "Your watch isn't listening · since …" in plain; design's table has no line of its own for
it, so this is a reading of rule 18 that design may correct. A watch that has not said what it
is doing gets no line.

Before, the line was pinned in the scroll's top inset and showed only when something was wrong;
on the wide phone it covered what scrolled under it.

On the wide phone the line belongs in Today's left pane under the date (rule 19). That pane is
built by the Today work; until then the line is in Today's page only.

Guards: `testTodaysWatchLine` in `ios/YansuTests/WatchWhereTests.swift`;
`testWideTodayWatchLineSitsUnderTheDateAndScrollsWithThePage` in `DuoWideUITests`.
