import {
    act,
    fireEvent,
    render,
    screen,
    waitFor,
    within,
} from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { ModelsPanel } from "./models-panel";
import { SettingsDevToolsContext } from "@/components/settings/settings-dev-tools";
import { DownloadProgressLine } from "./model-download-progress";
import {
    modelChoicesFixture,
    modelPlanFixture,
    fixtureModels,
} from "./setup-fixtures";
import type { ModelChoices, ModelDownloadPlan } from "@/lib/setup-backend";
const mocks = vi.hoisted(() => ({
    network: vi.fn(),
    resume: vi.fn(),
    choices: vi.fn(),
    jobs: vi.fn(),
    plan: vi.fn(),
    start: vi.fn(),
    choice: vi.fn(),
    remove: vi.fn(),
    retry: vi.fn(),
    events: new Map<string, () => void>(),
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetModelChoices: mocks.choices,
    GetModelNetworkState: mocks.network,
    ResumeModelDownload: mocks.resume,
    GetModelDownloads: mocks.jobs,
    PlanModelDownload: mocks.plan,
    StartModelDownloads: mocks.start,
    SetModelChoice: mocks.choice,
    RemoveModel: mocks.remove,
    RetryModelDownload: mocks.retry,
}));
vi.mock("../../../wailsjs/runtime/runtime", () => ({
    EventsOn: (key: string, fn: () => void) => {
        mocks.events.set(key, fn);
        return () => mocks.events.delete(key);
    },
}));
let choices: ModelChoices;
let plan: ModelDownloadPlan;
beforeEach(async () => {
    await i18n.changeLanguage("en");
    vi.resetAllMocks();
    Element.prototype.scrollIntoView = vi.fn();
    choices = modelChoicesFixture();
    plan = modelPlanFixture(choices);
    mocks.events.clear();
    mocks.choices.mockImplementation(async () => structuredClone(choices));
    mocks.jobs.mockResolvedValue([]);
    mocks.network.mockResolvedValue({
        known: true,
        offline: false,
        expensive: false,
        constrained: false,
    });
    mocks.resume.mockResolvedValue(undefined);
    mocks.plan.mockImplementation(async () => plan);
    mocks.start.mockResolvedValue(undefined);
    mocks.remove.mockResolvedValue(undefined);
    mocks.retry.mockResolvedValue(undefined);
    mocks.choice.mockImplementation(async (job: "live", id: string) => {
        choices = {
            ...choices,
            jobs: choices.jobs.map((row) =>
                row.job === job ? { ...row, chosenId: id } : row,
            ),
        };
        return choices;
    });
});
async function openSheet() {
    await waitFor(() =>
        expect(screen.getByTestId("onboarding-continue")).not.toBeDisabled(),
    );
    fireEvent.click(screen.getByTestId("onboarding-continue"));
    return screen.findByRole("dialog");
}
describe("shared setup and Settings models", () => {
    it("uses the plan total and lists all six items before starting anything", async () => {
        plan.totalBytes = 123_000_000;
        const next = vi.fn();
        render(<ModelsPanel onNext={next} />);
        await waitFor(() =>
            expect(screen.getByTestId("onboarding-continue")).toHaveTextContent(
                "117 MB",
            ),
        );
        const sheet = await openSheet();
        expect(
            within(sheet).getByText("Download 6 models"),
        ).toBeInTheDocument();
        expect(within(sheet).getAllByRole("combobox")).toHaveLength(6);
        expect(mocks.start).not.toHaveBeenCalled();
        fireEvent.click(
            within(sheet).getByRole("button", { name: "Download 117 MB" }),
        );
        await waitFor(() => expect(mocks.start).toHaveBeenCalledOnce());
        expect(next).toHaveBeenCalledOnce();
    });
    it("blocks confirmation when the plan reports insufficient space", async () => {
        plan.enoughSpace = false;
        plan.freeBytes = 100;
        render(<ModelsPanel onNext={vi.fn()} />);
        const sheet = await openSheet();
        expect(within(sheet).getByRole("alert")).toHaveTextContent(
            "Not enough space",
        );
        expect(
            within(sheet).getByRole("button", { name: /^Download [0-9]/ }),
        ).toBeDisabled();
        expect(mocks.start).not.toHaveBeenCalled();
    });
    it("requires the explicit non-commercial confirmation and keeps source choices scoped to this download", async () => {
        plan.items[0].nonCommercial = true;
        plan.items[0].licence = {
            ...plan.items[0].licence,
            name: "CC BY-NC",
            nonCommercial: true,
        };
        render(<ModelsPanel onNext={vi.fn()} />);
        const sheet = await openSheet();
        expect(
            within(sheet).getByText(/SenseVoice uses CC BY-NC/),
        ).toBeInTheDocument();
        fireEvent.click(
            within(sheet).getByRole("button", {
                name: /^I understand, download/,
            }),
        );
        await waitFor(() => expect(mocks.start).toHaveBeenCalledWith(plan));
    });
    it("expands alternatives with evidence and changes the persisted selection", async () => {
        render(<ModelsPanel onNext={vi.fn()} />);
        fireEvent.click(
            await screen.findByRole("button", {
                name: "Change Live, while you talk",
            }),
        );
        expect(
            screen.getByText(
                "Tests: 4.8% errors in English, 7.1% in Chinese · 20× real time",
            ),
        ).toBeInTheDocument();
        fireEvent.click(screen.getByRole("radio", { name: "Parakeet TDT v2" }));
        await waitFor(() =>
            expect(mocks.choice).toHaveBeenCalledWith("live", "parakeet"),
        );
    });
    it("run again and Settings agree about installed items and Remove uses the item id", async () => {
        fixtureModels(choices).forEach((model) => {
            model.downloaded = true;
        });
        const { unmount } = render(<ModelsPanel onNext={vi.fn()} />);
        await screen.findAllByText("Downloaded");
        expect(screen.getByTestId("onboarding-continue")).toHaveTextContent(
            "Continue",
        );
        expect(screen.getAllByText("Downloaded")).toHaveLength(3);
        unmount();
        render(<ModelsPanel settings />);
        expect(await screen.findAllByText("Downloaded")).toHaveLength(6);
        expect(within(screen.getByTestId("model-job-live")).queryByRole("button", { name: /Change|Remove/ })).not.toBeInTheDocument();
        expect(mocks.remove).not.toHaveBeenCalled();
        expect(screen.getByText(choices.modelsFolder)).toBeInTheDocument();
    });
    it("refreshes download state from backend events", async () => {
        render(<ModelsPanel onNext={vi.fn()} />);
        await screen.findByText("SenseVoice");
        fixtureModels(choices).forEach((model) => {
            model.downloaded = true;
        });
        await act(async () => {
            mocks.events.get("models:downloads")?.();
        });
        expect(await screen.findAllByText("Downloaded")).toHaveLength(3);
    });
});
describe("download footer", () => {
    it("shows exact progress, failures with Retry, and disappears silently when complete", async () => {
        const job = {
            modelId: "sensevoice",
            name: "SenseVoice",
            state: "downloading" as const,
            downloadedBytes: 41,
            totalBytes: 100,
        };
        const { rerender } = render(<DownloadProgressLine downloads={[job]} />);
        expect(screen.getByRole("status")).toHaveTextContent(
            "Downloading SenseVoice 41%",
        );
        rerender(
            <DownloadProgressLine downloads={[{ ...job, state: "failed" }]} />,
        );
        fireEvent.click(screen.getByRole("button", { name: "Retry" }));
        await waitFor(() =>
            expect(mocks.retry).toHaveBeenCalledWith("sensevoice"),
        );
        rerender(
            <DownloadProgressLine
                downloads={[{ ...job, state: "downloaded" }]}
            />,
        );
        expect(screen.queryByRole("status")).not.toBeInTheDocument();
    });
    it("names the model downloading now when several are queued, in both languages", async () => {
        const jobs = [
            { modelId: "sensevoice", name: "SenseVoice", state: "downloading" as const, downloadedBytes: 41, totalBytes: 100 },
            { modelId: "kokoro", name: "Kokoro", state: "queued" as const, downloadedBytes: 0, totalBytes: 100 },
        ];
        const { rerender } = render(<DownloadProgressLine downloads={jobs} />);
        expect(screen.getByRole("status")).toHaveTextContent("Downloading SenseVoice 41%");
        await act(async () => { await i18n.changeLanguage("zh-CN"); });
        rerender(<DownloadProgressLine downloads={jobs} />);
        expect(screen.getByRole("status")).toHaveTextContent("正在下载 SenseVoice 41%");
        await act(async () => { await i18n.changeLanguage("en"); });
    });
    it("shows background count for more than one download", () => {
        render(
            <DownloadProgressLine
                downloads={["one", "two"].map((modelId) => ({
                    modelId,
                    name: modelId,
                    state: "queued",
                    downloadedBytes: 0,
                    totalBytes: 100,
                }))}
            />,
        );
        expect(screen.getByRole("status")).toHaveTextContent(
            "Downloading 2 in the background",
        );
    });
});

