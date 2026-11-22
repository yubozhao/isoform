import Foundation
import XCTest
@testable import Yansu

final class HubRequestsTests: XCTestCase {
    func testFindTimeRouteAndAnswer() throws {
        let route = HubRoute.findTime
        XCTAssertEqual(route.method, "GET")
        XCTAssertEqual(route.path, "/calendar/free-time-details")
        XCTAssertEqual(route.query, "")
        let ranges = [PhoneFreeTimeRange(start: "2026-10-30T09:30:00Z", end: "2026-10-30T11:00:00Z"),
                      PhoneFreeTimeRange(start: "2026-11-02T09:00:00Z", end: "2026-11-02T11:00:00Z")]
        let partial = PhoneFreeTimeDetails(ranges: ranges, checkedCalendars: ["Work", "Home"], browserOnly: ["Google"])
        let qualified = try XCTUnwrap(PhoneFindTimeCopy.answer(partial, locale: Locale(identifier: "en")))
        XCTAssertTrue(qualified.contains("Work"))
        XCTAssertTrue(qualified.contains("Google"))
        XCTAssertTrue(qualified.contains("only in your browser"))
        for language in ["en", "zh-Hans"] {
            let answer = try XCTUnwrap(PhoneFindTimeCopy.answer(ranges, locale: Locale(identifier: language)))
            XCTAssertFalse(answer.isEmpty)
        }
        XCTAssertNil(PhoneFindTimeCopy.answer([], locale: Locale(identifier: "en")))
        XCTAssertNil(PhoneFindTimeCopy.answer([PhoneFreeTimeRange(start: "invalid", end: "invalid")], locale: Locale(identifier: "en")))
    }

    func testTaskMotionUsesAdditivePostRoutesWithoutLegacyFallback() async throws {
        for path in ["/offers/find-more", "/handoffs/hf_1/start"] {
            let vault = try makeVault()
            let context = try vault.session()
            let relay = HeldHubTransport()
            let channel = HubChannel(vault: vault, relay: relay)
            let request = Task {
                if path == "/offers/find-more" {
                    let page = try await channel.findMoreOffers()
                    XCTAssertEqual(page.nextAllowedAt, "2026-10-27T20:00:00Z")
                } else {
                    try await channel.runOneTimeWork(id: "hf_1", idempotencyKey: "start-key")
                }
            }
            await relay.waitUntilPolling()
            let sends = await relay.sends
            let envelope = try XCTUnwrap(sends.first)
            let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                         key: context.keys.d2h, hub: context.hubID,
                                                         dev: context.deviceID, dir: "d2h", seq: envelope.seq, kind: "req")
            let object = try XCTUnwrap(JSONSerialization.jsonObject(with: plaintext) as? [String: Any])
            XCTAssertEqual(object["method"] as? String, "POST")
            XCTAssertEqual(object["path"] as? String, path)
            let body = try XCTUnwrap(object["body"] as? String)
            if path.hasSuffix("start") {
                XCTAssertEqual(body, #"{"idempotencyKey":"start-key"}"#)
            } else { XCTAssertEqual(body, "{}") }
            let response = path.hasSuffix("start") ? #"{"ok":true,"data":{"id":"hf_1","sessionId":"run-chat"}}"#
                : #"{"ok":true,"data":{"offers":[],"nextAllowedAt":"2026-10-27T20:00:00Z"}}"#
            try await relay.releasePoll([reply(to: XCTUnwrap(object["id"] as? String), seq: 1, context: context,
                                               status: 200, body: response)])
            try await request.value
            let count = await relay.sends.count
            XCTAssertEqual(count, 1)
            await channel.prepareForPairingChange()
        }
    }

    func testAudioRouteEncodesItsVersionAsOneQueryValue() {
        let version = "2026-10-17-morning|2026-10-17T12:10:00+08:00&other=value"
        let route = HubRoute.briefAudio(id: "2026-10-17-morning", version: version)
        XCTAssertEqual(route.method, "GET")
        XCTAssertEqual(route.path, "/briefs/2026-10-17-morning/audio")
        XCTAssertEqual(route.body, "")
        XCTAssertFalse(route.query.contains("+"), "The computer's form query decoder treats a bare plus as a space")
        let components = URLComponents(string: "https://example.invalid/?\(route.query)")
        XCTAssertEqual(components?.queryItems, [URLQueryItem(name: "version", value: version)])
    }

    func testAudioResponseCannotSubstituteAnotherBriefVersion() throws {
        var brief = MobileBriefV1()
        brief.briefId = "2026-10-17-morning"
        brief.asOf = "2026-10-17T12:10:00Z"
        let expected = BriefAudioResource(briefId: brief.identifier,
                                          version: BriefAudioResource.versionKey(for: brief), audio: BriefAudio())
        let json = String(decoding: try JSONEncoder().encode(expected), as: UTF8.self)
        let response = HubResponse(status: 200, body: "{\"ok\":true,\"data\":\(json)}")
        XCTAssertEqual(try response.briefAudioPayload(for: brief).version, expected.version)
        brief.asOf = "2026-10-17T12:11:00Z"
        XCTAssertThrowsError(try response.briefAudioPayload(for: brief)) { error in
            XCTAssertEqual(error as? BriefAudioError, .versionChanged)
        }
    }

