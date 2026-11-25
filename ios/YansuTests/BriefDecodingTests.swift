import Foundation
import SwiftUI
import XCTest
@testable import Yansu

final class BriefDecodingTests: XCTestCase {
    /// The generation record of a brief made before its slot is read (P157).
    func testMadeAheadGenerationIsDecoded() throws {
        let json = #"{"briefId":"2026-11-08-morning","generation":{"reason":"made_ahead","scheduledAt":"2026-11-08T14:50:00Z","generatedAt":"2026-11-07T07:20:00Z","message":""}}"#
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(json.utf8))
        XCTAssertEqual(brief.generation?.reason, "made_ahead")
    }

    func testCommitmentFindTimeCardOpensTheAnswer() {
        let card = MobileBriefCard(id: "item:commitments:q4", kind: "item", mode: "send", label: "Find time for Q4",
                                   message: "Where do I stand on Q4?", itemKey: "commitments:q4")
        XCTAssertTrue(BriefCardsView.isFindTimeCard(card))
        var current = card
        current.itemKey = "commitments.79bc12"
        XCTAssertTrue(BriefCardsView.isFindTimeCard(current))
        XCTAssertTrue(isCommitmentBriefItemKey("commitments-1"))
        XCTAssertFalse(isCommitmentBriefItemKey("workingToward.79bc12"))
        var other = card
        other.label = "Draft the plan."
        XCTAssertFalse(BriefCardsView.isFindTimeCard(other))
    }

    func testCachedHoldQuestionAndAnswersAreWithheld() throws {
        for question in ["Hold Monday morning for the plan?", "周一上午留给计划？"] {
            let json = """
            {"narrative":[{"text":"The plan is due."},{"text":"\(question)"}],
             "cards":[{"id":"hold","kind":"action","mode":"send","message":"Hold it"}],
             "conversationKey":"old", "audio":{"mime":"audio/wav","bytes":3}}
            """
            let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(json.utf8))
            XCTAssertEqual(brief.narrative.map(\.text), ["The plan is due."])
            XCTAssertTrue(brief.cards.isEmpty)
            XCTAssertNil(brief.audio)
            XCTAssertNil(brief.conversationKey)
        }
    }

    func testCachedFeedSectionsAndEvidenceDropPlaceholderCopy() throws {
        let raw = #"{"briefId":"cached","localDate":"2026-10-23","sections":{"moved":[{"title":"Reply to Name?","refs":["e1"]},{"title":"Draft for [CONTACT_NAME]","refs":["e1"]},{"title":"Review [RFC] retry policy","detail":"Ready.\nReply to Name?","nextStep":"Draft for [CONTACT_NAME]","attentionReason":"Reply to Name?","refs":["e1"]}],"workingToward":[{"title":"Draft for [CONTACT_NAME]"}],"commitments":[{"title":"Reply to Name?"}],"waitingOnYou":[{"title":"Reply to Name?"}]},"evidence":[{"ref":"e1","label":"Draft for [CONTACT_NAME]","sourceKind":"history","sourceId":"h1"}]}"#
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(raw.utf8))
        XCTAssertEqual(brief.sections.moved.map(\.title), ["Review [RFC] retry policy"])
        XCTAssertEqual(brief.sections.moved.first?.detail, "Ready.")
        XCTAssertEqual(brief.sections.moved.first?.nextStep, "")
        XCTAssertEqual(brief.sections.moved.first?.attentionReason, "")
        XCTAssertTrue(brief.sections.workingToward.isEmpty)
        XCTAssertTrue(brief.sections.commitments.isEmpty)
        XCTAssertTrue(brief.sections.waitingOnYou.isEmpty)
        XCTAssertEqual(brief.evidence.first?.label, "")
        XCTAssertEqual(brief.evidence.first?.sourceId, "h1", "Cleaning copy keeps source provenance")
        let cards = FeedAssembly.days(from: [brief], today: "2026-10-23").flatMap(\.cards)
        XCTAssertEqual(cards.map(\.item.title), ["Review [RFC] retry policy"])
        XCTAssertEqual(cards.first?.sources.first?.label, "")
    }

    func testRealBracketedCopyRemainsReadable() throws {
        for text in ["Review [RFC] retry policy", "Reply to Dana <dana@example.com>", "The API returns [1, 2, 3]", "Review [MAX_RETRIES]", "Reply to Dana <person@example.com>"] {
            let encoded = try JSONEncoder().encode(MobileBriefV1(narrative: [BriefParagraph(text: text)],
                sections: BriefSections(moved: [BriefItem(title: text)]), evidence: [BriefEvidence(label: text)]))
            let brief = try JSONDecoder().decode(MobileBriefV1.self, from: encoded)
            XCTAssertEqual(brief.narrative.first?.text, text)
            XCTAssertEqual(brief.sections.moved.first?.title, text)
            XCTAssertEqual(brief.evidence.first?.label, text)
        }
    }

    func testOnRequestBriefUsesConfiguredDisplaySlot() throws {
        let raw = #"{"slot":"on_request","displaySlot":"evening","asOf":"2026-10-23T00:11:00Z","timezone":"UTC"}"#
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(raw.utf8))
        XCTAssertEqual(brief.slotLabel, String(localized: "Evening brief"))
        let encoded = try JSONEncoder().encode(brief)
        XCTAssertEqual(try JSONDecoder().decode(MobileBriefV1.self, from: encoded).displaySlot, "evening")
    }

    func testLegacyRequestsHaveSlotNames() {
        let midnight = RelayTime.parse("2026-10-23T00:11:00Z")!
        let morning = RelayTime.parse("2026-10-23T09:00:00Z")!
        XCTAssertEqual(briefSlotLabel(slot: "on_request", asOf: midnight, timezone: "UTC"), String(localized: "Evening brief"))
        XCTAssertEqual(briefSlotLabel(slot: "on_request", asOf: morning, timezone: "UTC"), String(localized: "Morning brief"))
    }

    func testCachedPlaceholderCopyAndItsChipsAreDropped() throws {
        let raw = #"{"narrative":[{"text":"The Meridian proposal is ready.","refs":["e1"]},{"text":"Want me to reply to Name?","refs":["e1"]},{"text":"要我给Name起草回复吗？","refs":["e1"]}],"cards":[{"id":"q","kind":"question","mode":"send","message":"Draft a reply","paragraph":1}],"conversationKey":"old"}"#
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(raw.utf8))
        XCTAssertEqual(brief.narrative.count, 1)
        XCTAssertEqual(brief.narrative.first?.text, "The Meridian proposal is ready.")
        XCTAssertTrue(brief.cards.isEmpty)
        XCTAssertNil(brief.conversationKey)
    }

    func testNonTokenPrivacyMarkersRemainReadable() throws {
        let raw = #"{"narrative":[{"text":"The order [masked] is ready. [...]","refs":["e1"]}]}"#
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(raw.utf8))
        XCTAssertEqual(brief.narrative.count, 1)
        XCTAssertEqual(brief.narrative.first?.text, "The order [masked] is ready. [...]")
    }

    func testCachedRedactionTokensAndTheirRecordingAreWithheld() throws {
        for token in ["[REDACTED:account_number]sed", "[redacted:future_kind]", "[REDACTED:account_number"] {
            let raw: [String: Any] = [
                "narrative": [["text": "The contract \(token) out."]],
                "cards": [["id": "q", "kind": "question", "mode": "send", "message": token]],
                "offers": [["id": "offer", "label": token, "prompt": token]],
                "sections": ["moved": [["title": token]]],
                "audio": ["mime": "audio/wav", "bytes": 1], "conversationKey": "old"
            ]
            let brief = try JSONDecoder().decode(MobileBriefV1.self, from: JSONSerialization.data(withJSONObject: raw))
            XCTAssertTrue(brief.narrative.isEmpty)
            XCTAssertTrue(brief.cards.isEmpty)
            XCTAssertTrue(brief.offers.isEmpty)
            XCTAssertTrue(brief.sections.moved.isEmpty)
            XCTAssertNil(brief.audio)
            XCTAssertNil(brief.conversationKey)
        }
    }

    func testLocalizedSanitizedBriefRemainsReadable() throws {
        for text in ["Check an account number.", "请核对一个账号。"] {
            let raw = try JSONSerialization.data(withJSONObject: ["narrative": [["text": text]]])
            let brief = try JSONDecoder().decode(MobileBriefV1.self, from: raw)
            XCTAssertEqual(brief.narrative.first?.text, text)
        }
    }

    private let fullJSON = """
    {
      "version": 1,
      "briefId": "2026-10-09-morning",
      "slot": "morning",
      "localDate": "2026-10-09",
      "timezone": "America/Los_Angeles",
      "windowStart": "2026-10-08T18:00:00Z",
      "asOf": "2026-10-09T07:00:12Z",
      "hubName": "Alex's MacBook Pro",
      "coldStart": false,
      "narrativeStatus": "ok",
      "narrative": [{"text": "You moved the launch plan forward.", "refs": ["e1", "e2"]}],
      "needsYouCount": 2,
      "sections": {
        "moved": [{"id": "m1", "title": "Launch plan", "detail": "Edited the outline", "status": "done", "at": "2026-10-09T06:30:00Z", "kind": "history", "refs": ["e1"]}],
        "doing": [],
        "watching": [{"id": "find1", "title": "Useful reading", "kind": "find", "url": "https://example.com/reading?source=brief#overview"}],
        "meetings": [{"id": "calendar1", "title": "Design review", "kind": "calendar", "url": "https://meet.example.com/review"},
                     {"id": "day1", "title": "The shape of today", "kind": "calendar_day"}],
        "people": [],
        "openLoops": [{"id": "o1", "title": "Send pricing", "detail": "", "status": "open", "at": "2026-10-08T20:00:00Z", "kind": "commitment", "refs": ["e2"]}],
        "learned": [], "deliverables": [], "schedules": []
      },
      "dayShape": {"activeMinutes": 312, "hours": [{"hour": 9, "activeMinutes": 42}], "topApps": [{"name": "Figma", "activeMinutes": 90}],
                   "firstActive": "2026-10-08T09:05:00-07:00", "lastActive": "2026-10-08T23:30:00-07:00", "breakMinutes": 45},
      "counts": {"openLoopsByKind": {"commitment": 3}, "selfUpdatesPending": 1, "learningItems": 2, "needsYou": 2},
      "evidence": [{"ref": "e1", "sourceKind": "history", "sourceId": "h1", "label": "Launch plan doc", "observedAt": "2026-10-09T06:29:00Z"}],
      "limits": {"itemsPerSection": 8, "evidenceRefs": 60, "truncatedSections": []},
      "audio": {"mime": "audio/mp4", "bytes": 345678, "durationSeconds": 95.4,
                "attachment": {"id": "7a4f7d3e-1b2c-4d5e-8f90-123456789abc", "key": "a2V5", "nonce": "bm9uY2U"}}
    }
    """

    func testAudioDecodesWithItsAttachmentAndIsOptional() throws {
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(fullJSON.utf8))
        let audio = try XCTUnwrap(brief.audio)
        XCTAssertEqual(audio.mime, "audio/mp4")
        XCTAssertEqual(audio.bytes, 345678)
        XCTAssertEqual(audio.durationLabel, "1:35")
        XCTAssertEqual(audio.fileExtension, "m4a")
        XCTAssertEqual(audio.attachment?.id, "7a4f7d3e-1b2c-4d5e-8f90-123456789abc")
        XCTAssertEqual(audio.attachment?.key, "a2V5")

        let without = try JSONDecoder().decode(MobileBriefV1.self, from: Data("{\"version\":1}".utf8))
        XCTAssertNil(without.audio)
        // A malformed audio object never fails the brief.
        let malformed = try JSONDecoder().decode(MobileBriefV1.self, from: Data("{\"version\":1,\"audio\":\"nope\"}".utf8))
        XCTAssertNil(malformed.audio)
        XCTAssertNil(BriefAudio(mime: "audio/wav", bytes: 1, durationSeconds: 0).durationLabel)
        XCTAssertEqual(BriefAudio(mime: "audio/wav").fileExtension, "wav")
    }

    func testFullBriefDecodesEveryDocumentedField() throws {
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(fullJSON.utf8))
        XCTAssertEqual(brief.briefId, "2026-10-09-morning")
        XCTAssertEqual(brief.slot, "morning")
        XCTAssertEqual(brief.hubName, "Alex's MacBook Pro")
        XCTAssertEqual(brief.narrative.count, 1)
        XCTAssertEqual(brief.narrative.first?.refs, ["e1", "e2"])
        XCTAssertEqual(brief.sections.moved.first?.title, "Launch plan")
        XCTAssertEqual(brief.sections.openLoops.first?.kind, "commitment")
        XCTAssertEqual(brief.dayShape?.hours.first?.activeMinutes, 42)
        XCTAssertEqual(brief.dayShape?.topApps.first?.name, "Figma")
        XCTAssertEqual(brief.dayShape?.firstActive, "2026-10-08T09:05:00-07:00")
        XCTAssertEqual(brief.dayShape?.lastActive, "2026-10-08T23:30:00-07:00")
        XCTAssertEqual(brief.dayShape?.firstActiveDate, RelayTime.parse("2026-10-08T16:05:00Z"))
        XCTAssertEqual(brief.dayShape?.lastActiveDate, RelayTime.parse("2026-10-09T06:30:00Z"))
        XCTAssertEqual(brief.dayShape?.breakMinutes, 45)
        XCTAssertEqual(brief.sections.watching.first?.url, "https://example.com/reading?source=brief#overview")
        XCTAssertEqual(brief.sections.watching.first?.webURL?.absoluteString, "https://example.com/reading?source=brief#overview")
        XCTAssertEqual(brief.sections.meetings.first?.webURL?.host, "meet.example.com")
        XCTAssertEqual(brief.sections.meetings.last?.kind, "calendar_day")
        XCTAssertEqual(brief.counts?.openLoopsByKind["commitment"], 3)
        XCTAssertEqual(brief.needsYouCount, 2, "no longer shown, but still decoded")
        XCTAssertEqual(brief.counts?.needsYou, 2)
        XCTAssertEqual(brief.evidence.count, 1)
        XCTAssertEqual(brief.limits?.itemsPerSection, 8)
        XCTAssertNotNil(brief.asOfDate)
        XCTAssertFalse(brief.isColdStart)
    }

    func testMinimalBriefDecodesWithEmptyArraysAndNoOptionalBlocks() throws {
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data("{\"briefId\":\"2026-10-09-evening\"}".utf8))
        XCTAssertEqual(brief.identifier, "2026-10-09-evening")
        XCTAssertTrue(brief.narrative.isEmpty)
        XCTAssertTrue(brief.sections.isEmpty)
        XCTAssertTrue(brief.evidence.isEmpty)
        XCTAssertNil(brief.dayShape)
        XCTAssertNil(brief.counts)
        XCTAssertNil(brief.asOfDate)
        XCTAssertNil(brief.needsYouCount)
    }

    func testUnknownFieldsAreIgnoredAtEveryLevel() throws {
        let json = """
        {"briefId": "b1", "futureTopLevel": {"x": 1}, "sections": {"moved": [{"id": "m", "title": "T", "score": 0.4, "extra": [1, 2]}], "futureSection": []},
         "dayShape": null, "limits": {"itemsPerSection": 8, "newLimit": "value"}}
        """
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(json.utf8))
        XCTAssertEqual(brief.sections.moved.first?.title, "T")
        XCTAssertNil(brief.dayShape)
        XCTAssertEqual(brief.limits?.itemsPerSection, 8)
    }

    func testBriefRoundTripsThroughTheCacheEncoding() throws {
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(fullJSON.utf8))
        let encoded = try JSONEncoder().encode(brief)
        XCTAssertEqual(try JSONDecoder().decode(MobileBriefV1.self, from: encoded), brief)
    }

    func testOlderAndNullBriefFieldsKeepDecoding() throws {
        for json in [
            """
            {"sections":{"watching":[{"title":"Older find"}]},"dayShape":{"activeMinutes":12}}
            """,
            """
            {"sections":{"watching":[{"title":"Older find","url":null}]},
             "dayShape":{"activeMinutes":12,"firstActive":null,"lastActive":null,"breakMinutes":null}}
            """
        ] {
            let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(json.utf8))
            XCTAssertNil(brief.sections.watching.first?.url)
            XCTAssertNil(brief.sections.watching.first?.webURL)
            let shape = try XCTUnwrap(brief.dayShape)
            XCTAssertEqual(shape.activeMinutes, 12)
            XCTAssertNil(shape.firstActive)
            XCTAssertNil(shape.lastActive)
            XCTAssertNil(shape.breakMinutes)
            XCTAssertTrue(shape.hours.isEmpty)
            XCTAssertTrue(shape.topApps.isEmpty)
        }
    }

    func testBriefLinksOnlyOpenHTTPSWithAHost() throws {
        for value in ["https://example.com/find", "HTTPS://example.com/meeting"] {
            XCTAssertNotNil(BriefItem(url: value).webURL, value)
        }
        for value in ["http://example.com", "isoform://pair", "yansu://pair", "javascript:alert(1)", "file:///tmp/find",
                      "mailto:person@example.com", "//example.com/find", "https:find", "https:///", ""] {
            let data = try JSONSerialization.data(withJSONObject: ["url": value])
            let item = try JSONDecoder().decode(BriefItem.self, from: data)
            XCTAssertEqual(item.url, value, "Unsupported links remain readable in cached briefs")
            XCTAssertNil(item.webURL, value)
        }
    }

    func testMalformedDayTimestampsDoNotHideTheRestOfTheShape() throws {
        let json = """
        {"dayShape":{"activeMinutes":12,"firstActive":"unknown","lastActive":"","breakMinutes":0}}
        """
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(json.utf8))
        let shape = try XCTUnwrap(brief.dayShape)
        XCTAssertEqual(shape.activeMinutes, 12)
        XCTAssertNil(shape.firstActiveDate)
        XCTAssertNil(shape.lastActiveDate)
        XCTAssertEqual(shape.breakMinutes, 0)
    }

    @MainActor
    func testDayMarksUseTheMacTimezone() throws {
        let date = try XCTUnwrap(RelayTime.parse("2026-10-08T16:05:00Z"))
        let locale = Locale(identifier: "en_GB")
        XCTAssertEqual(BriefContentView.dayTime(date, timezone: "America/Los_Angeles", locale: locale), "09:05")
        XCTAssertEqual(BriefContentView.dayTime(date, timezone: "America/New_York", locale: locale), "12:05")
    }

    @MainActor
    func testBriefWithLinksAndDayMarksRendersForReview() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: directory.appendingPathComponent("briefs"),
                                         journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: directory.appendingPathComponent("chats"))
        var brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(fullJSON.utf8))
        brief.audio = nil
        // The same objects YansuApp gives every screen, so the brief renders as it does in the app.
        let renderer = ImageRenderer(content: BriefContentView(brief: brief)
            .environmentObject(environment)
            .environmentObject(environment.router)
            .environmentObject(environment.firstRun)
            .environmentObject(environment.brief)
            .environmentObject(environment.briefOnScreen)
            .environmentObject(environment.today)
            .environmentObject(environment.tasks)
            .environmentObject(environment.actions)
            .environmentObject(environment.chat).environmentObject(environment.chatAvailability)
            .environmentObject(environment.pairing)
            .environmentObject(environment.push)
            .environmentObject(environment.notifications)
            .environmentObject(environment.keepAwake)
            .padding(16)
            .frame(width: 393)
            .background(YansuTheme.background)
            .environment(\.colorScheme, .light))
        let attachment = XCTAttachment(image: try XCTUnwrap(renderer.uiImage))
        attachment.name = "Brief links and day marks"
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    func testEvidenceResolvesRefsAndUsesProductLabels() throws {
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(fullJSON.utf8))
        let sources = brief.sources(for: ["e1", "missing"])
        XCTAssertEqual(sources.count, 1)
        XCTAssertEqual(sources.first?.sourceLabel, "Activity")
        XCTAssertEqual(BriefEvidence(sourceKind: "find").sourceLabel, "Find")
        XCTAssertEqual(BriefEvidence(sourceKind: "calendar").sourceLabel, "Calendar")
        XCTAssertEqual(BriefEvidence(sourceKind: "calendar_day").sourceLabel, "Day overview")
        XCTAssertEqual(BriefEvidence(sourceKind: "initiative").sourceLabel, "Source")
        XCTAssertEqual(BriefEvidence(sourceKind: "self_update").sourceLabel, "Source")
        XCTAssertEqual(BriefEvidence(sourceKind: "purpose").sourceLabel, "Goal")
        XCTAssertEqual(BriefEvidence(sourceKind: "unknown_kind").sourceLabel, "Source")
    }

    func testOpenLoopKindsGetReadableLabels() {
        XCTAssertEqual(BriefItem.kindLabel("commitment"), "Commitments")
        XCTAssertEqual(BriefItem.kindLabel("follow_up"), "Follow-ups")
        XCTAssertEqual(BriefItem.kindLabel("find"), "Finds")
        XCTAssertEqual(BriefItem.kindLabel("calendar"), "Calendar")
        XCTAssertEqual(BriefItem.kindLabel("calendar_day"), "Day overview")
        XCTAssertEqual(BriefItem.kindLabel("waiting_on_partner"), "Waiting on partner")
        XCTAssertEqual(BriefItem.kindLabel(nil), "Other")
    }

    func testSummaryListDecodesMetadataOnly() throws {
        let json = "[{\"briefId\":\"2026-10-08-evening\",\"slot\":\"evening\",\"localDate\":\"2026-10-08\",\"asOf\":\"2026-10-08T18:00:00Z\",\"narrativeStatus\":\"skipped\",\"futureField\":true}]"
        let summaries = try JSONDecoder().decode([BriefSummary].self, from: Data(json.utf8))
        XCTAssertEqual(summaries.count, 1)
        XCTAssertEqual(summaries.first?.id, "2026-10-08-evening")
        XCTAssertNotNil(summaries.first?.asOfDate)
    }

    func testItemKeysDecodeAndBriefsStoredBeforeKeysHaveNone() throws {
        let json = """
        {"sections": {"moved": [{"id": "moved-1", "key": "moved.0e4021db1056423d", "title": "Filed the report", "refs": ["e1"]}],
                      "learned": [{"id": "learned-1", "title": "Prefers short replies", "refs": ["e2"]}]}}
        """
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data(json.utf8))
        XCTAssertEqual(brief.sections.moved.first?.key, "moved.0e4021db1056423d")
        XCTAssertNil(brief.sections.learned.first?.key)
        // The cache writes the key back, so a cached brief keeps it.
        let cached = try JSONDecoder().decode(MobileBriefV1.self, from: JSONEncoder().encode(brief))
        XCTAssertEqual(cached.sections.moved.first?.key, "moved.0e4021db1056423d")
        let full = try JSONDecoder().decode(MobileBriefV1.self, from: Data(fullJSON.utf8))
        XCTAssertNil(full.sections.moved.first?.key)
    }
}

private extension BriefEvidence {
    init(sourceKind: String) {
        self.init(ref: nil, sourceKind: sourceKind, sourceId: nil, label: nil, observedAt: nil)
    }
}
