package main

// toastOptions is what a notification adds to a plain desktop toast: one
// action button, the same action the phone push offers, and hooks that report
// what the user did so the engagement receipts match the phone's (shown is
// delivered, a click on the body is opened, the button is acted, the close
// control is dismissed). A toast that times out reports nothing. Hooks run on
// their own goroutine and may be nil.
type toastOptions struct {
	ActionLabel string
	// Actions, when set, replaces the one button with every action of the
	// notification's kind (notify v1.0.0 B6), each reported to OnAction by
	// its id. An action other than "open" leaves the app closed.
	Actions  []toastAction
	OnAction func(id string)
	// Tag names what the toast is about (a notice id), so reading it
	// elsewhere can take it away (closeDesktopToastsTagged).
	Tag         string
	OnShown     func()
	OnOpened    func()
	OnActed     func()
	OnDismissed func()
}

// toastAction is one button on a notification.
type toastAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// toastActionOpens says whether pressing an action also opens the app at the
// notification: "open" does; an answer such as Not needed does not.
func toastActionOpens(id string) bool { return id == "" || id == "open" }
