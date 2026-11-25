import Foundation
import SwiftUI
import Vision
import XCTest
@testable import Yansu

final class PurposeBriefTests: XCTestCase {
    private let purposeJSON = """
    {
      "version": 1,
      "briefId": "2026-10-16-morning",
      "timezone": "America/Los_Angeles",
      "sections": {
        "workingToward": [{
          "id": "purpose-1", "title": "Ship the iPhone companion",
          "detail": "Two sessions reviewed pairing and the morning brief layout.",
          "lastTouch": "2026-10-16T16:30:00Z",
          "nextStep": "Review the simulator screenshots with the team.",
          "refs": ["session-1"]
        }],
        "commitments": [{
          "id": "commitment-1", "title": "Send the release plan",
          "detail": "The release document was edited; no completed send is recorded.",
          "status": "slipping", "dueAt": "2026-10-16T17:00:00Z",
          "lastTouch": "2026-10-15T16:30:00Z",
          "momentId": "moment-1", "chatSessionId": "mobile-main-chat",
          "chatMessageId": "report-message-1", "refs": ["session-1", "moment-1"]
        }],
        "waitingOnYou": [{
          "id": "person-1", "title": "Morgan",
          "detail": "No touch has been recorded for the research partnership in three weeks.",
          "lastTouch": "2026-09-20T16:30:00Z", "refs": ["purpose-1"]
        }]
      },
      "evidence": [
        {"ref": "session-1", "sourceKind": "summary", "sourceId": "session-1", "label": "Pairing review in Xcode"},
        {"ref": "moment-1", "sourceKind": "engagement_moment", "sourceId": "moment-1", "label": "Release plan commitment"},
        {"ref": "purpose-1", "sourceKind": "purpose", "sourceId": "purpose-1", "label": "Research partnership"}
      ],
      "limits": {"excludedSensitiveItems": 2}
    }
    """