it("continues a resumed setup without offering to confirm already queued or failed jobs again", async () => {
    mocks.jobs.mockResolvedValue(
        fixtureModels(choices).map((model) => ({
            id: model.id,
            state: model.id === "sensevoice" ? "failed" : "queued",
            bytesDone: 0,
            bytesTotal: model.sizeBytes,
            error: "",
        })),
    );
    const next = vi.fn();
    render(<ModelsPanel onNext={next} />);
    await waitFor(() =>
        expect(screen.getByTestId("onboarding-continue")).not.toBeDisabled(),
    );
    expect(screen.getByTestId("onboarding-continue")).toHaveTextContent(
        "Continue",
    );
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(
        screen.queryByText(/on this computer already/),
    ).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("onboarding-continue"));
    expect(next).toHaveBeenCalledOnce();
    expect(mocks.start).not.toHaveBeenCalled();
});

it("falls back to the previous model behavior when the new binding is absent", async () => {
    mocks.choices.mockRejectedValue(
        new Error("Wails binding GetModelChoices is not available"),
    );
    const next = vi.fn();
    render(<ModelsPanel onNext={next} />);
    await screen.findByText(
        "This version uses the current model settings. Continue and manage them in Settings.",
    );
    fireEvent.click(screen.getByTestId("onboarding-continue"));
    expect(next).toHaveBeenCalledOnce();
    expect(mocks.start).not.toHaveBeenCalled();
});
it("keeps the model step optional on a metadata transport failure", async () => {
    mocks.choices.mockRejectedValue(new Error("transport unavailable"));
    const next = vi.fn();
    render(<ModelsPanel onNext={next} />);
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(next).toHaveBeenCalledOnce();
    expect(mocks.start).not.toHaveBeenCalled();
});

