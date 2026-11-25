package main

import "context"

func (a *App) topicOutboundWaiting(ctx context.Context) map[string]int64 {
	out := map[string]int64{}
	if a == nil || a.store == nil || a.IsClosedEyesModeEnabled() {
		return out
	}
	sends, err := a.store.ListOutboundSendsWaiting(ctx)
	if err != nil {
		return out
	}
	owners, err := a.store.PeopleMergeOwners(ctx)
	if err != nil {
		return out
	}
	cache := newPeopleReadGateCache()
	readableOwners := map[string]bool{}
	for _, owner := range owners {
		readableOwners[owner.ID] = a.peopleMergeOwnerReadable(ctx, owner, cache)
	}
	for _, send := range sends {
		principal, err := a.store.GetPrincipal(ctx, send.PrincipalID)
		if err != nil || !a.peopleProfileAllowsModelRead(ctx, principal, cache) || !a.peopleMetadataReadable(ctx, principal.ID, cache) {
			continue
		}
		if send.Channel == "mail" && (!a.captureAllowed(ctx, "main_chat_send_projection", mailPermissionKey) || !a.peopleAppReadable(ctx, "email")) {
			continue
		}
		if send.Channel == "slack" && !a.peopleAppReadable(ctx, "slack") {
			continue
		}
		names := peopleNamesAfterMerge(principal.ID, principal.DisplayName, principal.Aliases, owners, readableOwners)
		for name := range names {
			key := "person:" + threadNorm(name)
			if send.SentAt > out[key] {
				out[key] = send.SentAt
			}
		}
	}
	return out
}