    func testAudioUnavailableReasonsAndScopeRefusalRemainDistinct() {
        let cases: [(String, BriefAudioError)] = [
            ("voice_not_installed", .voiceNotInstalled), ("audio_rendering", .rendering),
            ("audio_unavailable", .notAvailable), ("unknown", .notAvailable)
        ]
        for (reason, expected) in cases {
            let response = HubResponse(status: 404, body: "{\"ok\":false,\"data\":{\"reason\":\"\(reason)\"}}")
            XCTAssertThrowsError(try response.briefAudioPayload(for: MobileBriefV1())) { error in
                XCTAssertEqual(error as? BriefAudioError, expected)
            }
        }
        let forbidden = HubResponse(status: 403, body: #"{"ok":false,"error":"forbidden"}"#)
        XCTAssertThrowsError(try forbidden.briefAudioPayload(for: MobileBriefV1())) { error in
            XCTAssertEqual(error as? HubRequestError, .forbidden)
        }
    }

    func testSourceLinkedMessagesUseTheGuardedRouteAndNeverFallBackOn404() async throws {
        for status in [202, 404] {
            let vault = try makeVault()
            let context = try vault.session()
            let relay = HeldHubTransport()
            let channel = HubChannel(vault: vault, relay: relay)
            let reference = ChatOfferReference(id: "suggestion.notes", version: String(repeating: "a", count: 64))
            let request = Task {
                try await channel.sendMessage(chatID: "main", text: "Edited idea text", offerReferences: [reference],
                                              idempotencyKey: "same-source-key")
            }
            await relay.waitUntilPolling()
            let sends = await relay.sends
            let envelope = try XCTUnwrap(sends.first)
            let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                         key: context.keys.d2h, hub: context.hubID,
                                                         dev: context.deviceID, dir: "d2h", seq: envelope.seq, kind: "req")
            let object = try XCTUnwrap(JSONSerialization.jsonObject(with: plaintext) as? [String: Any])
            XCTAssertEqual(object["path"] as? String, "/chats/main/offer-messages")
            let body = try XCTUnwrap(object["body"] as? String)
            let decoded = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(body.utf8)) as? [String: Any])
            XCTAssertEqual(decoded["text"] as? String, "Edited idea text")
            XCTAssertEqual(decoded["idempotencyKey"] as? String, "same-source-key")
            XCTAssertEqual(decoded["offerReferences"] as? [[String: String]],
                           [["id": reference.id, "version": reference.version]])
            let responseBody = status == 202 ? #"{"ok":true,"data":{"turnId":"turn","seq":1}}"#
                : #"{"ok":false,"error":"route not found"}"#
            try await relay.releasePoll([reply(to: XCTUnwrap(object["id"] as? String), seq: 1, context: context,
                                               status: status, body: responseBody)])
            do {
                let result = try await request.value
                XCTAssertEqual(status, 202)
                XCTAssertEqual(result.turnId, "turn")
            } catch {
                XCTAssertEqual(status, 404)
                XCTAssertEqual(error as? HubRequestError, .rejected(status: 404, message: "route not found"))
            }
            let count = await relay.sends.count
            XCTAssertEqual(count, 1, "An older computer must never receive a text-only fallback")
            await channel.prepareForPairingChange()
        }
    }

    func testBriefMessagesSendOnlyTheOpaqueReferenceAndNeverFallBackOnRefusal() async throws {
        for status in [202, 409, 404] {
            let vault = try makeVault()
            let context = try vault.session()
            let relay = HeldHubTransport()
            let channel = HubChannel(vault: vault, relay: relay)
            let reference = ChatBriefReference(briefId: "morning", briefVersion: "opaque-revision", offerId: "offer-1")
            let request = Task {
                try await channel.sendBriefMessage(chatID: "main", text: "Edited brief opener", reference: reference,
                                                   idempotencyKey: "brief-key")
            }
            await relay.waitUntilPolling()
            let sends = await relay.sends
            let envelope = try XCTUnwrap(sends.first)
            let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                         key: context.keys.d2h, hub: context.hubID,
                                                         dev: context.deviceID, dir: "d2h", seq: envelope.seq, kind: "req")
            let object = try XCTUnwrap(JSONSerialization.jsonObject(with: plaintext) as? [String: Any])
            XCTAssertEqual(object["path"] as? String, "/chats/main/brief-messages")
            let body = try XCTUnwrap(object["body"] as? String)
            let decoded = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(body.utf8)) as? [String: String])
            XCTAssertEqual(decoded, ["text": "Edited brief opener", "idempotencyKey": "brief-key",
                                     "briefId": "morning", "briefVersion": "opaque-revision", "offerId": "offer-1"])
            let responseBody = status == 202 ? #"{"ok":true,"data":{"turnId":"turn","seq":1}}"#
                : status == 409 ? #"{"ok":false,"error":"This brief changed. Refresh and try again."}"#
                : #"{"ok":false,"error":"route not found"}"#
            try await relay.releasePoll([reply(to: XCTUnwrap(object["id"] as? String), seq: 1, context: context,
                                               status: status, body: responseBody)])
            do {
                let result = try await request.value
                XCTAssertEqual(status, 202)
                XCTAssertEqual(result.turnId, "turn")
            } catch {
                XCTAssertNotEqual(status, 202)
                if status == 409 {
                    XCTAssertEqual(error as? HubRequestError, .conflict(message: "This brief changed. Refresh and try again."))
                }
            }
            let count = await relay.sends.count
            XCTAssertEqual(count, 1, "No ordinary-message fallback may detach a brief's context")
            await channel.prepareForPairingChange()
        }
    }

    func testBriefParagraphRequestCarriesOnlyTheReferenceAndIndex() async throws {
        let vault = try makeVault()
        let context = try vault.session()
        let relay = HeldHubTransport()
        let channel = HubChannel(vault: vault, relay: relay)
        let reference = ChatBriefReference(briefId: "morning", briefVersion: "opaque-revision")
        let request = Task {
            try await channel.openBriefParagraph(chatID: "main", reference: reference, paragraph: 2,
                                                 idempotencyKey: "paragraph-key")
        }
        await relay.waitUntilPolling()
        let sends = await relay.sends
        let envelope = try XCTUnwrap(sends.first)
        let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                     key: context.keys.d2h, hub: context.hubID,
                                                     dev: context.deviceID, dir: "d2h", seq: envelope.seq, kind: "req")
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: plaintext) as? [String: Any])
        XCTAssertEqual(object["method"] as? String, "POST")
        XCTAssertEqual(object["path"] as? String, "/chats/main/brief-paragraph")
        let body = try XCTUnwrap(object["body"] as? String)
        let decoded = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(body.utf8)) as? [String: Any])
        XCTAssertEqual(decoded["idempotencyKey"] as? String, "paragraph-key")
        XCTAssertEqual(decoded["briefId"] as? String, "morning")
        XCTAssertEqual(decoded["briefVersion"] as? String, "opaque-revision")
        XCTAssertEqual(decoded["paragraph"] as? Int, 2)
        XCTAssertNil(decoded["text"], "the phone sends no paragraph text")
        try await relay.releasePoll([reply(to: XCTUnwrap(object["id"] as? String), seq: 1, context: context,
                                           status: 200, body: #"{"ok":true,"data":{"posted":true}}"#)])
        try await request.value
        await channel.prepareForPairingChange()
    }

    func testACardSendCarriesTheCardAndItsShownWordsAndTalkPostsOnlyTheReference() async throws {
        let vault = try makeVault()
        let context = try vault.session()
        let relay = HeldHubTransport()
        let channel = HubChannel(vault: vault, relay: relay)
        let reference = ChatBriefReference(briefId: "morning", briefVersion: "opaque-revision",
                                           cardId: "card-plan", cardMessage: "Draft a plan.")
        let send = Task {
            try await channel.sendBriefMessage(chatID: "main", text: "Draft a short plan.", reference: reference,
                                               idempotencyKey: "card-key")
        }
        await relay.waitUntilPolling()
        var sends = await relay.sends
        var envelope = try XCTUnwrap(sends.last)
        var plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                     key: context.keys.d2h, hub: context.hubID,
                                                     dev: context.deviceID, dir: "d2h", seq: envelope.seq, kind: "req")
        var object = try XCTUnwrap(JSONSerialization.jsonObject(with: plaintext) as? [String: Any])
        XCTAssertEqual(object["path"] as? String, "/chats/main/brief-messages")
        var body = try XCTUnwrap(object["body"] as? String)
        XCTAssertEqual(try XCTUnwrap(JSONSerialization.jsonObject(with: Data(body.utf8)) as? [String: String]),
                       ["text": "Draft a short plan.", "idempotencyKey": "card-key", "briefId": "morning",
                        "briefVersion": "opaque-revision", "cardId": "card-plan", "cardMessage": "Draft a plan."])
        try await relay.releasePoll([reply(to: XCTUnwrap(object["id"] as? String), seq: 1, context: context, status: 202,
                                           body: #"{"ok":true,"data":{"turnId":"turn","seq":1}}"#)])
        _ = try await send.value
        await channel.prepareForPairingChange()

        // "Talk about this brief" posts only the opaque reference; the computer writes what is said.
        let talkVault = try makeVault()
        let talkContext = try talkVault.session()
        let talkRelay = HeldHubTransport()
        let talkChannel = HubChannel(vault: talkVault, relay: talkRelay)
        let talk = Task {
            try await talkChannel.openBrief(chatID: "main", reference: ChatBriefReference(briefId: "morning", briefVersion: "opaque-revision"),
                                            idempotencyKey: "talk-key")
        }
        await talkRelay.waitUntilPolling()
        sends = await talkRelay.sends
        envelope = try XCTUnwrap(sends.last)
        plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                 key: talkContext.keys.d2h, hub: talkContext.hubID,
                                                 dev: talkContext.deviceID, dir: "d2h", seq: envelope.seq, kind: "req")
        object = try XCTUnwrap(JSONSerialization.jsonObject(with: plaintext) as? [String: Any])
        XCTAssertEqual(object["method"] as? String, "POST")
        XCTAssertEqual(object["path"] as? String, "/chats/main/brief-opener")
        body = try XCTUnwrap(object["body"] as? String)
        XCTAssertEqual(try XCTUnwrap(JSONSerialization.jsonObject(with: Data(body.utf8)) as? [String: String]),
                       ["idempotencyKey": "talk-key", "briefId": "morning", "briefVersion": "opaque-revision"])
        try await talkRelay.releasePoll([reply(to: XCTUnwrap(object["id"] as? String), seq: 1, context: talkContext,
                                               status: 200, body: #"{"ok":true,"data":{"posted":true}}"#)])
        try await talk.value
        await talkChannel.prepareForPairingChange()
    }

    func testOffersReadIsAnEmptyGetWithNoContentInItsTrace() {
        XCTAssertEqual(HubRoute.offers.method, "GET")
        XCTAssertEqual(HubRoute.offers.path, "/offers")
        XCTAssertEqual(HubRoute.offers.query, "")
        XCTAssertEqual(HubRoute.offers.body, "")
        XCTAssertEqual(HubRoute.offers.traceName, "offers.list")
    }

    func testNonSuccessStatusesMapToTypedErrors() {
        XCTAssertEqual(HubRequestError.rejection(status: 409, message: "changed"), .conflict(message: "changed"))
        XCTAssertEqual(HubRequestError.rejection(status: 409, message: nil),
                       .conflict(message: "Your computer could not apply that because it changed."))
        XCTAssertEqual(HubRequestError.rejection(status: 403, message: "no scope"), .forbidden)
        XCTAssertEqual(HubRequestError.rejection(status: 400, message: "bad request"),
                       .rejected(status: 400, message: "bad request"))
        XCTAssertEqual(HubRequestError.rejection(status: 503, message: nil),
                       .rejected(status: 503, message: "Your computer could not finish that request."))
    }

    func testPayloadTurnsForbiddenAndConflictEnvelopesIntoTypedErrors() {
        let forbidden = HubResponse(status: 403, body: #"{"ok":false,"error":"pair again"}"#)
        XCTAssertThrowsError(try forbidden.payload(RemovedPayload.self)) { error in
            XCTAssertEqual(error as? HubRequestError, .forbidden)
        }

        let conflict = HubResponse(status: 409, body: #"{"ok":false,"error":"memory changed"}"#)
        XCTAssertThrowsError(try conflict.payload(RemovedPayload.self)) { error in
            XCTAssertEqual(error as? HubRequestError, .conflict(message: "memory changed"))
        }
    }

    func testPayloadDecodesASuccessfulWrite() throws {
        let response = HubResponse(status: 201, body: #"{"ok":true,"data":{"id":"hf_1"}}"#)

        let created = try response.payload(CreatedRecordPayload.self)

        XCTAssertEqual(created.id, "hf_1")
    }

    func testASuccessEnvelopeWithoutDataIsMalformed() {
        let response = HubResponse(status: 200, body: #"{"ok":true}"#)
        XCTAssertThrowsError(try response.payload(RemovedPayload.self)) { error in
            XCTAssertEqual(error as? HubRequestError, .malformedResponse)
        }
    }

    func testWritesArePostsToTheirOwnPath() {
        let write = HubRoute.write(path: "/handoffs", body: "{}")
        XCTAssertEqual(write.method, "POST")
        XCTAssertEqual(write.path, "/handoffs")
        XCTAssertEqual(write.query, "")
        XCTAssertEqual(write.body, "{}")
    }

    func testProactiveActionRequestPreservesTheStoredIdentityChoiceAndRetryKey() throws {
        let momentID = "handoff:work:blocked:Check /Users/you/报告 first:100"
        let request = ProactiveActionRequest(idempotencyKey: "retry-key", momentId: momentID, actionId: "not_now")
        let data = try JSONEncoder().encode(request)
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: String])
        XCTAssertEqual(body, ["idempotencyKey": "retry-key", "momentId": momentID, "actionId": "not_now"])
        let response = HubResponse(status: 200, body: #"{"ok":true,"data":{"state":"deferred"}}"#)
        XCTAssertEqual(try response.payload(ProactiveActionResult.self).state, "deferred")
    }

    func testTheForbiddenMessageNoLongerNamesTheDesk() {
        let message = HubRequestError.forbidden.errorDescription ?? ""
        XCTAssertFalse(message.contains("Desk"))
        XCTAssertTrue(message.contains("Pair this phone again"))
    }

    func testPathIdentifiersMustBeSafeForAPath() {
        XCTAssertTrue(Identifiers.isSafePathID("chat-1"))
        XCTAssertTrue(Identifiers.isSafePathID("op_2.3:4"))
        XCTAssertFalse(Identifiers.isSafePathID(""))
        XCTAssertFalse(Identifiers.isSafePathID("a/b"))
        XCTAssertFalse(Identifiers.isSafePathID("a?b"))
        XCTAssertFalse(Identifiers.isSafePathID("a#b"))
        XCTAssertFalse(Identifiers.isSafePathID("a%20b"))
        XCTAssertFalse(Identifiers.isSafePathID("a b"))
        XCTAssertFalse(Identifiers.isSafePathID(String(repeating: "a", count: 129)))
        XCTAssertTrue(Identifiers.isSafePathID(String(repeating: "a", count: 128)))
    }

    func testFirstRequestStartsImmediatelyAndPublishesBeforeAcknowledgement() async throws {
        let vault = try makeVault()
        let startedAt = FirstLoadTrace.now
        let relay = ImmediateHubTransport(context: try vault.session())
        let channel = HubChannel(vault: vault, relay: relay)
        let received = expectation(description: "The answer is available while its ACK is held")
        let request = Task {
            let status = try await channel.status()
            received.fulfill()
            return status
        }
        await relay.waitUntilAcknowledging()
        await fulfillment(of: [received], timeout: 1)
        let sendTimes = await relay.sendTimes
        let waits = await relay.pollWaits
        let acknowledgements = await relay.acknowledgements
        XCTAssertEqual(sendTimes.count, 1)
        XCTAssertLessThan(try XCTUnwrap(sendTimes.first) - startedAt, 1,
                          "The initial send must have no retry delay or preflight round trip")
        XCTAssertEqual(waits, [HubChannel.pollWait], "A ready long-poll returns without an extra probe")
        XCTAssertEqual(acknowledgements, [1])
        XCTAssertEqual(vault.lastAcceptedMessage(), 1, "Persist the receive cursor before rendering")
        await relay.releaseAcknowledgement()
        let status = try await request.value
        XCTAssertEqual(status.hubName, "Test Mac")
        await channel.prepareForPairingChange()
    }

    /// On 2026-11-20 opening the app made eight status requests, each a request, a long-poll and an
    /// acknowledgement at the relay, and the relay refused what came after. The checks share one.
    func testOpeningTheAppAsksTheComputerWhatItCanDoOnce() async throws {
        let vault = try makeVault()
        let relay = ImmediateHubTransport(context: try vault.session())
        let channel = HubChannel(vault: vault, relay: relay)
        async let first = channel.abilities()
        async let second = channel.recordingAbilities()
        async let third = channel.workAvailable()
        _ = try await (first, second, third)
        _ = try await channel.brainDumpAvailable()
        _ = try await channel.noticesReadAvailable()
        _ = try await channel.recordingAbilities()
        let sends = await relay.sendTimes
        XCTAssertEqual(sends.count, 1, "Every check of what the computer can do shares one request")
        await relay.releaseAcknowledgement()
        await channel.prepareForPairingChange()
        await channel.finishPairingChange()
    }

    func testUnpairingDropsWhatTheComputerSaidItCanDo() async throws {
        let vault = try makeVault()
        let relay = ImmediateHubTransport(context: try vault.session())
        let channel = HubChannel(vault: vault, relay: relay)
        _ = try await channel.abilities()
        await relay.releaseAcknowledgement()
        try vault.wipe()
        do {
            _ = try await channel.abilities()
            XCTFail("With no pairing there is no computer to answer for")
        } catch {}
        let sends = await relay.sendTimes
        XCTAssertEqual(sends.count, 1)
    }

    func testPairingAgainAsksTheNewComputerWhatItCanDo() async throws {
        let vault = try makeVault()
        let relay = ImmediateHubTransport(context: try vault.session())
        let channel = HubChannel(vault: vault, relay: relay)
        _ = try await channel.abilities()
        await relay.releaseAcknowledgement()
        await channel.prepareForPairingChange()
        let next = ImmediateHubTransport(context: try storePairing(in: vault))
        await channel.finishPairingChange()
        let other = HubChannel(vault: vault, relay: next)
        _ = try await other.abilities()
        let asked = await next.sendTimes
        XCTAssertEqual(asked.count, 1, "Each channel keeps its own answer")
        await next.releaseAcknowledgement()
        do { _ = try await channel.abilities() } catch {}
        let old = await relay.sendTimes
        XCTAssertEqual(old.count, 2, "After a pairing change the old answer is not reused")
        await channel.prepareForPairingChange()
        await other.prepareForPairingChange()
    }

    func testAFailedAskIsNotKept() async throws {
        let vault = try makeVault()
        let relay = ImmediateHubTransport(context: try vault.session(), failSends: 1)
        let channel = HubChannel(vault: vault, relay: relay)
        do {
            _ = try await channel.abilities()
            XCTFail("The relay refused the request")
        } catch {}
        let status = try await channel.abilities()
        XCTAssertEqual(status.hubName, "Test Mac", "The next ask goes to the computer again")
        await relay.releaseAcknowledgement()
        await channel.prepareForPairingChange()
    }

    func testConcurrentRequestsShareTheResponsePollWithoutWaitingForAcknowledgement() async throws {
        let vault = try makeVault()
        let relay = ImmediateHubTransport(context: try vault.session(), expectedRequests: 2)
        let channel = HubChannel(vault: vault, relay: relay)
        let received = expectation(description: "Both concurrent answers arrive before ACK finishes")
        let requests = Task {
            async let first = channel.status()
            async let second = channel.status()
            let values = try await [first, second]
            received.fulfill()
            return values
        }
        await relay.waitUntilAcknowledging()
        await fulfillment(of: [received], timeout: 1)
        let sendTimes = await relay.sendTimes
        let waits = await relay.pollWaits
        XCTAssertEqual(sendTimes.count, 2)
        XCTAssertEqual(waits, [HubChannel.pollWait])
        XCTAssertEqual(vault.lastAcceptedMessage(), 2)
        await relay.releaseAcknowledgement()
        let statuses = try await requests.value
        XCTAssertEqual(statuses.map(\.hubName), ["Test Mac", "Test Mac"])
        await channel.prepareForPairingChange()
    }

    func testPairingChangeCancelsAndDrainsAcknowledgementBeforeReplacingCredentials() async throws {
        let vault = try makeVault()
        let cancelled = expectation(description: "The background ACK is cancelled")
        let relay = ImmediateHubTransport(context: try vault.session(), onAckCancel: { cancelled.fulfill() })
        let channel = HubChannel(vault: vault, relay: relay)
        _ = try await channel.status()
        await relay.waitUntilAcknowledging()
        let drained = expectation(description: "Pairing waits for the old ACK to finish")
        drained.isInverted = true
        let change = Task {
            await channel.prepareForPairingChange()
            drained.fulfill()
        }
        await fulfillment(of: [cancelled], timeout: 1)
        await fulfillment(of: [drained], timeout: 0.05)
        do {
            _ = try await channel.status()
            XCTFail("Requests must remain suspended while the old ACK drains")
        } catch is CancellationError {}
        await relay.releaseAcknowledgement()
        await change.value
        _ = try storePairing(in: vault)
        await channel.finishPairingChange()
        XCTAssertEqual(vault.lastAcceptedMessage(), 0)
        let acknowledgements = await relay.acknowledgements
        XCTAssertEqual(acknowledgements, [1])
    }

    func testAcknowledgementsCoalesceWhileResponsesContinueToRender() async throws {
        let vault = try makeVault()
        let relay = ImmediateHubTransport(context: try vault.session())
        let channel = HubChannel(vault: vault, relay: relay)
        _ = try await channel.status()
        await relay.waitUntilAcknowledging()
        _ = try await channel.status()
        _ = try await channel.status()
        let firstAcknowledgements = await relay.acknowledgements
        XCTAssertEqual(firstAcknowledgements, [1])
        XCTAssertEqual(vault.lastAcceptedMessage(), 3)

        await relay.releaseAcknowledgement()
        await relay.waitUntilAcknowledging()
        let allAcknowledgements = await relay.acknowledgements
        XCTAssertEqual(allAcknowledgements, [1, 3], "One pending ACK covers both later accepted replies")
        await relay.releaseAcknowledgement()
        await channel.prepareForPairingChange()
    }

    func testFailedAcknowledgementDoesNotStrandANewerQueuedWatermark() async throws {
        let vault = try makeVault()
        let newerAcknowledged = expectation(description: "The newer watermark gets its own ACK attempt")
        let relay = ImmediateHubTransport(context: try vault.session(), failFirstAcknowledgement: true,
                                          onAcknowledgement: { sequence in
                                              if sequence == 3 { newerAcknowledged.fulfill() }
                                          })
        let channel = HubChannel(vault: vault, relay: relay)
        _ = try await channel.status()
        await relay.waitUntilAcknowledging()
        _ = try await channel.status()
        _ = try await channel.status()

        await relay.releaseAcknowledgement()
        await fulfillment(of: [newerAcknowledged], timeout: 1)
        let acknowledgements = await relay.acknowledgements
        XCTAssertEqual(acknowledgements, [1, 3], "Failing the first ACK must not require a fourth response")
        XCTAssertEqual(vault.lastAcceptedMessage(), 3)
        await relay.releaseAcknowledgement()
        await channel.prepareForPairingChange()
    }

    func testAnOldPollCannotAdvanceTheReplacementPairingsCursor() async throws {
        let vault = try makeVault()
        let original = try vault.session()
        let relay = HeldHubTransport()
        let channel = HubChannel(vault: vault, relay: relay)
        let polling = Task { try await channel.pollChatTurns() }
        await relay.waitUntilPolling()

        let replacement = try storePairing(in: vault)
        try await relay.releasePoll([event(for: original, seq: 80)])

        do {
            _ = try await polling.value
            XCTFail("An old poll must not publish events after the pairing changes")
        } catch HubRequestError.notPaired {}
        XCTAssertEqual(vault.current()?.deviceID, replacement.deviceID)
        XCTAssertEqual(vault.lastAcceptedMessage(), 0)
        let acknowledgements = await relay.acknowledgements
        XCTAssertTrue(acknowledgements.isEmpty)
    }

    func testAnOldSendConflictCannotUseTheReplacementPairingsSequence() async throws {
        let vault = try makeVault()
        let original = try vault.session()
        let relay = HeldHubTransport(holdSends: true)
        let channel = HubChannel(vault: vault, relay: relay)
        let sending = Task { try await channel.perform(.status) }
        await relay.waitUntilSending()

        let replacement = try storePairing(in: vault)
        await relay.releaseSend(error: RelayClientError.conflict)

        do {
            _ = try await sending.value
            XCTFail("A send must not retry with an old key and the new pairing's counter")
        } catch HubRequestError.notPaired {}
        let sends = await relay.sends
        XCTAssertEqual(sends.count, 1)
        XCTAssertEqual(sends.first?.dev, original.deviceID)
        XCTAssertEqual(vault.current()?.counters.sendSeq, 0)
        XCTAssertEqual(try vault.nextSendSeq(deviceID: replacement.deviceID), 1)
    }

    func testAnOldSuccessfulSendCannotStartAPollForTheReplacement() async throws {
        let vault = try makeVault()
        let relay = HeldHubTransport(holdSends: true)
        let channel = HubChannel(vault: vault, relay: relay)
        let sending = Task { try await channel.perform(.status) }
        await relay.waitUntilSending()

        _ = try storePairing(in: vault)
        await relay.releaseSend()

        do {
            _ = try await sending.value
            XCTFail("An old request must stop before polling with stale credentials")
        } catch HubRequestError.notPaired {}
        let pollCount = await relay.pollCount
        XCTAssertEqual(pollCount, 0)
        XCTAssertEqual(vault.current()?.counters, SequenceCounters())
    }

    func testJournaledRequestCannotStartOnAnotherHubAfterReservation() async throws {
        let vault = try makeVault()
        let channel = HubChannel(vault: vault)
        do {
            _ = try await HubRequestScope.$hubID.withValue("a-previous-hub") {
                try await channel.status()
            }
            XCTFail("A request reserved for another Mac must not be sent")
        } catch is CancellationError {}
        XCTAssertEqual(vault.current()?.counters.sendSeq, 0)
    }

    func testRemoteOfflineJournaledWriteRetriesWithItsReservedKey() async throws {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let origin = HubTestRemoteOrigin()
        let ticket = "endpointabcdefghijklmnopqrst"
        let hub = TestHub()
        let secrets = InMemorySecretStore()
        let vault = PairingVault(directory: directory, secrets: secrets,
                                 remoteOrigin: { origin.url(for: $0) }, startRemote: { _ in })
        let device = RelayCrypto.makeDeviceKeyPair()
        let record = PairedDevice(hubID: hub.hubID, hubName: "Test Mac", hubPublicKey: hub.hubPublicText,
                                  relayOrigin: hub.relay, deviceID: UUID().uuidString.lowercased(),
                                  deviceName: "Test iPhone", scopes: ["chat:write"], pairedAt: Date(),
                                  counters: SequenceCounters())
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey),
                        bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)), irohTicket: ticket)

        let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("writes.json"),
                                          hubID: { vault.current()?.hubID })
        let writer = JournaledWriter(journal: journal, makeKey: { "stable-write-key" }, now: { Date() })
        let relay = HeldHubTransport()
        let channel = HubChannel(vault: vault, relay: relay)
        let keys = HubRequestWriteKeys()
        let send: (String) async throws -> HubResponse = { key in
            await keys.append(key)
            let body = try JSONSerialization.data(withJSONObject: ["idempotencyKey": key])
            return try await channel.perform(.write(path: "/chat/messages", body: String(decoding: body, as: UTF8.self)))
        }

        let first = await writer.send(.chatMessage, target: "main", choice: "send",
                                      fingerprint: "same-message", send)
        guard case .failed(let error, .keep) = first else {
            return XCTFail("An offline remote write must remain queued, got \(first)")
        }
        XCTAssertEqual(error as? RelayClientError, .offline)
        XCTAssertEqual(JournaledWriter.disposition(for: RelayClientError.offline), .keep)
        let queued = await journal.entries()
        XCTAssertEqual(queued.map(\.key), ["stable-write-key"])
        let sendsWhileOffline = await relay.sends
        XCTAssertTrue(sendsWhileOffline.isEmpty)

        let mappedOrigin = try XCTUnwrap(URL(string: "http://127.0.0.1:41237"))
        origin.set(mappedOrigin, for: ticket)
        let context = try vault.session()
        let retry = Task {
            await writer.send(.chatMessage, target: "main", choice: "send",
                              fingerprint: "same-message", send)
        }
        await relay.waitUntilPolling()
        let sends = await relay.sends
        XCTAssertEqual(sends.count, 1)
        let envelope = try XCTUnwrap(sends.first)
        let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                     key: context.keys.d2h, hub: context.hubID,
                                                     dev: context.deviceID, dir: "d2h",
                                                     seq: envelope.seq, kind: "req")
        let request = try XCTUnwrap(JSONSerialization.jsonObject(with: plaintext) as? [String: Any])
        let requestBody = try XCTUnwrap(request["body"] as? String)
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(requestBody.utf8)) as? [String: String])
        XCTAssertEqual(body["idempotencyKey"], "stable-write-key")
        let response = try reply(to: try XCTUnwrap(request["id"] as? String), seq: 1, context: context)
        await relay.releasePoll([response])

        guard case .sent(let result) = await retry.value else {
            return XCTFail("The recovered remote write should be sent")
        }
        XCTAssertEqual(result.status, 200)
        let attemptedKeys = await keys.snapshot()
        XCTAssertEqual(attemptedKeys, ["stable-write-key", "stable-write-key"])
        let remaining = await journal.entries()
        XCTAssertTrue(remaining.isEmpty)
        let delivered = await relay.sends
        XCTAssertEqual(delivered.count, 1)
        await channel.prepareForPairingChange()
    }

    func testPairingChangeCancelsAndDrainsThePollBeforeAReplacementIsStored() async throws {
        let vault = try makeVault()
        let original = try vault.session()
        let cancelled = expectation(description: "The old long-poll was cancelled")
        let relay = HeldHubTransport(onPollCancel: { cancelled.fulfill() })
        let channel = HubChannel(vault: vault, relay: relay)
        let polling = Task { try await channel.pollChatTurns() }
        await relay.waitUntilPolling()

        let replacementStored = expectation(description: "No replacement before the old poll finishes")
        replacementStored.isInverted = true
        let change = Task {
            await channel.prepareForPairingChange()
            replacementStored.fulfill()
        }
        await fulfillment(of: [cancelled], timeout: 2)
        await fulfillment(of: [replacementStored], timeout: 0.05)
        XCTAssertEqual(vault.current()?.deviceID, original.deviceID)
        do {
            _ = try await channel.perform(.status)
            XCTFail("A new request must not start while the pairing is changing")
        } catch is CancellationError {}

        // A transport can return a frame even after cancellation. It must still be discarded.
        try await relay.releasePoll([event(for: original, seq: 80)])
        await change.value
        do {
            _ = try await polling.value
            XCTFail("The cancelled poll must not publish its last frame")
        } catch is CancellationError {}
        XCTAssertEqual(vault.lastAcceptedMessage(), 0)

        let replacement = try storePairing(in: vault)
        await channel.finishPairingChange()
        let nextPoll = Task { try await channel.pollChatTurns() }
        await relay.waitUntilPolling(count: 2)
        try await relay.releasePoll([event(for: replacement, seq: 1)])
        let events = try await nextPoll.value
        XCTAssertEqual(events.map(\.chatId), [replacement.deviceID])
        XCTAssertEqual(vault.lastAcceptedMessage(), 1)
        await relay.waitUntilAcknowledging()
        let acknowledgements = await relay.acknowledgements
        XCTAssertEqual(acknowledgements, [1])
    }

    func testRetainingTheOriginalPairingDoesNotRestartAnInterruptedSend() async throws {
        let vault = try makeVault()
        let relay = HeldHubTransport(holdSends: true)
        let channel = HubChannel(vault: vault, relay: relay)
        let sending = Task { try await channel.perform(.status) }
        await relay.waitUntilSending()

        await channel.prepareForPairingChange()
        // A failed replacement save retains the same device, but its interrupted requests must still stop.
        await channel.finishPairingChange()
        await relay.releaseSend(error: RelayClientError.conflict)

        do {
            _ = try await sending.value
            XCTFail("Resuming the original pairing must not restart a request from before the change")
        } catch is CancellationError {}
        let sends = await relay.sends
        XCTAssertEqual(sends.count, 1)
        XCTAssertEqual(vault.current()?.counters.sendSeq, 1)
    }

    func testOverlappingPairingChangesStayPausedUntilBothFinish() async throws {
        let vault = try makeVault()
        let relay = HeldHubTransport()
        let channel = HubChannel(vault: vault, relay: relay)
        await channel.prepareForPairingChange()
        await channel.prepareForPairingChange()
        await channel.finishPairingChange()
        do {
            _ = try await channel.perform(.status)
            XCTFail("Finishing one change must not resume another change's suspended channel")
        } catch is CancellationError {}

        await channel.finishPairingChange()
        let poll = Task { try await channel.pollChatTurns() }
        await relay.waitUntilPolling()
        await relay.releasePoll([])
        let events = try await poll.value
        XCTAssertTrue(events.isEmpty)
    }

    func testRepliesToCancelledRequestsCannotEvictAWaitingRequestsReply() async throws {
        let vault = try makeVault()
        let relay = OrphanedReplyTransport(context: try vault.session(), orphans: 64)
        let channel = HubChannel(vault: vault, relay: relay)

        let status = try await channel.perform(.status, timeout: 1).payload(HubStatus.self)

        XCTAssertEqual(status.hubName, "Test Mac")
        XCTAssertEqual(vault.lastAcceptedMessage(), 65, "Unclaimed replies are still accepted for their sequence")
        await channel.prepareForPairingChange()
    }

    func testAnEnvelopeTheVaultCouldNotSaveIsNotAcknowledged() async throws {
        let writes = RecordWrites()
        let vault = try makeVault(writes: writes)
        let session = try vault.session()
        let relay = HeldHubTransport()
        let channel = HubChannel(vault: vault, relay: relay)
        let polling = Task { try await channel.pollChatTurns() }
        await relay.waitUntilPolling()

        writes.allow(1)
        try await relay.releasePoll([event(for: session, seq: 1), event(for: session, seq: 2)])
        let saved = try await polling.value
        XCTAssertEqual(saved.count, 1, "The saved event arrives; the unsaved one stays on the relay")
        await relay.waitUntilAcknowledging()
        let firstAcknowledgements = await relay.acknowledgements
        XCTAssertEqual(firstAcknowledgements, [1], "The relay must keep the envelope this iPhone did not save")
        XCTAssertEqual(vault.lastAcceptedMessage(), 1)

        writes.allow(nil)
        let retry = Task { try await channel.pollChatTurns() }
        await relay.waitUntilPolling(count: 2)
        try await relay.releasePoll([event(for: session, seq: 2)])
        let events = try await retry.value
        XCTAssertEqual(events.count, 1, "The second event arrives once it is saved")
        XCTAssertEqual(vault.lastAcceptedMessage(), 2)
        await channel.prepareForPairingChange()
    }

    func testAReplyThisIPhoneCouldNotSaveFailsOnlyItsOwnRequest() async throws {
        let writes = RecordWrites()
        let vault = try makeVault(writes: writes)
        let session = try vault.session()
        let relay = HeldHubTransport()
        let channel = HubChannel(vault: vault, relay: relay)
        let unsaved = Task { try await channel.perform(.status, timeout: 10) }
        await relay.waitUntilPolling()
        let waiting = Task { try await channel.perform(.status, timeout: 10) }
        var sends = await relay.sends
        for _ in 0..<200 where sends.count < 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
            sends = await relay.sends
        }
        let ids = try sends.map { try requestID(in: $0, context: session) }
        XCTAssertEqual(ids.count, 2)

        writes.allow(0)
        try await relay.releasePoll([reply(to: ids[0], seq: 1, context: session),
                                     reply(to: ids[1], seq: 2, context: session)])
        do {
            _ = try await unsaved.value
            XCTFail("The request whose reply could not be saved must fail")
        } catch {}

        writes.allow(nil)
        var polls = await relay.pollCount
        for _ in 0..<300 where polls < 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
            polls = await relay.pollCount
        }
        XCTAssertEqual(polls, 2, "The other request keeps polling for its own reply")
        guard polls == 2 else {
            await channel.prepareForPairingChange()
            return
        }
        // Neither reply was acknowledged, so the relay sends both again.
        try await relay.releasePoll([reply(to: ids[0], seq: 1, context: session),
                                     reply(to: ids[1], seq: 2, context: session)])
        let status = try await waiting.value.payload(HubStatus.self)
        XCTAssertEqual(status.hubName, "Test Mac", "A request whose reply came after the unsaved one keeps waiting")
        XCTAssertEqual(vault.lastAcceptedMessage(), 2)
        await relay.waitUntilAcknowledging()
        let acknowledgements = await relay.acknowledgements
        XCTAssertEqual(acknowledgements, [2], "Nothing was acknowledged past the unsaved reply")
        await channel.prepareForPairingChange()
    }

    func testASequenceZeroEnvelopeIsSkippedWithoutStoppingThePoll() async throws {
        let vault = try makeVault()
        let session = try vault.session()
        let relay = HeldHubTransport()
        let channel = HubChannel(vault: vault, relay: relay)
        let polling = Task { try await channel.pollChatTurns() }
        await relay.waitUntilPolling()

        try await relay.releasePoll([event(for: session, seq: 0), event(for: session, seq: 1)])
        let events = try await polling.value

        XCTAssertEqual(events.count, 1)
        XCTAssertEqual(vault.lastAcceptedMessage(), 1)
        await channel.prepareForPairingChange()
    }

    private func makeVault(writes: RecordWrites? = nil) throws -> PairingVault {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let vault: PairingVault
        if let writes {
            vault = PairingVault(directory: directory, secrets: InMemorySecretStore(),
                                 writeRecord: { data, url in try writes.write(data, to: url) })
        } else {
            vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        }
        _ = try storePairing(in: vault)
        return vault
    }

    private func storePairing(in vault: PairingVault) throws -> SessionContext {
        let hub = TestHub()
        let device = RelayCrypto.makeDeviceKeyPair()
        let record = PairedDevice(hubID: hub.hubID, hubName: "Test Mac", hubPublicKey: hub.hubPublicText,
                                  relayOrigin: hub.relay, deviceID: UUID().uuidString.lowercased(),
                                  deviceName: "Test iPhone", scopes: ["chat:read"], pairedAt: Date(),
                                  counters: SequenceCounters())
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey),
                        bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
        return try vault.session()
    }

    private func requestID(in envelope: RelayEnvelope, context: SessionContext) throws -> String {
        let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                     key: context.keys.d2h, hub: context.hubID,
                                                     dev: context.deviceID, dir: "d2h", seq: envelope.seq, kind: "req")
        let object = try JSONSerialization.jsonObject(with: plaintext) as? [String: Any]
        return try XCTUnwrap(object?["id"] as? String)
    }

    private func reply(to requestID: String, seq: UInt64, context: SessionContext, status: Int = 200,
                       body: String = #"{"ok":true,"data":{"hubName":"Test Mac"}}"#) throws -> RelayEnvelope {
        let plaintext = try JSONSerialization.data(withJSONObject: [
            "id": requestID, "status": status, "body": body
        ])
        let ciphertext = try RelayCrypto.sealEnvelope(plaintext, key: context.keys.h2d, hub: context.hubID,
                                                      dev: context.deviceID, dir: "h2d", seq: seq, kind: "res")
        return RelayEnvelope(v: RelayCrypto.protocolVersion, hub: context.hubID, dev: context.deviceID,
                             dir: "h2d", seq: seq, kind: "res", ct: RelayCrypto.base64URLEncode(ciphertext))
    }

    private func event(for context: SessionContext, seq: UInt64) throws -> RelayEnvelope {
        let plaintext = try JSONSerialization.data(withJSONObject: ["event": "chat_turn", "chatId": context.deviceID])
        let ciphertext = try RelayCrypto.sealEnvelope(plaintext, key: context.keys.h2d, hub: context.hubID,
                                                      dev: context.deviceID, dir: "h2d", seq: seq, kind: "event")
        return RelayEnvelope(v: RelayCrypto.protocolVersion, hub: context.hubID, dev: context.deviceID,
                             dir: "h2d", seq: seq, kind: "event", ct: RelayCrypto.base64URLEncode(ciphertext))
    }
}

