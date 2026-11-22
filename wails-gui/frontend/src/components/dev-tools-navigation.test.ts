import { describe, expect, it } from "vitest";

import enSettings from "@/i18n/locales/en/settings.json";
import zhSettings from "@/i18n/locales/zh-CN/settings.json";
import {
    DEV_TOOLS_GROUPS,
    DEV_TOOLS_TOPIC_IDS,
} from "@/components/dev-tools-navigation";
import { SETTINGS_PAGE_IDS } from "@/components/settings/settings-registry";

const expectedLabels = [
    "Activity processing",
    "Audio capture",
    "App reading",
    "Learning decisions",
    "Priorities",
    "Memory and search",
    "Browser preview",
    "Notification checks",
    "App health",
];

const expectedGroupLabels = [
    "Capture",
    "Learning",
    "Testing",
    "Troubleshooting",
];

const expectedChineseLabels = [
    "活动处理",
    "音频采集",
    "应用读取",
    "学习决策",
    "优先事项",
    "记忆与搜索",
    "浏览器预览",
    "通知检查",
    "应用运行状态",
];

const expectedChineseGroupLabels = [
    "采集",
    "学习",
    "测试",
    "故障排查",
];

describe("Developer tools content", () => {
    it("groups every destination by the diagnostic job it supports", () => {
        expect(DEV_TOOLS_GROUPS.map((group) => group.id)).toEqual([
            "inputs",
            "learning",
            "testing",
            "health",
        ]);
        expect(DEV_TOOLS_TOPIC_IDS).toHaveLength(9);
        expect(new Set(DEV_TOOLS_TOPIC_IDS).size).toBe(DEV_TOOLS_TOPIC_IDS.length);
        expect(DEV_TOOLS_TOPIC_IDS as string[]).not.toContain("agency-runtime");
        expect(DEV_TOOLS_TOPIC_IDS as string[]).not.toContain("handoff-monitor");
        expect(DEV_TOOLS_TOPIC_IDS as string[]).not.toContain("shadow-agent");
        expect(enSettings).not.toHaveProperty("handoffMonitor");
        expect(zhSettings).not.toHaveProperty("handoffMonitor");
        // Onboarding moved to Help and about and Learning and memory, and
        // Chat context to General and Learning and memory; the notification
        // test page must not take the user Notifications id.
        expect(DEV_TOOLS_TOPIC_IDS as string[]).not.toContain("onboarding");
        expect(DEV_TOOLS_TOPIC_IDS as string[]).not.toContain("debug");
        expect(DEV_TOOLS_TOPIC_IDS as string[]).not.toContain("notifications");
        for (const id of DEV_TOOLS_TOPIC_IDS) {
            expect(SETTINGS_PAGE_IDS as readonly string[]).not.toContain(id);
        }
    });

    it("uses short, familiar English labels instead of subsystem names", () => {
        const topics = enSettings.topics as Record<string, string>;
        const labels = DEV_TOOLS_TOPIC_IDS.map((id) => topics[id]);

        expect(labels).toEqual(expectedLabels);
        expect(new Set(labels).size).toBe(labels.length);
        for (const retiredLabel of [
            "Triage Log",
            "Agency Runtime",
            "Purpose Map",
            "AX Parser & OCR",
            "Perf Analysis",
            "Handoff Monitor",
            "Handoffs",
            "Activity Pipeline",
            "Shadow Agent",
            "Browser Bridge",
            "Browser access",
            "Chat context",
        ]) {
            expect(labels).not.toContain(retiredLabel);
        }
        expect(labels.join(" ")).not.toMatch(
            /\b(?:pipeline|runtime|parser|monitor|qualification|correlation|evidence|runs)\b/i,
        );
    });

    it("uses concrete operator jobs for the group labels", () => {
        const groups = enSettings.devTools.groups as Record<string, string>;
        expect(DEV_TOOLS_GROUPS.map((group) => groups[group.id])).toEqual(
            expectedGroupLabels,
        );
    });

    it("keeps the canonical product terms aligned across navigation and pages", () => {
        expect(enSettings.topics).not.toHaveProperty("working-boundaries");
        expect(enSettings.topics).not.toHaveProperty("agency-runtime");
        expect(enSettings.activityTriage.shadowTitle).toBe(
            "Learn by comparison",
        );
        expect(enSettings.shadowDebug.title).toBe("Shadow learning");
        expect(enSettings.shadowDebug.question).toBe(
            "How is Isoform learning from your work?",
        );
        expect(enSettings.triage.title).toBe(
            "Why did Isoform use or skip this activity?",
        );
        expect(enSettings.triage.decisions).toMatchObject({
            triage: "Reviewed",
            saved: "Saved",
            dedup_update: "Updated",
            dedup_skip: "Already known",
            error: "Failed",
        });
        expect(enSettings.aiProvider.title).toBe("AI model");
        expect(enSettings.aiProvider.providerLabel).toBe("Model source");
        expect(enSettings.cursorOverlay.rowTitle).toBe(
            "Show Isoform's cursor while it works",
        );
        expect(enSettings.activityPipeline.title).toBe(
            "What happened to observed activity?",
        );
        expect(enSettings.activityPipeline).toMatchObject({
            runNow: "Check pending activity",
            clear: "Clear activity-flow history",
            stats: {
                title: "Which frames were used?",
                range: {
                    "24h": "Past 24 hours",
                    "7d": "Past 7 days",
                    all: "All loaded records",
                },
            },
        });
        expect(enSettings.notifications.cardTitle).toBe("Notification checks");
        expect(enSettings.perfAnalysis.cardTitle).toBe(
            "Is Isoform running smoothly?",
        );
        expect(enSettings.axParser.cardTitle).toBe(
            "What can Isoform read from this app?",
        );
        expect(enSettings.axParser).toMatchObject({
            matchedAppLabel: "Reading method",
            noMatchedApp: "No reading method matched",
            bodyLabel: "Text provided directly by the app",
            catalogSectionTitle: "Supported apps and websites",
            catalogColParser: "Reading method",
            unsupportedSectionTitle: "Tested but not readable",
        });
        // Chat context left Developer tools: the preview is on Learning and
        // memory, and Show tool steps went (answers v1.0.0 T2).
        expect(enSettings.topics).not.toHaveProperty("debug");
        expect(enSettings.debug).not.toHaveProperty("toolRawTitle");
        expect(enSettings.general).not.toHaveProperty("toolSteps");
        expect(enSettings.debug.memoryContext).toMatchObject({
            title: "Preview chat context",
            queryLabel: "Chat message",
            inspect: "Preview context",
            selectedTitle: "Context selected for this message",
        });
        // Run setup again lives on Help and about.
        expect(enSettings.onboarding.title).toBe("Run setup again");
        expect(enSettings.shadowDebug.tabs).toMatchObject({
            runs: "Private practice",
            taskEpisodes: "Observed tasks",
            observer: "Task detection",
            profiles: "Prediction accuracy",
            qualifications: "Suggestion readiness",
            selfModel: "Supported insights",
            attempts: "Comparisons",
        });
        expect(enSettings.shadowDebug.groups).toEqual({
            shadowWork: "Task timeline",
            learning: "Learning quality",
            sources: "Learning sources",
        });
        expect(enSettings.shadowDebug.stats).toMatchObject({
            runs: "Private practice",
            profiles: "Task types compared",
            ready: "Accuracy targets met",
            errors: "Practice errors",
        });
        expect(enSettings.shadowDebug.selectRun).toBe(
            "Select a practice run to see Isoform's prediction and private result.",
        );

        const memoryContextLabels = JSON.stringify(enSettings.debug.memoryContext);
        expect(memoryContextLabels).not.toMatch(
            /\b(?:top-N|candidate corpus|retrieval pipeline|selection resolver|context assembler)\b/i,
        );
        expect([
            enSettings.axParser.matchedAppLabel,
            enSettings.axParser.noMatchedApp,
            enSettings.axParser.catalogSectionTitle,
            enSettings.axParser.catalogColParser,
        ].join(" ")).not.toMatch(/\b(?:parser|probe|catalog)\b/i);

        const primaryShadowLabels = [
            ...Object.values(enSettings.shadowDebug.groups),
            ...Object.values(enSettings.shadowDebug.tabs),
        ];
        expect(primaryShadowLabels.join(" ")).not.toMatch(
            /\b(?:episode|observer|shadow run|correlation|qualification|self model|self dream|first run|background understanding|capability profile|prediction quality|learned profile|result matching|detection checks)\b/i,
        );
    });

    it("keeps the Chinese navigation and Shadow learning hierarchy parallel", () => {
        const topics = zhSettings.topics as Record<string, string>;
        const groups = zhSettings.devTools.groups as Record<string, string>;

        expect(DEV_TOOLS_TOPIC_IDS.map((id) => topics[id])).toEqual(
            expectedChineseLabels,
        );
        expect(DEV_TOOLS_GROUPS.map((group) => groups[group.id])).toEqual(
            expectedChineseGroupLabels,
        );
        expect(zhSettings.activityPipeline).toMatchObject({
            runNow: "检查待处理活动",
            clear: "清空活动流记录",
            stats: {
                title: "使用了哪些画面？",
                range: {
                    "24h": "过去 24 小时",
                    "7d": "过去 7 天",
                    all: "全部已加载记录",
                },
            },
        });
        expect(zhSettings.notifications.cardTitle).toBe("通知检查");
        expect(zhSettings.topics).not.toHaveProperty("working-boundaries");
        expect(zhSettings.topics).not.toHaveProperty("agency-runtime");
        expect(zhSettings.activityTriage.shadowTitle).toBe("通过对比学习");
        expect(zhSettings.activityPipeline.title).toBe(
            "观察到的活动后来发生了什么？",
        );
        expect(zhSettings.onboarding.title).toBe("重新进行初始设置");
        expect(zhSettings.triage.decisions).toMatchObject({
            triage: "已检查",
            saved: "已保存",
            dedup_update: "已更新",
            dedup_skip: "已有记录",
            error: "失败",
        });
        expect(zhSettings.aiProvider.title).toBe("AI 模型");
        expect(zhSettings.aiProvider.providerLabel).toBe("模型来源");
        expect(zhSettings.topics).not.toHaveProperty("debug");
        expect(zhSettings.axParser).toMatchObject({
            matchedAppLabel: "读取方式",
            noMatchedApp: "没有匹配的读取方式",
            bodyLabel: "应用直接提供的文字",
            catalogSectionTitle: "支持的应用和网站",
            catalogColParser: "读取方式",
            unsupportedSectionTitle: "已测试但无法读取",
        });
        expect(zhSettings.shadowDebug.tabs).toMatchObject({
            runs: "私下练习",
            taskEpisodes: "观察到的任务",
            observer: "任务识别",
            profiles: "预测准确度",
            qualifications: "建议可靠度",
            selfModel: "有依据的理解",
            attempts: "对比",
        });
        expect(zhSettings.shadowDebug.groups).toEqual({
            shadowWork: "任务时间线",
            learning: "学习质量",
            sources: "学习来源",
        });
        expect(zhSettings.shadowDebug.question).toBe(
            "Isoform 如何从你的工作中学习？",
        );
        expect(zhSettings.shadowDebug.stats).toMatchObject({
            runs: "私下练习",
            profiles: "已对比的任务类型",
            ready: "已达到准确度目标",
            errors: "练习错误",
        });
        expect(zhSettings.shadowDebug.selectRun).toBe(
            "请选择一次练习，查看 Isoform 的预测和私下结果。",
        );
        expect(zhSettings.debug.memoryContext).toMatchObject({
            title: "预览聊天上下文",
            queryLabel: "聊天消息",
            inspect: "预览上下文",
            selectedTitle: "为这条消息选中的上下文",
        });
    });
});
