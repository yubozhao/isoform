// Screenshots and recordings on the activity page come from the host bridge,
// which serves /api/activity/* only with the per-launch media token. <img>
// and <video> cannot send headers, so the token rides in the "token" query
// parameter. The activity page loads it (GetHostBridgeMediaToken) before it
// sets the bridge port, so every URL built here carries it.

let mediaToken = '';

export function setHostBridgeMediaToken(token: string): void {
    mediaToken = token;
}

export type HostBridgeMediaRoute = 'snapshot' | 'frame' | 'video' | 'audio' | 'recording';

export function hostBridgeMediaURL(
    port: number,
    route: HostBridgeMediaRoute,
    params: Record<string, string | number>,
): string {
    const query = Object.entries(params)
        .map(([key, value]) => `${key}=${encodeURIComponent(String(value))}`)
        .concat(`token=${encodeURIComponent(mediaToken)}`)
        .join('&');
    return `http://127.0.0.1:${port}/api/activity/${route}?${query}`;
}

/** storage_tier of a snapshot row whose screenshot file is gone (activity_store.go). */
export const SNAPSHOT_TIER_MISSING = 'missing';

/**
 * The screenshot URL for a snapshot row, or null when the row is marked
 * image-missing: the row keeps its text, but there is no file to load.
 */
export function snapshotImageURL(
    port: number,
    snapshot: { filePath: string; storageTier?: string },
): string | null {
    if (!snapshot.filePath || snapshot.storageTier === SNAPSHOT_TIER_MISSING) return null;
    return hostBridgeMediaURL(port, 'snapshot', { path: snapshot.filePath });
}
