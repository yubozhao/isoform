import { MobileConnectionSettings } from "@/components/mobile-connection-settings";

/**
 * Your phone: the connection status, pairing and paired phones. The server
 * address and what each phone may do sit under Advanced. The brief's
 * schedule and every notification preference live on Briefs and
 * notifications (computer-settings v1.0.0 rule 6).
 */
export function IPhonePage() {
    return <MobileConnectionSettings />;
}