/// Real envelope crypto with an independently held ACK. A request is answered as soon as the expected
/// sends arrive, matching the relay's wake-on-response long-poll rather than sleeping for its wait cap.
private actor ImmediateHubTransport: HubTransport {
    private let context: SessionContext
    private var failSends: Int
    private let expectedRequests: Int
    private let onAckCancel: @Sendable () -> Void
    private let failFirstAcknowledgement: Bool
    private let onAcknowledgement: @Sendable (UInt64) -> Void
    private var responses: [RelayEnvelope] = []
    private var responseWaiter: CheckedContinuation<Void, Never>?
    private var acknowledgement: CheckedContinuation<Void, Never>?
    private var acknowledging: CheckedContinuation<Void, Never>?
    private(set) var sendTimes: [TimeInterval] = []
    private(set) var pollWaits: [Int] = []
    private(set) var acknowledgements: [UInt64] = []

    init(context: SessionContext, expectedRequests: Int = 1, onAckCancel: @escaping @Sendable () -> Void = {},
         failFirstAcknowledgement: Bool = false, onAcknowledgement: @escaping @Sendable (UInt64) -> Void = { _ in },
         failSends: Int = 0) {
        self.failSends = failSends
        self.context = context
        self.expectedRequests = expectedRequests
        self.onAckCancel = onAckCancel
        self.failFirstAcknowledgement = failFirstAcknowledgement
        self.onAcknowledgement = onAcknowledgement
    }

    func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
        if failSends > 0 {
            failSends -= 1
            throw RelayClientError.rateLimited(retryAfter: nil)
        }
        sendTimes.append(FirstLoadTrace.now)
        let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                     key: context.keys.d2h, hub: context.hubID,
                                                     dev: context.deviceID, dir: "d2h",
                                                     seq: envelope.seq, kind: "req")
        let object = try JSONSerialization.jsonObject(with: plaintext) as? [String: Any]
        let requestID = try XCTUnwrap(object?["id"] as? String)
        let reply = try JSONSerialization.data(withJSONObject: [
            "id": requestID, "status": 200, "body": #"{"ok":true,"data":{"hubName":"Test Mac"}}"#
        ])
        let sequence = UInt64(sendTimes.count)
        let ciphertext = try RelayCrypto.sealEnvelope(reply, key: context.keys.h2d, hub: context.hubID,
                                                      dev: context.deviceID, dir: "h2d", seq: sequence, kind: "res")
        responses.append(RelayEnvelope(v: RelayCrypto.protocolVersion, hub: context.hubID, dev: context.deviceID,
                                        dir: "h2d", seq: sequence, kind: "res",
                                        ct: RelayCrypto.base64URLEncode(ciphertext)))
        if sendTimes.count >= expectedRequests {
            responseWaiter?.resume()
            responseWaiter = nil
        }
    }

    func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope] {
        pollWaits.append(wait)
        if sendTimes.count < expectedRequests {
            await withCheckedContinuation { responseWaiter = $0 }
        }
        let ready = responses
        responses.removeAll()
        return ready
    }

    func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {
        acknowledgements.append(upTo)
        let fails = failFirstAcknowledgement && acknowledgements.count == 1
        onAcknowledgement(upTo)
        await withTaskCancellationHandler {
            await withCheckedContinuation { continuation in
                acknowledgement = continuation
                acknowledging?.resume()
                acknowledging = nil
            }
        } onCancel: {
            onAckCancel()
        }
        if fails { throw RelayClientError.offline }
    }

    func waitUntilAcknowledging() async {
        guard acknowledgement == nil else { return }
        await withCheckedContinuation { acknowledging = $0 }
    }

    func releaseAcknowledgement() {
        acknowledgement?.resume()
        acknowledgement = nil
    }
}