async function chooseSource(sheet: HTMLElement, name: string) {
    fireEvent.keyDown(within(sheet).getAllByRole("combobox")[0], {
        key: "ArrowDown",
    });
    fireEvent.click(await screen.findByRole("option", { name }));
}

it("never confirms stale sources: pending and failed re-plans block confirm and preserve the reviewed host", async () => {
    render(<ModelsPanel onNext={vi.fn()} />);
    const sheet = await openSheet();
    let reject!: (error: Error) => void;
    mocks.plan.mockImplementationOnce(
        () =>
            new Promise((_resolve, fail) => {
                reject = fail;
            }),
    );
    await chooseSource(sheet, "Hugging Face");
    const confirm = within(sheet).getByRole("button", {
        name: /^Download [0-9]/,
    });
    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    expect(mocks.start).not.toHaveBeenCalled();
    await act(async () => reject(new Error("offline")));
    expect(confirm).toBeDisabled();
    expect(within(sheet).queryByText("huggingface.co")).not.toBeInTheDocument();
    expect(within(sheet).getAllByText("model-assets.yansu.app")).toHaveLength(
        6,
    );
    await chooseSource(sheet, "Hugging Face");
    // The fixture backend returned the mirror plan; display stays truthful.
    await waitFor(() => expect(confirm).not.toBeDisabled());
    expect(within(sheet).queryByText("huggingface.co")).not.toBeInTheDocument();
    fireEvent.click(confirm);
    await waitFor(() => expect(mocks.start).toHaveBeenCalledWith(plan));
});