    func testOlderAndNullBriefSectionsStayReadable() throws {
        for json in [
            """
            {"sections":{"openLoops":[{"title":"Older purpose"}]}}
            """,
            """
            {"sections":{"workingToward":null,"commitments":null,"waitingOnYou":null,
             "openLoops":[{"title":"Older purpose"}]},"limits":{"excludedSensitiveItems":null}}
            """
        ] {
            let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(json.utf8))
            XCTAssertEqual(brief.sections.openLoops.first?.title, "Older purpose")
            XCTAssertTrue(brief.sections.workingToward.isEmpty)
            XCTAssertTrue(brief.sections.commitments.isEmpty)
            XCTAssertTrue(brief.sections.waitingOnYou.isEmpty)
            XCTAssertNil(brief.limits?.excludedSensitiveItems)
        }
    }

    func testPurposeBriefDecodesProgressEvidenceAndExistingChatReference() throws {
        let brief = try decodePurposeBrief()
        XCTAssertFalse(brief.sections.isEmpty)
        let purpose = try XCTUnwrap(brief.sections.workingToward.first)
        XCTAssertEqual(purpose.detail, "Two sessions reviewed pairing and the morning brief layout.")
        XCTAssertEqual(purpose.nextStep, "Review the simulator screenshots with the team.")
        XCTAssertEqual(purpose.lastTouchDate, RelayTime.parse("2026-10-16T16:30:00Z"))
        let commitment = try XCTUnwrap(brief.sections.commitments.first)
        XCTAssertEqual(commitment.status, "slipping")
        XCTAssertEqual(commitment.dueDate, RelayTime.parse("2026-10-16T17:00:00Z"))
        XCTAssertEqual(commitment.momentId, "moment-1")
        XCTAssertEqual(commitment.chatSessionId, "mobile-main-chat")
        XCTAssertEqual(commitment.chatMessageId, "report-message-1")
        XCTAssertEqual(brief.sources(for: commitment.refs).map(\.label),
                       ["Pairing review in Xcode", "Release plan commitment"])
        XCTAssertEqual(brief.sections.waitingOnYou.first?.title, "Morgan")
        XCTAssertEqual(brief.limits?.excludedSensitiveItems, 2)
    }

    func testPurposeOnlySectionsAreNotEmpty() throws {
        for name in ["workingToward", "commitments", "waitingOnYou"] {
            let data = try JSONSerialization.data(withJSONObject: ["sections": [name: [["title": "One item"]]]])
            let brief = try JSONDecoder().decode(MobileBriefV1.self, from: data)
            XCTAssertFalse(brief.sections.isEmpty, name)
        }
    }

    func testPurposeFieldsSurviveOfflineCacheRoundTrip() throws {
        let brief = try decodePurposeBrief()
        let encoded = try JSONEncoder().encode(brief)
        XCTAssertEqual(try JSONDecoder().decode(MobileBriefV1.self, from: encoded), brief)
    }

    func testUnavailableEvidenceFlagSurvivesOfflineCacheRoundTrip() throws {
        let data = Data("""
        {"title":"Ship the beta","status":"active","evidenceUnavailable":true,"purposeId":"purged-purpose","url":"https://example.com/beta",
         "detail":"Evidence no longer on this Mac."}
        """.utf8)
        let item = try JSONDecoder().decode(BriefItem.self, from: data)
        XCTAssertNil(item.webURL, "Unavailable evidence is a content-free row, not a stale destination.")
        let encoded = try JSONEncoder().encode(item)
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: encoded) as? [String: Any])
        XCTAssertEqual(object["evidenceUnavailable"] as? Bool, true)
        XCTAssertEqual(object["purposeId"] as? String, "purged-purpose")
        XCTAssertEqual(try JSONDecoder().decode(BriefItem.self, from: encoded), item)
        // Older Macs omit the additive flag and retain the existing item behavior.
        let older = try JSONDecoder().decode(BriefItem.self, from: Data("{\"title\":\"Older purpose\"}".utf8))
        let olderObject = try XCTUnwrap(JSONSerialization.jsonObject(with: JSONEncoder().encode(older)) as? [String: Any])
        XCTAssertNil(olderObject["evidenceUnavailable"])
        XCTAssertNil(olderObject["purposeId"])
    }

    func testPendingMomentsAreNotPresentedAsAlreadyInMainChat() {
        XCTAssertFalse(BriefItem(momentId: "pending-moment").hasMainChatReference)
        XCTAssertFalse(BriefItem(momentId: "pending-moment", chatMessageId: "").hasMainChatReference)
        XCTAssertTrue(BriefItem(momentId: "reported-moment", chatMessageId: "stored-message").hasMainChatReference)
    }

    func testPurposeEvidenceUsesReadableSourceLabels() {
        for (kind, label) in ["activity_run": "Activity summary", "activity_session": "Work session",
                              "activity": "Activity", "purpose": "Goal",
                              "engagement_moment": "Reminder", "chat_message": "Chat"] {
            XCTAssertEqual(BriefEvidence(sourceKind: kind).sourceLabel, label)
        }
    }

    func testPurposeSourcesDoNotCountIdentityOrNextStepEvidence() {
        var brief = MobileBriefV1()
        brief.evidence = [
            BriefEvidence(ref: "goal", sourceKind: "purpose", sourceId: "goal-id"),
            BriefEvidence(ref: "step", sourceKind: "purpose", sourceId: "step-id"),
            BriefEvidence(ref: "work", sourceKind: "activity_session", sourceId: "session-id")
        ]
        XCTAssertTrue(brief.purposeSources(for: BriefItem(refs: ["goal"])).isEmpty)
        XCTAssertEqual(brief.purposeSources(for: BriefItem(refs: ["goal", "work", "step"])).compactMap(\.ref),
                       ["work"])
        XCTAssertEqual(brief.purposeSources(for: BriefItem(refs: ["work", "step"])).compactMap(\.ref),
                       ["work"], "An observed source remains evidence even when its row has no primary purpose ref.")
        XCTAssertTrue(brief.purposeSources(for: BriefItem(refs: ["work"], evidenceUnavailable: true)).isEmpty,
                      "Unavailable evidence must not restore stale cached source references.")
        XCTAssertNil(brief.primaryPurposeSource(for: BriefItem(refs: ["missing", "step"])),
                     "A missing primary ref must not promote a next-step ref to the row's identity.")
    }

    func testUnavailableEvidencePurposeIdentityDeduplicatesWithoutSources() throws {
        let data = Data("""
        {"sections":{"workingToward":[{"title":"Ship the beta","status":"active",
          "detail":"Evidence no longer on this Mac.","evidenceUnavailable":true,"purposeId":"purged-purpose","refs":[]}],
         "openLoops":[{"id":"legacy","title":"Ship the beta","status":"active",
          "detail":"Evidence no longer on this Mac.","evidenceUnavailable":true,"purposeId":"purged-purpose","refs":[]},
          {"id":"overflow","title":"Another goal","purposeId":"other-purpose","refs":[]}]},"evidence":[]}
        """.utf8)
        var brief = try JSONDecoder().decode(MobileBriefV1.self, from: data)
        let item = try XCTUnwrap(brief.sections.workingToward.first)
        XCTAssertEqual(brief.purposeID(for: item), "purged-purpose")
        XCTAssertTrue(item.refs.isEmpty)
        XCTAssertTrue(brief.evidence.isEmpty)
        XCTAssertTrue(brief.purposeSources(for: item).isEmpty)
        XCTAssertEqual(brief.remainingOpenLoops.compactMap(\.id), ["overflow"])
        brief.sections.workingToward = []
        XCTAssertEqual(brief.remainingOpenLoops.compactMap(\.id), ["legacy", "overflow"])
    }

    func testNextStepReferenceDoesNotHideUnshownLegacyPurpose() {
        var brief = MobileBriefV1()
        brief.evidence = (1...6).map {
            BriefEvidence(ref: "purpose-\($0)", sourceKind: "purpose", sourceId: "purpose-id-\($0)")
        }
        brief.sections.openLoops = (1...6).map {
            BriefItem(id: "loop-\($0)", title: "Purpose \($0)", refs: ["purpose-\($0)"])
        }
        brief.sections.workingToward = Array(brief.sections.openLoops.prefix(5))
        brief.sections.workingToward[0].refs.append("purpose-6")
        brief.sections.workingToward[0].nextStep = "Purpose 6"

        XCTAssertEqual(brief.remainingOpenLoops.compactMap(\.id), ["loop-6"],
                       "A next step does not replace the unshown purpose's own open-loop card.")
        XCTAssertTrue(brief.showsOpenLoopsSection)
    }

    func testEmptyOpenLoopSectionIsHiddenOnlyWithPurposeSections() {
        var brief = MobileBriefV1()
        XCTAssertTrue(brief.showsOpenLoopsSection, "Older briefs keep their empty open-loop state.")
        brief.sections.workingToward = [BriefItem(title: "Active purpose")]
        XCTAssertFalse(brief.showsOpenLoopsSection)
        brief.sections.openLoops = [BriefItem(title: "Unrelated work", kind: "one_time_work")]
        XCTAssertTrue(brief.showsOpenLoopsSection)
        brief.sections.openLoops = []
        brief.sections.workingToward = []
        brief.limits = BriefLimits(excludedSensitiveItems: 1)
        XCTAssertTrue(brief.showsOpenLoopsSection, "A privacy notice alone does not replace open loops.")
    }

    func testNewSectionsHideOnlyLegacyCopiesOfTheSamePurpose() {
        var brief = MobileBriefV1()
        brief.evidence = [
            BriefEvidence(ref: "purpose-source", sourceKind: "purpose", sourceId: "purpose-1"),
            BriefEvidence(ref: "other-source", sourceKind: "purpose", sourceId: "purpose-2")
        ]
        brief.sections.workingToward = [BriefItem(title: "Launch", refs: ["purpose-source"])]
        brief.sections.openLoops = [
            BriefItem(id: "legacy", title: "Launch", refs: ["purpose-source"]),
            BriefItem(id: "unrelated", title: "Launch", refs: ["other-source"]),
            BriefItem(id: "work", title: "Launch", kind: "one_time_work", refs: ["purpose-source"])
        ]
        XCTAssertEqual(brief.remainingOpenLoops.compactMap(\.id), ["unrelated", "work"])
        brief.sections.workingToward = []
        XCTAssertEqual(brief.remainingOpenLoops, brief.sections.openLoops,
                       "Old briefs retain every purpose when they have no dedicated sections.")
    }

    func testUnknownPurposeFieldsAndMalformedDatesKeepTheRestReadable() throws {
        let json = """
        {"sections":{"workingToward":[{"title":"Ship the companion","lastTouch":"unknown",
          "dueAt":"", "futureProgress":{"confidence":0.7}}],"futurePurposeSection":[]}}
        """
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(json.utf8))
        let item = try XCTUnwrap(brief.sections.workingToward.first)
        XCTAssertEqual(item.title, "Ship the companion")
        XCTAssertNil(item.lastTouchDate)
        XCTAssertNil(item.dueDate)
    }

    func testDueLabelsUseReadTimeAndTheWholeMacCalendarDay() throws {
        let now = try XCTUnwrap(RelayTime.parse("2026-10-17T07:30:00Z"))
        for (dueAt, expected) in [
            ("2026-10-17T06:59:59Z", "Overdue"),
            ("2026-10-17T07:00:00Z", "Due today"),
            ("2026-10-18T06:59:59Z", "Due today")
        ] {
            let item = BriefItem(dueAt: dueAt)
            XCTAssertEqual(item.dueLabel(now: now, timezone: "America/Los_Angeles"), expected, dueAt)
        }
        XCTAssertNil(BriefItem(dueAt: "2026-10-18T07:00:00Z")
            .dueLabel(now: now, timezone: "America/Los_Angeles"))
        let item = BriefItem(dueAt: "2026-10-17T06:59:59Z")
        XCTAssertEqual(item.dueLabel(now: now, timezone: "UTC"), "Due today")
        let tomorrow = try XCTUnwrap(RelayTime.parse("2026-10-18T07:30:00Z"))
        XCTAssertEqual(item.dueLabel(now: tomorrow, timezone: "UTC"), "Overdue",
                       "Cached briefs must describe deadlines relative to the day they are read.")
        let beforeMidnight = try XCTUnwrap(RelayTime.parse("2026-10-18T06:59:59Z"))
        let midnight = try XCTUnwrap(RelayTime.parse("2026-10-18T07:00:00Z"))
        let dueToday = BriefItem(dueAt: "2026-10-17T08:00:00Z")
        XCTAssertEqual(dueToday.dueLabel(now: beforeMidnight, timezone: "America/Los_Angeles"), "Due today")
        XCTAssertEqual(dueToday.dueLabel(now: midnight, timezone: "America/Los_Angeles"), "Overdue")
    }

    func testDueLabelsHandleDaylightSavingCalendarBoundaries() throws {
        let now = try XCTUnwrap(RelayTime.parse("2026-11-01T16:00:00Z"))
        for dueAt in ["2026-11-01T07:00:00Z", "2026-11-02T07:59:59Z"] {
            XCTAssertEqual(BriefItem(dueAt: dueAt).dueLabel(now: now, timezone: "America/Los_Angeles"),
                           "Due today", "The day when daylight saving ends has 25 hours.")
        }
        XCTAssertNil(BriefItem(dueAt: "2026-11-02T08:00:00Z")
            .dueLabel(now: now, timezone: "America/Los_Angeles"))
    }

    func testDueLabelsIgnoreMissingMalformedAndUnavailableEvidenceDates() throws {
        let now = try XCTUnwrap(RelayTime.parse("2026-10-17T16:00:00Z"))
        for json in ["{}", "{\"dueAt\":null}", "{\"dueAt\":\"\"}", "{\"dueAt\":\"unknown\"}"] {
            let item = try JSONDecoder().decode(BriefItem.self, from: Data(json.utf8))
            XCTAssertNil(item.dueLabel(now: now, timezone: "America/Los_Angeles"), json)
        }
        let unavailable = BriefItem(dueAt: "2026-10-16T16:00:00Z", evidenceUnavailable: true)
        XCTAssertNil(unavailable.dueLabel(now: now, timezone: "America/Los_Angeles"))
    }

    @MainActor
    func testDueRowsRenderSmallLabelsWithoutPastClockTimes() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: directory.appendingPathComponent("briefs"),
                                         journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: directory.appendingPathComponent("chats"))
        let now = try XCTUnwrap(RelayTime.parse("2026-10-17T16:00:00Z"))
        let dueToday = BriefItem(title: "Send the plan", status: "slipping", at: "2026-10-16T17:00:00Z",
                                 kind: "commitment", lastTouch: "2026-10-16T17:00:00Z",
                                 dueAt: "2026-10-17T08:00:00Z")
        let overdue = BriefItem(title: "Review the draft", status: "due_today", at: "2026-10-16T17:00:00Z",
                                kind: "commitment", lastTouch: "2026-10-16T17:00:00Z",
                                dueAt: "2026-10-17T06:59:00Z")
        for section in ["workingToward", "openLoops"] {
            var brief = MobileBriefV1(timezone: "America/Los_Angeles")
            if section == "workingToward" {
                brief.sections.workingToward = [dueToday, overdue]
            } else {
                brief.sections.openLoops = [dueToday, overdue]
            }
            let text = try renderedText(BriefContentView(brief: brief, now: now)
                .environmentObject(environment.actions)
                .environmentObject(environment.pairing).environmentObject(environment)
                .environmentObject(environment.chat).environmentObject(environment.chatAvailability).environmentObject(environment.brief)
                .environmentObject(environment.router), name: "Due labels in \(section)")
            XCTAssertTrue(text.contains("due today"), text)
            XCTAssertTrue(text.contains("overdue"), text)
            XCTAssertEqual(text.components(separatedBy: "due today").count - 1, 1,
                           "A cached due_today status must not contradict the read-time deadline: \(text)")
            for forbidden in ["slipping", "due:", "10:00", "1:00", "11:59"] {
                XCTAssertFalse(text.contains(forbidden), "Unexpected past clock time: \(text)")
            }
        }
    }

    @MainActor
    func testDueRowsAdvanceAcrossMacMidnightWhenTheClockChanges() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: directory.appendingPathComponent("briefs"),
                                         journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: directory.appendingPathComponent("chats"))
        let beforeMidnight = try XCTUnwrap(RelayTime.parse("2026-10-18T06:59:59Z"))
        let midnight = try XCTUnwrap(RelayTime.parse("2026-10-18T07:00:00Z"))
        let item = BriefItem(title: "Send the plan", status: "due_today", kind: "commitment",
                             dueAt: "2026-10-17T08:00:00Z")
        for section in ["workingToward", "openLoops"] {
            var brief = MobileBriefV1(timezone: "America/Los_Angeles")
            if section == "workingToward" {
                brief.sections.workingToward = [item]
            } else {
                brief.sections.openLoops = [item]
            }
            // History's periodic timeline advances now without fetching a new copy of the brief.
            var content = BriefContentView(brief: brief, now: beforeMidnight)
            func snapshot(_ name: String) throws -> String {
                try renderedText(content.environmentObject(environment.actions)
                    .environmentObject(environment.pairing).environmentObject(environment)
                    .environmentObject(environment.chat).environmentObject(environment.chatAvailability).environmentObject(environment.brief)
                    .environmentObject(environment.router), name: name)
            }
            let before = try snapshot("Before midnight in \(section)")
            XCTAssertTrue(before.contains("due today"), before)
            XCTAssertFalse(before.contains("overdue"), before)
            content.now = midnight
            let after = try snapshot("After midnight in \(section)")
            XCTAssertTrue(after.contains("overdue"), after)
            XCTAssertFalse(after.contains("due today"), after)
        }
    }

    @MainActor
    func testPurposeDatesUseTheMacTimezone() throws {
        let date = try XCTUnwrap(RelayTime.parse("2026-10-16T01:30:00Z"))
        let formatted = BriefPurposeSections.dateTime(date, timezone: "America/Los_Angeles",
                                                     locale: Locale(identifier: "en_GB"))
        XCTAssertTrue(formatted.contains("15 Oct 2026"), formatted)
        XCTAssertTrue(formatted.contains("18:30"), formatted)
    }

    @MainActor
    func testPurposeWithoutActivityRendersWithoutSourceChip() throws {
        var brief = MobileBriefV1()
        brief.sections.workingToward = [
            BriefItem(title: "Ship the companion", detail: "No linked activity recorded yet.", refs: ["goal"])
        ]
        brief.evidence = [BriefEvidence(ref: "goal", sourceKind: "purpose", sourceId: "goal-id")]
        let text = try renderedText(BriefPurposeSections(brief: brief) { _ in }, name: "Purpose without activity")
        XCTAssertTrue(text.contains("no linked activity recorded yet"), text)
        XCTAssertFalse(text.contains("source"), "A purpose identity must not render as an evidence chip: \(text)")
    }

    @MainActor
    func testOverflowPurposeOpenLoopOnlyCountsObservedSources() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: directory.appendingPathComponent("briefs"),
                                         journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: directory.appendingPathComponent("chats"))
        var brief = MobileBriefV1()
        brief.sections.openLoops = (1...6).map { index in
            BriefItem(id: "loop-\(index)", title: index == 6 ? "Ship the beta" : "Purpose \(index)",
                      detail: "No linked activity recorded yet.", kind: "goal", refs: ["purpose-\(index)"])
        }
        brief.sections.workingToward = Array(brief.sections.openLoops.prefix(5))
        brief.evidence = (1...6).map { index in
            BriefEvidence(ref: "purpose-\(index)", sourceKind: "purpose", sourceId: "purpose-id-\(index)",
                          label: index == 6 ? "Ship the beta" : "Purpose \(index)")
        }
        XCTAssertEqual(brief.remainingOpenLoops.compactMap(\.id), ["loop-6"])

        func snapshot(_ name: String) throws -> String {
            try renderedText(BriefContentView(brief: brief)
                .environmentObject(environment.actions)
                .environmentObject(environment.pairing).environmentObject(environment)
                .environmentObject(environment.chat).environmentObject(environment.chatAvailability).environmentObject(environment.brief)
                .environmentObject(environment.router), name: name)
        }

        let withoutActivity = try snapshot("Overflow purpose without activity")
        XCTAssertTrue(withoutActivity.contains("ship the beta"), withoutActivity)
        XCTAssertTrue(withoutActivity.contains("priorities and open loops"), withoutActivity)
        XCTAssertFalse(withoutActivity.contains("source"),
                       "The legacy overflow row must not count its own purpose as evidence: \(withoutActivity)")

        brief.sections.openLoops[5].refs.append("session")
        brief.sections.openLoops[5].detail = "Reviewed the beta checklist."
        brief.evidence.append(BriefEvidence(ref: "session", sourceKind: "activity_session", sourceId: "session-id",
                                           label: "Beta checklist review"))
        let withActivity = try snapshot("Overflow purpose with observed activity")
        XCTAssertTrue(withActivity.contains("1 source"), withActivity)
        XCTAssertFalse(withActivity.contains("2 sources"),
                       "Only the observed session belongs in the legacy row's source chip: \(withActivity)")
    }

    @MainActor
    func testNarrativeChipsOnlyCountObservedSources() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: directory.appendingPathComponent("briefs"),
                                         journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: directory.appendingPathComponent("chats"))
        var brief = MobileBriefV1()
        brief.narrative = [BriefParagraph(text: "You are working toward the launch.", refs: ["goal", "next"])]
        brief.evidence = [
            BriefEvidence(ref: "goal", sourceKind: "purpose", sourceId: "goal-id", label: "Ship the beta"),
            BriefEvidence(ref: "next", sourceKind: "purpose", sourceId: "next-id", label: "Review the checklist")
        ]
        // The written paragraph's chip lives in the brief view (BriefParagraphSources), which the Mac's words go through.
        func snapshot(_ name: String) throws -> String {
            try renderedText(BriefParagraphSources(brief: brief, refs: brief.narrative[0].refs) { _ in }
                .frame(width: 361, alignment: .leading)
                .environmentObject(environment.actions)
                .environmentObject(environment.pairing), name: name)
        }
        let identityOnly = try snapshot("Narrative with goal identities")
        XCTAssertFalse(identityOnly.contains("source"), identityOnly)
        brief.narrative[0].refs.append("observed")
        brief.evidence.append(BriefEvidence(ref: "observed", sourceKind: "activity_session",
                                           sourceId: "session-id", label: "Checklist review"))
        let withActivity = try snapshot("Narrative with observed activity")
        XCTAssertTrue(withActivity.contains("1 source"), withActivity)
        XCTAssertFalse(withActivity.contains("3 sources"), withActivity)
    }

    @MainActor
    func testNextStepPurposeDoesNotRenderAsAnExtraSourceChip() throws {
        var brief = MobileBriefV1()
        brief.sections.workingToward = [BriefItem(title: "Ship the beta", refs: ["goal", "next"],
                                                nextStep: "Review the checklist")]
        brief.evidence = [
            BriefEvidence(ref: "goal", sourceKind: "purpose", sourceId: "goal-id"),
            BriefEvidence(ref: "next", sourceKind: "purpose", sourceId: "next-id")
        ]
        let text = try renderedText(BriefPurposeSections(brief: brief) { _ in }, name: "Next step without source chip")
        XCTAssertTrue(text.contains("next: review the checklist"), text)
        XCTAssertFalse(text.contains("source"), text)
    }

    @MainActor
    func testUnavailableEvidenceRowsRenderOnlyTitleStateAndFallback() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: directory.appendingPathComponent("briefs"),
                                         journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: directory.appendingPathComponent("chats"))
        let itemJSON = """
        {"title":"Ship the beta","status":"active","detail":"Evidence no longer on this Mac.",
         "evidenceUnavailable":true,"at":"2026-10-16T17:00:00Z","lastTouch":"2026-10-16T17:00:00Z",
         "nextStep":"Do not display this step","dueAt":"2026-10-16T18:00:00Z",
         "chatSessionId":"main-chat","chatMessageId":"message-1","purposeId":"purged-purpose","refs":[]}
        """
        let item = try JSONDecoder().decode(BriefItem.self, from: Data(itemJSON.utf8))
        var brief = MobileBriefV1()
        XCTAssertTrue(brief.evidence.isEmpty)
        XCTAssertTrue(item.refs.isEmpty)
        for section in ["workingToward", "openLoops"] {
            brief.sections = BriefSections()
            if section == "workingToward" {
                brief.sections.workingToward = [item]
            } else {
                brief.sections.openLoops = [item]
            }
            let text = try renderedText(BriefContentView(brief: brief)
                .environmentObject(environment.actions)
                .environmentObject(environment.pairing)
                .environmentObject(environment.router).environmentObject(environment)
                .environmentObject(environment.chat).environmentObject(environment.chatAvailability).environmentObject(environment.brief), name: "Unavailable evidence in \(section)")
            XCTAssertTrue(text.contains("ship the beta"), text)
            XCTAssertTrue(text.contains("active"), text)
            XCTAssertTrue(text.contains("evidence no longer on this mac"), text)
            for forbidden in ["source", "last touch", "no recorded touch", "next:", "due:", "due today", "overdue", "main chat", "2026"] {
                XCTAssertFalse(text.contains(forbidden), "Unexpected \(forbidden): \(text)")
            }
        }
    }

    @MainActor
    func testMainChatReferenceRendersControlAndResolvesConversationOrChatTab() throws {
        for scopes in [["chat:read", "chat:write"], ["brief:read"]] {
            let directory = try makeTemporaryDirectory()
            defer { try? FileManager.default.removeItem(at: directory) }
            let record = PairedDevice(hubID: "purpose-test-hub", hubName: "Test Mac", hubPublicKey: "",
                                      relayOrigin: "https://purpose-test.invalid", deviceID: "test-phone",
                                      deviceName: "Test iPhone", scopes: scopes,
                                      pairedAt: Date(), counters: SequenceCounters())
            let encoder = JSONEncoder()
            encoder.dateEncodingStrategy = .iso8601
            try encoder.encode(record).write(to: directory.appendingPathComponent("pairing.json"))
            // The metadata grants the control's scope; no session secret is stored, so rendering cannot
            // contact a relay. The routing policy below is the same one the control consumes.
            let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                             briefDirectory: directory.appendingPathComponent("briefs"),
                                             journalURL: directory.appendingPathComponent("writes.json"),
                                             chatDirectory: directory.appendingPathComponent("chats"),
                                             widgetStore: WidgetSnapshotStore(fileURL: directory.appendingPathComponent("widget.json")))
            environment.router.selectedTab = .today
            for sessionID: String? in ["stored-main-chat", nil, ""] {
                let item = BriefItem(title: "Send the release plan", status: "due_today",
                                     chatSessionId: sessionID, chatMessageId: "stored-report")
                var brief = MobileBriefV1()
                brief.sections.commitments = [item]
                let text = try renderedText(BriefPurposeSections(brief: brief) { _ in }
                    .environmentObject(environment.pairing)
                    .environmentObject(environment.router), name: "Existing chat report")
                XCTAssertTrue(text.contains("already in your chat"), text)
                let route = BriefChatReferenceDestination.resolve(item: item, scopes: scopes)
                if scopes.contains("chat:read") {
                    XCTAssertFalse(text.contains("on your computer"), text)
                    if let sessionID, !sessionID.isEmpty {
                        XCTAssertEqual(route, .conversation(sessionID))
                    } else {
                        XCTAssertEqual(route, .chatTab)
                    }
                } else {
                    XCTAssertTrue(text.contains("already in your chat on your computer"), text)
                    XCTAssertEqual(route, .macOnly)
                }
                XCTAssertEqual(environment.router.selectedTab, .today,
                               "Rendering an existing report must not navigate or resend it.")
                XCTAssertNil(environment.chat.openChatID)
                XCTAssertTrue(environment.chat.outgoingMessages(in: "stored-main-chat").isEmpty)
            }
        }
    }

    @MainActor
    func testPrivacyExclusionNoticeCoversPrivateAndPausedActivity() throws {
        var brief = MobileBriefV1()
        brief.limits = BriefLimits(excludedSensitiveItems: 1)
        let text = try renderedText(BriefPurposeSections(brief: brief) { _ in }, name: "Brief privacy exclusions")
        XCTAssertTrue(text.contains("sensitive, private, or paused activity"), text)
        XCTAssertFalse(text.contains("sensitive apps"), text)
    }

    @MainActor
    func testCoveredOpenLoopsDoNotRenderEmptyState() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: directory.appendingPathComponent("briefs"),
                                         journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: directory.appendingPathComponent("chats"))
        var brief = MobileBriefV1()
        let purpose = BriefItem(title: "Ship the companion", refs: ["goal"])
        brief.sections.workingToward = [purpose]
        brief.sections.openLoops = [purpose]
        brief.evidence = [BriefEvidence(ref: "goal", sourceKind: "purpose", sourceId: "goal-id")]
        let text = try renderedText(BriefContentView(brief: brief)
            .environmentObject(environment.actions)
            .environmentObject(environment.pairing).environmentObject(environment)
            .environmentObject(environment.chat).environmentObject(environment.chatAvailability).environmentObject(environment.brief)
            .environmentObject(environment.router), name: "Brief with covered open loops")
        XCTAssertTrue(text.contains("ship the companion"), text)
        XCTAssertFalse(text.contains("no open loops right now"), text)
        XCTAssertFalse(text.contains("priorities and open loops"), text)
    }

    @MainActor
    func testPurposeBriefCardsFitNarrowPhonesAndExpandForAccessibilityText() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: directory.appendingPathComponent("briefs"),
                                         journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: directory.appendingPathComponent("chats"))
        var brief = try decodePurposeBrief()
        // Exercise actual text wrapping independently of the source-chip count.
        brief.sections.workingToward[0].detail = "Reviewed the companion release checklist, compared pairing results, and recorded the remaining setup issues. The notes cover phone reconnection, permission prompts, and the next test build for review."

        func snapshot(width: CGFloat, textSize: DynamicTypeSize, name: String) throws -> CGSize {
            let renderer = ImageRenderer(content: BriefPurposeSections(brief: brief) { _ in }
                .environmentObject(environment.pairing)
                .environmentObject(environment.router)
                .padding(16)
                .frame(width: width)
                .background(YansuTheme.background)
                .environment(\.locale, Locale(identifier: "en_US"))
                .environment(\.dynamicTypeSize, textSize)
                .environment(\.colorScheme, .light))
            renderer.scale = 1
            let image = try XCTUnwrap(renderer.uiImage)
            XCTAssertEqual(image.size.width, width, accuracy: 1)
            XCTAssertGreaterThan(image.size.height, 400, "All three sections should be visible.")
            let attachment = XCTAttachment(image: image)
            attachment.name = name
            attachment.lifetime = .keepAlways
            add(attachment)
            if textSize == .accessibility3 {
                let request = VNRecognizeTextRequest()
                request.recognitionLevel = .accurate
                request.recognitionLanguages = ["en-US"]
                request.usesLanguageCorrection = false
                try VNImageRequestHandler(cgImage: XCTUnwrap(image.cgImage), options: [:]).perform([request])
                let visibleText = (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }
                    .joined(separator: " ").lowercased()
                XCTAssertTrue(visibleText.contains("working toward"), "The purpose heading must wrap without truncating.")
                XCTAssertTrue(visibleText.contains("due or slipping"), "The commitment heading must wrap without truncating.")
            }
            return image.size
        }

        let regular = try snapshot(width: 393, textSize: .large, name: "Purpose brief on iPhone 15")
        let narrow = try snapshot(width: 320, textSize: .large, name: "Purpose brief at narrow width")
        let accessible = try snapshot(width: 320, textSize: .accessibility3,
                                      name: "Purpose brief with accessibility text")
        XCTAssertGreaterThan(narrow.height, regular.height, "Wrapping should expand cards, not clip their text.")
        XCTAssertGreaterThan(accessible.height, narrow.height,
                             "Larger text should expand progress, dates, evidence and the existing-chat reference.")
    }

    private func decodePurposeBrief() throws -> MobileBriefV1 {
        try JSONDecoder().decode(MobileBriefV1.self, from: Data(purposeJSON.utf8))
    }

    @MainActor
    private func renderedText<Content: View>(_ content: Content, name: String) throws -> String {
        let renderer = ImageRenderer(content: content
            .padding(16)
            .frame(width: 393)
            .background(YansuTheme.background)
            .environment(\.locale, Locale(identifier: "en_US"))
            .environment(\.dynamicTypeSize, .large)
            .environment(\.colorScheme, .light))
        renderer.scale = 2
        let image = try XCTUnwrap(renderer.uiImage)
        let attachment = XCTAttachment(image: image)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        let request = VNRecognizeTextRequest()
        request.recognitionLevel = .accurate
        request.recognitionLanguages = ["en-US"]
        request.usesLanguageCorrection = false
        try VNImageRequestHandler(cgImage: XCTUnwrap(image.cgImage), options: [:]).perform([request])
        return (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }
            .joined(separator: " ").lowercased()
    }
}