private final class HubTestRemoteOrigin: @unchecked Sendable {
    private let lock = NSLock()
    private var origins: [String: URL] = [:]

    func url(for ticket: String) -> URL? {
        lock.lock()
        defer { lock.unlock() }
        return origins[ticket]
    }

    func set(_ url: URL, for ticket: String) {
        lock.lock()
        defer { lock.unlock() }
        origins[ticket] = url
    }
}

private actor HubRequestWriteKeys {
    private var values: [String] = []

    func append(_ key: String) {
        values.append(key)
    }

    func snapshot() -> [String] {
        values
    }
}

/// Lets a test stop the vault from saving its record, the way a locked or full iPhone would.
private final class RecordWrites: @unchecked Sendable {
    private let lock = NSLock()
    private var remaining: Int?

    /// Allows this many more saves; nil allows every save.
    func allow(_ count: Int?) {
        lock.lock()
        defer { lock.unlock() }
        remaining = count
    }

    func write(_ data: Data, to url: URL) throws {
        lock.lock()
        defer { lock.unlock() }
        if let remaining {
            guard remaining > 0 else { throw CocoaError(.fileWriteOutOfSpace) }
            self.remaining = remaining - 1
        }
        try data.write(to: url, options: .atomic)
    }
}

/// Answers the first request, then, on the same page, replies to requests nobody waits for any more: their
/// callers were cancelled or timed out.
private actor OrphanedReplyTransport: HubTransport {
    private let context: SessionContext
    private let orphans: Int
    private var requestID: String?
    private var delivered = false

    init(context: SessionContext, orphans: Int) {
        self.context = context
        self.orphans = orphans
    }

    func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
        let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct),
                                                     key: context.keys.d2h, hub: context.hubID,
                                                     dev: context.deviceID, dir: "d2h",
                                                     seq: envelope.seq, kind: "req")
        let object = try JSONSerialization.jsonObject(with: plaintext) as? [String: Any]
        requestID = try XCTUnwrap(object?["id"] as? String)
    }

    func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope] {
        guard !delivered, let requestID else {
            try await Task.sleep(nanoseconds: 10_000_000)
            return []
        }
        delivered = true
        var replies = [try reply(to: requestID, seq: 1)]
        for index in 0..<orphans {
            replies.append(try reply(to: UUID().uuidString.lowercased(), seq: UInt64(index + 2)))
        }
        return replies
    }

    func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {}

    private func reply(to requestID: String, seq: UInt64) throws -> RelayEnvelope {
        let plaintext = try JSONSerialization.data(withJSONObject: [
            "id": requestID, "status": 200, "body": #"{"ok":true,"data":{"hubName":"Test Mac"}}"#
        ])
        let ciphertext = try RelayCrypto.sealEnvelope(plaintext, key: context.keys.h2d, hub: context.hubID,
                                                      dev: context.deviceID, dir: "h2d", seq: seq, kind: "res")
        return RelayEnvelope(v: RelayCrypto.protocolVersion, hub: context.hubID, dev: context.deviceID,
                             dir: "h2d", seq: seq, kind: "res", ct: RelayCrypto.base64URLEncode(ciphertext))
    }
}