it("atomically displays and confirms the successful source replacement", async () => {
    render(<ModelsPanel onNext={vi.fn()} />);
    const sheet = await openSheet();
    const replacement = structuredClone(plan);
    replacement.planId = "hugging-face-plan";
    replacement.items[0].source = {
        kind: "huggingface",
        url: "https://huggingface.co/sensevoice",
    };
    replacement.items[0].host = "huggingface.co";
    mocks.plan.mockResolvedValueOnce(replacement);
    await chooseSource(sheet, "Hugging Face");
    await within(sheet).findByText("huggingface.co");
    fireEvent.click(
        within(sheet).getByRole("button", { name: /^Download [0-9]/ }),
    );
    await waitFor(() => expect(mocks.start).toHaveBeenCalledWith(replacement));
});

it("shows the custom mirror in Settings only while Developer tools are on (answers v1.5.0 S21)", async () => {
    const first = render(<ModelsPanel settings />);
    await screen.findByTestId("model-job-live");
    expect(screen.queryByLabelText("Your own mirror")).not.toBeInTheDocument();
    first.unmount();
    render(
        <SettingsDevToolsContext.Provider value>
            <ModelsPanel settings />
        </SettingsDevToolsContext.Provider>,
    );
    await screen.findByTestId("model-job-live");
    expect(screen.getByLabelText("Your own mirror")).toBeInTheDocument();
});

it("does not offer live model alternatives in Settings", async () => {
    choices.jobs[0].alternatives.find((model) => model.id === "parakeet")!.downloaded = true;
    render(<ModelsPanel settings />);
    const row = await screen.findByTestId("model-job-live");
    expect(within(row).queryByRole("button", { name: /Change|Remove/ })).not.toBeInTheDocument();
});

it("lists sizes, installed states and removal for both Always on assets and the speaker pack", async () => {
    fixtureModels(choices).forEach((model) => {
        model.downloaded = true;
    });
    render(<ModelsPanel settings />);
    for (const [id, size] of [
        ["privacy", "634 MB"],
        ["memory", "86 MB"],
        ["speaker", "34 MB"],
    ]) {
        const row = await screen.findByTestId(`model-asset-${id}`);
        expect(row).toHaveTextContent(size);
        expect(within(row).getByText("Downloaded")).toBeInTheDocument();
        expect(
            within(row).getByRole("button", { name: "Remove" }),
        ).toBeInTheDocument();
    }
});

it.each(["privacy", "memory", "speaker"])(
    "offers confirmed download of the missing %s asset",
    async (id) => {
        render(<ModelsPanel settings />);
        const row = await screen.findByTestId(`model-asset-${id}`);
        expect(row).toHaveTextContent("Not downloaded");
        fireEvent.click(within(row).getByRole("button", { name: "Download" }));
        const sheet = await screen.findByRole("dialog");
        expect(
            within(sheet).getByText("Download 6 models"),
        ).toBeInTheDocument();
        expect(mocks.start).not.toHaveBeenCalled();
        fireEvent.click(
            within(sheet).getByRole("button", { name: /^Download [0-9]/ }),
        );
        await waitFor(() => expect(mocks.start).toHaveBeenCalledWith(plan));
    },
);

