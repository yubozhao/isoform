package main

import (
	"context"
	"slices"
	"sync"

	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

type mainChatReadMemo struct {
	mu sync.Mutex

	handoffsLoaded bool
	handoffs       []handoff.Entry
	handoffsErr    error
	pausesLoaded   bool
	pauses         []observationPause
	pausesErr      error
	mailLoaded     bool
	mail           []string
	mailErr        error
	threadSource   map[string]bool
	cardSource     map[engagementCardReadReceipt]bool
	voice          proactiveVoiceSourceMemo
}

type mainChatReadMemoKey struct{}

var mainChatReadMemoLoad func(kind string)

func recordMainChatReadMemoLoad(kind string) {
	if mainChatReadMemoLoad != nil {
		mainChatReadMemoLoad(kind)
	}
}

func withMainChatReadMemo(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if mainChatReadMemoFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, mainChatReadMemoKey{}, &mainChatReadMemo{
		threadSource: make(map[string]bool),
		cardSource:   make(map[engagementCardReadReceipt]bool),
	})
}

func mainChatReadMemoFrom(ctx context.Context) *mainChatReadMemo {
	if ctx == nil {
		return nil
	}
	memo, _ := ctx.Value(mainChatReadMemoKey{}).(*mainChatReadMemo)
	return memo
}

func (a *App) handoffEntries(ctx context.Context) ([]handoff.Entry, error) {
	memo := mainChatReadMemoFrom(ctx)
	if memo == nil {
		entries, err := a.handoffStore.List()
		recordMainChatReadMemoLoad("handoffs")
		return entries, err
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if !memo.handoffsLoaded {
		memo.handoffs, memo.handoffsErr = a.handoffStore.List()
		memo.handoffsLoaded = true
		recordMainChatReadMemoLoad("handoffs")
	}
	return memo.handoffs, memo.handoffsErr
}

func mobileBriefMailSubjectsContext(ctx context.Context, a *App) ([]string, error) {
	memo := mainChatReadMemoFrom(ctx)
	if memo == nil {
		subjects, err := mobileBriefMailSubjects(a)
		recordMainChatReadMemoLoad("mail")
		return subjects, err
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if !memo.mailLoaded {
		memo.mail, memo.mailErr = mobileBriefMailSubjects(a)
		memo.mailLoaded = true
		recordMainChatReadMemoLoad("mail")
	}
	return memo.mail, memo.mailErr
}

func (memo *mainChatReadMemo) threadSourceReadable(sourceID string, load func() bool) bool {
	memo.mu.Lock()
	readable, ok := memo.threadSource[sourceID]
	memo.mu.Unlock()
	if ok {
		return readable
	}
	recordMainChatReadMemoLoad("threadSource")
	readable = load()
	memo.mu.Lock()
	if memo.threadSource == nil {
		memo.threadSource = make(map[string]bool)
	}
	if cached, exists := memo.threadSource[sourceID]; exists {
		readable = cached
	} else {
		memo.threadSource[sourceID] = readable
	}
	memo.mu.Unlock()
	return readable
}

func (memo *mainChatReadMemo) cardSourceReadable(record engagementCardReadReceipt, load func() bool) bool {
	memo.mu.Lock()
	readable, ok := memo.cardSource[record]
	memo.mu.Unlock()
	if ok {
		return readable
	}
	recordMainChatReadMemoLoad("cardSource")
	readable = load()
	memo.mu.Lock()
	if memo.cardSource == nil {
		memo.cardSource = make(map[engagementCardReadReceipt]bool)
	}
	if cached, exists := memo.cardSource[record]; exists {
		readable = cached
	} else {
		memo.cardSource[record] = readable
	}
	memo.mu.Unlock()
	return readable
}

// The voice gate reloads a card's sources for every card: the people list, a
// month of mail and the open reminders, once per card. One projection pass
// reads each once (proactiveVoiceSourceMemo), the same way handoffs and mail
// subjects are read once above. Each call gets its own copy of the slice.
type proactiveVoiceSourceMemo struct {
	principalsLoaded bool
	principals       []storage.PrincipalRow
	principalsErr    error
	mailLoaded       bool
	mailSince        int64
	mail             []storage.MailMessageRow
	remindersLoaded  bool
	reminders        []storage.ReminderRow
	remindersErr     error
}

func (a *App) proactiveVoicePrincipals(ctx context.Context) ([]storage.PrincipalRow, error) {
	memo := mainChatReadMemoFrom(ctx)
	if memo == nil {
		return a.store.ListPrincipals(ctx)
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if !memo.voice.principalsLoaded {
		memo.voice.principals, memo.voice.principalsErr = a.store.ListPrincipals(ctx)
		memo.voice.principalsLoaded = memo.voice.principalsErr == nil
		recordMainChatReadMemoLoad("principals")
		if memo.voice.principalsErr != nil {
			return nil, memo.voice.principalsErr
		}
	}
	return slices.Clone(memo.voice.principals), nil
}

// proactiveVoiceMailMessages is ListMailMessages(since). The pass keeps the
// widest read so far; a narrower one is filtered from it in the same order.
func (a *App) proactiveVoiceMailMessages(ctx context.Context, since int64) ([]storage.MailMessageRow, error) {
	memo := mainChatReadMemoFrom(ctx)
	if memo == nil {
		return a.store.ListMailMessages(ctx, since)
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if !memo.voice.mailLoaded || since < memo.voice.mailSince {
		rows, err := a.store.ListMailMessages(ctx, since)
		recordMainChatReadMemoLoad("mailMessages")
		if err != nil {
			return nil, err
		}
		memo.voice.mail, memo.voice.mailSince, memo.voice.mailLoaded = rows, since, true
	}
	out := make([]storage.MailMessageRow, 0, len(memo.voice.mail))
	for _, row := range memo.voice.mail {
		if row.At >= since {
			out = append(out, row)
		}
	}
	return out, nil
}

func (a *App) proactiveVoiceOpenReminders(ctx context.Context) ([]storage.ReminderRow, error) {
	memo := mainChatReadMemoFrom(ctx)
	if memo == nil {
		return a.store.ListReminders(ctx, false)
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if !memo.voice.remindersLoaded {
		memo.voice.reminders, memo.voice.remindersErr = a.store.ListReminders(ctx, false)
		memo.voice.remindersLoaded = memo.voice.remindersErr == nil
		recordMainChatReadMemoLoad("reminders")
		if memo.voice.remindersErr != nil {
			return nil, memo.voice.remindersErr
		}
	}
	return slices.Clone(memo.voice.reminders), nil
}
