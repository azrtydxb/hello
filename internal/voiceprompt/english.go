package voiceprompt

// english is the default language. The wording follows the key layout of the
// retrieval menus (docs/voicemail.md): keep the two in step.
var english = map[Key]string{
	PINEnter:   "Please enter your PIN, followed by the pound key.",
	PINInvalid: "That PIN is not correct. Please try again.",
	PINLocked:  "Too many wrong attempts. Goodbye.",
	NoMailbox:  "There is no voicemail box for this extension. Goodbye.",
	Goodbye:    "Goodbye.",
	InvalidKey: "Sorry, that is not an option.",

	Counts:               "You have {new} and {saved}.",
	CountNew:             "{n} new messages",
	CountNew + ".one":    "1 new message",
	CountNew + ".zero":   "no new messages",
	CountSaved:           "{n} saved messages",
	CountSaved + ".one":  "1 saved message",
	CountSaved + ".zero": "no saved messages",

	MainMenu:    "To hear your new messages, press 1. To hear your saved messages, press 2. For mailbox options, press 0.",
	NoNew:       "You have no new messages.",
	NoSaved:     "You have no saved messages.",
	NewFolder:   "Your new messages.",
	SavedFolder: "Your saved messages.",

	MsgHeader: "Message {n}, from {who}, {when}.",
	MsgMenu: "To replay this message, press 1. For the next message, press 2. To delete it, press 3. " +
		"To save it, press 9. For the previous message, press star. To skip it, press pound. " +
		"For mailbox options, press 0.",
	MsgMenuShort: "Press 1 to replay, 2 for next, 3 to delete, 9 to save, or 0 for options.",
	MsgDeleted:   "Message deleted.",
	MsgSaved:     "Message saved.",
	MsgNoPrev:    "There is no previous message.",
	MsgEnd:       "End of messages.",
	MsgUnavail:   "That message could not be played.",

	WhoUnknown:   "an unknown caller",
	WhoExtension: "extension {digits}",
	WhoMailbox:   "this voicemail box",
	WordPlus:     "plus",
	WhenToday:    "today at {time}",
	WhenYday:     "yesterday at {time}",
	WhenWeekday:  "on {weekday} at {time}",
	WhenDate:     "on {day} {month} at {time}",
	WhenDateYear: "on {day} {month} {year} at {time}",

	weekdayPfx + "0": "Sunday", weekdayPfx + "1": "Monday", weekdayPfx + "2": "Tuesday",
	weekdayPfx + "3": "Wednesday", weekdayPfx + "4": "Thursday", weekdayPfx + "5": "Friday",
	weekdayPfx + "6": "Saturday",
	monthPfx + "1":   "January", monthPfx + "2": "February", monthPfx + "3": "March",
	monthPfx + "4": "April", monthPfx + "5": "May", monthPfx + "6": "June",
	monthPfx + "7": "July", monthPfx + "8": "August", monthPfx + "9": "September",
	monthPfx + "10": "October", monthPfx + "11": "November", monthPfx + "12": "December",

	OptMenu: "Mailbox options. To record your unavailable greeting, press 1. To record your busy greeting, press 2. " +
		"To record your name, press 3. To change your PIN, press 4. To go back to the default greeting, press 5. " +
		"To return, press star.",
	RecUnavail:   "After the tone, record your greeting. Press pound when finished.",
	RecBusy:      "After the tone, record your busy greeting. Press pound when finished.",
	RecName:      "After the tone, say your name. Press pound when finished.",
	RecReview:    "Here is what you recorded.",
	RecConfirm:   "Press 1 to keep it, 2 to re-record, or 3 to cancel.",
	RecSaved:     "Your recording has been saved.",
	RecCancelled: "Cancelled. Nothing has changed.",
	RecTooShort:  "That recording was too short.",
	RecFailed:    "Sorry, your recording could not be saved.",
	RevertDone:   "Your greetings are back to the default.",

	PINNew:        "Enter your new PIN, at least four digits, followed by the pound key.",
	PINConfirm:    "Enter it again to confirm, followed by the pound key.",
	PINMismatch:   "The two entries did not match. Your PIN has not changed.",
	PINTooShort:   "A PIN needs at least four digits. Your PIN has not changed.",
	PINChanged:    "Your PIN has been changed.",
	PINChangeFail: "Sorry, your PIN could not be changed.",

	GreetUnavailable:     "{who} is not available. Please leave a message after the tone.",
	GreetBusy:            "{who} is busy at the moment. Please leave a message after the tone.",
	GreetUnreachable:     "{who} cannot be reached at the moment. Please leave a message after the tone.",
	GreetUnavailableTail: "is not available. Please leave a message after the tone.",
	GreetBusyTail:        "is busy at the moment. Please leave a message after the tone.",
	GreetUnreachableTail: "cannot be reached at the moment. Please leave a message after the tone.",

	DirectoryEnter:      "Please enter the first three letters of the name on the keypad, then press the pound key.",
	DirectoryConnecting: "Connecting you to",
	DirectoryOne:        "For {name}, press {digit}.",
	DirectoryChoose:     "More than one name matches.",
	DirectoryMore:       "That matches too many names. Please enter more letters.",
	DirectoryNone:       "Sorry, no name matches those letters.",
	DirectoryTooLong:    "That is too many letters. Please enter fewer letters.",
}