// Returns held work even after cancellation to exercise the session guards independently of URLSession.
private actor HeldHubTransport: HubTransport {
    private let holdSends: Bool
    private let onPollCancel: @Sendable () -> Void
    private var sendResponse: CheckedContinuation<Void, Error>?
    private var pollResponse: CheckedContinuation<[RelayEnvelope], Error>?
    private var sending: CheckedContinuation<Void, Never>?
    private var acknowledging: CheckedContinuation<Void, Never>?
    private var polling: [(Int, CheckedContinuation<Void, Never>)] = []
    private(set) var sends: [RelayEnvelope] = []
    private(set) var pollCount = 0
    private(set) var acknowledgements: [UInt64] = []

    init(holdSends: Bool = false, onPollCancel: @escaping @Sendable () -> Void = {}) {
        self.holdSends = holdSends
        self.onPollCancel = onPollCancel
    }

    func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
        sends.append(envelope)
        guard holdSends else { return }
        try await withCheckedThrowingContinuation { continuation in
            sendResponse = continuation
            sending?.resume()
            sending = nil
        }
    }

    func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope] {
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                pollResponse = continuation
                pollCount += 1
                for (count, waiter) in polling where pollCount >= count { waiter.resume() }
                polling.removeAll { pollCount >= $0.0 }
            }
        } onCancel: {
            onPollCancel()
        }
    }

    func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {
        acknowledgements.append(upTo)
        acknowledging?.resume()
        acknowledging = nil
    }

    func waitUntilAcknowledging() async {
        guard acknowledgements.isEmpty else { return }
        await withCheckedContinuation { acknowledging = $0 }
    }

    func waitUntilSending() async {
        guard sends.isEmpty else { return }
        await withCheckedContinuation { sending = $0 }
    }

    func waitUntilPolling(count: Int = 1) async {
        guard pollCount < count else { return }
        await withCheckedContinuation { polling.append((count, $0)) }
    }

    func releaseSend(error: Error? = nil) {
        if let error {
            sendResponse?.resume(throwing: error)
        } else {
            sendResponse?.resume()
        }
        sendResponse = nil
    }

    func releasePoll(_ envelopes: [RelayEnvelope]) {
        pollResponse?.resume(returning: envelopes)
        pollResponse = nil
    }
}