it("localizes the full evaluation dialog in Chinese while keeping source measurements", async () => {
    await i18n.changeLanguage("zh-CN");
    render(<ModelsPanel settings />);
    fireEvent.click(
        await screen.findByRole("button", { name: "查看完整评估表" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
        within(dialog).getByRole("heading", { name: "词错误率（越低越好）" }),
    ).toBeInTheDocument();
    expect(
        within(dialog).getByRole("heading", { name: "延迟与资源" }),
    ).toBeInTheDocument();
    expect(dialog).toHaveTextContent("7.11%");
    expect(dialog).toHaveTextContent("0.043");
    expect(dialog).toHaveTextContent("建议");
    expect(dialog).toHaveTextContent("每个模型在每个数据集上使用 500 个样本");
    expect(dialog).not.toHaveTextContent("Word Error Rate");
    expect(dialog).not.toHaveTextContent("Last updated");
    expect(dialog).not.toHaveTextContent("English only");
    expect(dialog).not.toHaveTextContent("Accuracy king");
});

it.each([
    [
        "en",
        "privacy",
        "634 MB",
        "Removes the model that finds names and other personal details in screenshots before anything is sent. Until you download it again, Isoform falls back to pattern-based redaction only, which catches fewer names.",
    ],
    [
        "en",
        "memory",
        "86 MB",
        "Removes the model behind meaning-based memory search. Keyword search keeps working.",
    ],
    [
        "zh-CN",
        "privacy",
        "634 MB",
        "移除在发送截图前识别人名及其他个人信息的模型。重新下载之前，Isoform 仅使用基于规则的脱敏，能识别的人名更少。",
    ],
    [
        "zh-CN",
        "memory",
        "86 MB",
        "移除用于按含义搜索记忆的模型。关键词搜索仍可正常使用。",
    ],
] as const)(
    "confirms installed %s %s removal with its consequence while keeping the choice locked",
    async (language, id, size, consequence) => {
        await i18n.changeLanguage(language);
        fixtureModels(choices).forEach((model) => {
            model.downloaded = true;
        });
        mocks.remove.mockImplementation(async (removedId: string) => {
            fixtureModels(choices).find(
                (model) => model.id === removedId,
            )!.downloaded = false;
        });
        render(<ModelsPanel settings />);
        const row = await screen.findByTestId(`model-asset-${id}`);
        const removeLabel = language === "en" ? "Remove" : "移除";
        const cancelLabel = language === "en" ? "Cancel" : "取消";
        expect(
            within(screen.getByTestId("model-job-always")).queryByRole(
                "button",
            ),
        ).not.toBeInTheDocument();
        fireEvent.click(within(row).getByRole("button", { name: removeLabel }));
        let confirm = await screen.findByRole("dialog");
        expect(confirm).toHaveTextContent(size);
        expect(confirm).toHaveTextContent(consequence);
        expect(mocks.remove).not.toHaveBeenCalled();
        fireEvent.click(
            within(confirm).getByRole("button", { name: cancelLabel }),
        );
        expect(mocks.remove).not.toHaveBeenCalled();
        fireEvent.click(within(row).getByRole("button", { name: removeLabel }));
        confirm = await screen.findByRole("dialog");
        fireEvent.click(
            within(confirm).getByRole("button", { name: removeLabel }),
        );
        await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith(id));
        expect(mocks.remove).toHaveBeenCalledOnce();
        expect(
            await within(row).findByRole("button", {
                name: language === "en" ? "Download" : "下载",
            }),
        ).toBeInTheDocument();
        expect(mocks.choice).not.toHaveBeenCalled();
        expect(mocks.start).not.toHaveBeenCalled();
    },
);

describe.each(["en", "zh-CN"])(
    "native network and download states in %s",
    (language) => {
        const isChinese = language === "zh-CN";
        const states = [
            {
                known: false,
                offline: false,
                expensive: false,
                constrained: false,
                en: "Waiting for network status",
                zh: "正在等待网络状态",
            },
            {
                known: true,
                offline: true,
                expensive: false,
                constrained: false,
                en: "You are offline",
                zh: "当前处于离线状态",
            },
            {
                known: true,
                offline: false,
                expensive: true,
                constrained: false,
                en: "This connection is metered",
                zh: "当前连接按流量计费",
            },
            {
                known: true,
                offline: false,
                expensive: false,
                constrained: true,
                en: "Low Data Mode is on",
                zh: "低数据模式已开启",
            },
        ];
        it.each(states)(
            "reads $en before confirmation in the step and sheet",
            async (state) => {
                await i18n.changeLanguage(language);
                mocks.network.mockResolvedValue(state);
                render(<ModelsPanel onNext={vi.fn()} />);
                expect(
                    await screen.findByTestId("model-network-state"),
                ).toHaveTextContent(isChinese ? state.zh : state.en);
                const sheet = await openSheet();
                expect(
                    within(sheet).getByTestId("model-network-state"),
                ).toHaveTextContent(isChinese ? state.zh : state.en);
                expect(mocks.start).not.toHaveBeenCalled();
                expect(mocks.resume).not.toHaveBeenCalled();
                // Confirmation grants only the reviewed plan; the manager queues it
                // and applies the current native path's admission policy.
                fireEvent.click(
                    within(sheet).getByRole("button", {
                        name: isChinese ? /^下载 [0-9]/ : /^Download [0-9]/,
                    }),
                );
                await waitFor(() =>
                    expect(mocks.start).toHaveBeenCalledWith(plan),
                );
            },
        );
        it("updates the open sheet from native network events without starting downloads", async () => {
            await i18n.changeLanguage(language);
            render(<ModelsPanel onNext={vi.fn()} />);
            const sheet = await openSheet();
            expect(
                within(sheet).queryByTestId("model-network-state"),
            ).not.toBeInTheDocument();
            mocks.network.mockResolvedValue(states[1]);
            await act(async () => mocks.events.get("models:downloads")?.());
            expect(
                await within(sheet).findByTestId("model-network-state"),
            ).toHaveTextContent(isChinese ? states[1].zh : states[1].en);
            mocks.network.mockResolvedValue({
                known: true,
                offline: false,
                expensive: false,
                constrained: false,
            });
            await act(async () => mocks.events.get("models:downloads")?.());
            await waitFor(() =>
                expect(
                    within(sheet).queryByTestId("model-network-state"),
                ).not.toBeInTheDocument(),
            );
            expect(mocks.start).not.toHaveBeenCalled();
        });
        it.each([
            ["Paused while offline", "paused while offline", "已因离线暂停"],
            [
                "Waiting for network status",
                "waiting for network status",
                "正在等待网络状态",
            ],
            [
                "Paused on a metered connection",
                "paused on a metered connection",
                "已因按流量计费的网络暂停",
            ],
            ["not enough free disk space", "Free disk space", "请释放磁盘空间"],
            [
                "another native admission failure",
                "check the connection and disk space",
                "重新检查网络和磁盘空间",
            ],
        ])(
            "keeps a %s job visible and resumes only that confirmed id",
            async (pauseReason, en, zh) => {
                await i18n.changeLanguage(language);
                const job = {
                    modelId: "sensevoice",
                    name: "SenseVoice",
                    state: "paused" as const,
                    pauseReason,
                    downloadedBytes: 41,
                    totalBytes: 100,
                };
                const refresh = vi.fn();
                const { rerender } = render(
                    <DownloadProgressLine
                        downloads={[job]}
                        onRetry={refresh}
                    />,
                );
                expect(screen.getByRole("status")).toHaveTextContent(
                    isChinese ? zh : en,
                );
                expect(mocks.resume).not.toHaveBeenCalled();
                fireEvent.click(
                    screen.getByRole("button", {
                        name: isChinese ? "继续下载" : "Resume",
                    }),
                );
                await waitFor(() =>
                    expect(mocks.resume).toHaveBeenCalledWith("sensevoice"),
                );
                expect(mocks.resume).toHaveBeenCalledOnce();
                expect(mocks.retry).not.toHaveBeenCalled();
                expect(refresh).toHaveBeenCalledOnce();
                // Successful IPC alone is not progress: a metered path can stay paused.
                expect(screen.getByRole("status")).toHaveTextContent(
                    isChinese ? zh : en,
                );
                rerender(
                    <DownloadProgressLine
                        downloads={[
                            { ...job, state: "downloading", pauseReason: "" },
                        ]}
                    />,
                );
                expect(screen.getByRole("status")).toHaveTextContent("41%");
                expect(screen.queryByRole("button")).not.toBeInTheDocument();
                rerender(
                    <DownloadProgressLine
                        downloads={[
                            {
                                ...job,
                                state: "downloaded",
                                downloadedBytes: 100,
                            },
                        ]}
                    />,
                );
                expect(screen.queryByRole("status")).not.toBeInTheDocument();
            },
        );
        it("shows low-data pause, resume failure, failed retry and queued state", async () => {
            await i18n.changeLanguage(language);
            const job = {
                modelId: "sensevoice",
                name: "SenseVoice",
                state: "paused" as const,
                pauseReason: "Paused on a metered connection",
                downloadedBytes: 41,
                totalBytes: 100,
            };
            mocks.resume.mockRejectedValue(new Error("disk unavailable"));
            const { rerender } = render(
                <DownloadProgressLine
                    downloads={[job]}
                    network={{
                        known: true,
                        offline: false,
                        expensive: false,
                        constrained: true,
                    }}
                />,
            );
            expect(screen.getByRole("status")).toHaveTextContent(
                isChinese ? "低数据模式暂停" : "paused in Low Data Mode",
            );
            fireEvent.click(
                screen.getByRole("button", {
                    name: isChinese ? "继续下载" : "Resume",
                }),
            );
            await screen.findByText(
                isChinese
                    ? "无法继续下载 SenseVoice。请重试。"
                    : "Could not resume SenseVoice. Try again.",
            );
            rerender(
                <DownloadProgressLine
                    downloads={[{ ...job, state: "failed", pauseReason: "" }]}
                />,
            );
            expect(screen.getByRole("status")).toHaveTextContent(
                isChinese
                    ? "SenseVoice 下载失败"
                    : "SenseVoice download failed",
            );
            mocks.retry.mockRejectedValueOnce(new Error("failed"));
            fireEvent.click(
                screen.getByRole("button", {
                    name: isChinese ? "重试" : "Retry",
                }),
            );
            await waitFor(() =>
                expect(mocks.retry).toHaveBeenCalledWith("sensevoice"),
            );
            await screen.findByText(
                isChinese
                    ? "无法重试 SenseVoice，请再试一次。"
                    : "Could not retry SenseVoice. Try again.",
            );
            fireEvent.click(
                screen.getByRole("button", {
                    name: isChinese ? "重试" : "Retry",
                }),
            );
            await waitFor(() => expect(mocks.retry).toHaveBeenCalledTimes(2));
            rerender(
                <DownloadProgressLine
                    downloads={[{ ...job, state: "queued", pauseReason: "" }]}
                />,
            );
            expect(screen.getByRole("status")).toHaveTextContent(
                isChinese
                    ? "已排队等待后台下载"
                    : "queued for background download",
            );
        });
        it("keeps mixed paused, failed and active jobs visible in Storage", async () => {
            await i18n.changeLanguage(language);
            mocks.jobs.mockResolvedValue([
                {
                    id: "sensevoice",
                    state: "paused",
                    pauseReason: "Paused while offline",
                    bytesDone: 41,
                    bytesTotal: 100,
                    error: "",
                },
                {
                    id: "qwen3",
                    state: "failed",
                    pauseReason: "",
                    bytesDone: 0,
                    bytesTotal: 100,
                    error: "private path that must not be rendered",
                },
                {
                    id: "kokoro",
                    state: "downloading",
                    pauseReason: "",
                    bytesDone: 41,
                    bytesTotal: 100,
                    error: "",
                },
            ]);
            render(<ModelsPanel settings />);
            const status = await screen.findByTestId("model-download-progress");
            expect(status).toHaveTextContent("SenseVoice");
            expect(status).toHaveTextContent("Qwen3");
            expect(status).toHaveTextContent("41%");
            expect(status).not.toHaveTextContent("private path");
            expect(
                within(status).getByRole("button", {
                    name: isChinese ? "继续下载" : "Resume",
                }),
            ).toBeInTheDocument();
            expect(
                within(status).getByRole("button", {
                    name: isChinese ? "重试" : "Retry",
                }),
            ).toBeInTheDocument();
        });
    },
);

it("shows a network snapshot transport failure rather than inventing an online or legacy state", async () => {
    mocks.network.mockRejectedValue(new Error("IPC disconnected"));
    render(<ModelsPanel onNext={vi.fn()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
        "Could not read or update the models",
    );
    expect(screen.queryByTestId("model-network-state")).not.toBeInTheDocument();
    expect(mocks.start).not.toHaveBeenCalled();
});

it("keeps the footer to one actionable interruption and prioritizes failures", () => {
    const job = {
        name: "SenseVoice",
        modelId: "sensevoice",
        downloadedBytes: 41,
        totalBytes: 100,
    };
    render(
        <DownloadProgressLine
            downloads={[
                {
                    ...job,
                    state: "paused",
                    pauseReason: "Paused while offline",
                },
                { ...job, modelId: "qwen3", name: "Qwen3", state: "failed" },
                { ...job, modelId: "kokoro", state: "downloading" },
            ]}
        />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
        "Qwen3 download failed",
    );
    expect(screen.getAllByRole("button")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
});

it("shows a model confirmed with Later as waiting for power and idle, in both languages", async () => {
    mocks.jobs.mockResolvedValue([
        { id: "privacy", state: "queued", pauseReason: "", bytesDone: 0, bytesTotal: 100, error: "", deferred: true },
    ]);
    render(<ModelsPanel settings />);
    const row = await screen.findByTestId("model-asset-privacy");
    await waitFor(() => expect(row).toHaveTextContent("Waiting for power and idle"));
    expect(screen.getByTestId("model-asset-memory")).toHaveTextContent("Not downloaded");
    expect(screen.queryByText(/queued for background download/)).toBeNull();
    await act(async () => {
        await i18n.changeLanguage("zh-CN");
    });
    expect(row).toHaveTextContent("等待接上电源并空闲");
    await act(async () => {
        await i18n.changeLanguage("en");
    });
});

it("Settings shows its own Models card, never onboarding's continue words or a path to this page (ST64)", async () => {
    render(<ModelsPanel settings />);
    expect(await screen.findByText("Models on this computer")).toBeInTheDocument();
    expect(screen.getByText("These models read your screen and audio on this computer.")).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/just continue|Settings › Storage › Models/);
    expect(screen.getAllByRole("button", { name: /^Change / }).length).toBeGreaterThan(0);
});

describe("Settings models: one row per model and an honest in-use choice (ST77)", () => {
    function chooseLive(id: string) {
        choices = {
            ...choices,
            jobs: choices.jobs.map((row) =>
                row.job === "live" ? { ...row, chosenId: id } : row,
            ),
        };
    }

    it("never shows a not-downloaded chosen model as in use and keeps its Download row", async () => {
        chooseLive("whisper");
        render(<ModelsPanel settings />);
        const row = await screen.findByTestId("model-job-live");
        expect(within(row).queryByText("Whisper large-v3 turbo")).toBeNull();
        expect(within(row).getByText("None")).toBeInTheDocument();
        expect(screen.queryByTestId("model-asset-whisper")).not.toBeInTheDocument();
    });

    it("falls back to the downloaded model that serves the job when the choice is not on disk", async () => {
        chooseLive("whisper");
        fixtureModels(choices).find((item) => item.id === "sensevoice")!.downloaded = true;
        render(<ModelsPanel settings />);
        const row = await screen.findByTestId("model-job-live");
        expect(within(row).getByText("SenseVoice")).toBeInTheDocument();
        expect(within(row).getByText("Downloaded")).toBeInTheDocument();
        expect(within(row).queryByText("Whisper large-v3 turbo")).toBeNull();
        expect(screen.queryByTestId("model-asset-whisper")).not.toBeInTheDocument();
    });

    it("lists each model once, on its job row, when everything is downloaded", async () => {
        fixtureModels(choices).forEach((item) => {
            item.downloaded = true;
        });
        render(<ModelsPanel settings />);
        await screen.findByTestId("model-job-live");
        const list = document.querySelector(".setup-list") as HTMLElement;
        for (const name of ["SenseVoice", "Qwen3-ASR 1.7B", "Kokoro", "Privacy filter", "Memory search"]) {
            expect(within(list).getAllByText(name), name).toHaveLength(1);
        }
    });

    it("lists a chosen model that is not on disk once, on its Download row", async () => {
        chooseLive("whisper");
        render(<ModelsPanel settings />);
        await screen.findByTestId("model-job-live");
        const list = document.querySelector(".setup-list") as HTMLElement;
        expect(within(list).queryByText("Whisper large-v3 turbo")).not.toBeInTheDocument();
    });
});
