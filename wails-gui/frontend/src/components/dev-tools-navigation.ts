// Developer tools pages, grouped by the diagnostic job they support. The
// group sits below Help and about in the Settings sidebar and only shows
// while Developer tools are on: by default in staging and dev builds, and
// Cmd/Ctrl+Shift+Option+D turns them on or off in any build.
//
// Nothing a user needs lives here. Run setup again is on About and Import
// memories on You › About you. Memory and search holds the chat context and
// daily topics previews and the search index, from Learning and memory's
// Advanced (settings batch D3).
//
// Ids must not collide with user-facing Settings page ids
// (settings-registry.ts): the notification test page is "notification-tests"
// because "notifications" is the user Notifications page.
export const DEV_TOOLS_GROUPS = [
    {
        id: "inputs",
        topics: ["activity-pipeline", "audio-capture", "ax-parser"],
    },
    {
        id: "learning",
        topics: ["triage", "purpose-map", "memory-debug"],
    },
    {
        id: "testing",
        topics: ["browser-bridge", "notification-tests"],
    },
    {
        id: "health",
        topics: ["perf-analysis"],
    },
] as const;

export type DevToolsGroupId = (typeof DEV_TOOLS_GROUPS)[number]["id"];
export type DevToolsTopicId = (typeof DEV_TOOLS_GROUPS)[number]["topics"][number];

export const DEV_TOOLS_TOPIC_IDS: DevToolsTopicId[] = DEV_TOOLS_GROUPS.flatMap(
    (group) => [...group.topics],
);
