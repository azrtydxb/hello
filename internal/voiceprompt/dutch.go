package voiceprompt

// dutch is the Dutch table. It must carry every key of english.
var dutch = map[Key]string{
	PINEnter:   "Voer uw pincode in, gevolgd door het hekje.",
	PINInvalid: "Die pincode is niet juist. Probeer het opnieuw.",
	PINLocked:  "Te veel foute pogingen. Tot ziens.",
	NoMailbox:  "Dit toestel heeft geen voicemailbox. Tot ziens.",
	Goodbye:    "Tot ziens.",
	InvalidKey: "Sorry, die keuze bestaat niet.",

	Counts:               "U heeft {new} en {saved}.",
	CountNew:             "{n} nieuwe berichten",
	CountNew + ".one":    "1 nieuw bericht",
	CountNew + ".zero":   "geen nieuwe berichten",
	CountSaved:           "{n} bewaarde berichten",
	CountSaved + ".one":  "1 bewaard bericht",
	CountSaved + ".zero": "geen bewaarde berichten",

	MainMenu:    "Voor uw nieuwe berichten, druk op 1. Voor uw bewaarde berichten, druk op 2. Voor opties van de mailbox, druk op 0.",
	NoNew:       "U heeft geen nieuwe berichten.",
	NoSaved:     "U heeft geen bewaarde berichten.",
	NewFolder:   "Uw nieuwe berichten.",
	SavedFolder: "Uw bewaarde berichten.",

	MsgHeader: "Bericht {n}, van {who}, {when}.",
	MsgMenu: "Om dit bericht opnieuw te beluisteren, druk op 1. Voor het volgende bericht, druk op 2. Om het te wissen, druk op 3. " +
		"Om het te bewaren, druk op 9. Voor het vorige bericht, druk op de ster. Om het over te slaan, druk op het hekje. " +
		"Voor opties van de mailbox, druk op 0.",
	MsgMenuShort: "Druk op 1 voor herhalen, 2 voor volgende, 3 om te wissen, 9 om te bewaren, of 0 voor opties.",
	MsgDeleted:   "Bericht gewist.",
	MsgSaved:     "Bericht bewaard.",
	MsgNoPrev:    "Er is geen vorig bericht.",
	MsgEnd:       "Einde van de berichten.",
	MsgUnavail:   "Dat bericht kon niet worden afgespeeld.",

	WhoUnknown:   "een onbekende beller",
	WhoExtension: "toestel {digits}",
	WhoMailbox:   "deze voicemailbox",
	WordPlus:     "plus",
	WhenToday:    "vandaag om {time}",
	WhenYday:     "gisteren om {time}",
	WhenWeekday:  "op {weekday} om {time}",
	WhenDate:     "op {day} {month} om {time}",
	WhenDateYear: "op {day} {month} {year} om {time}",

	weekdayPfx + "0": "zondag", weekdayPfx + "1": "maandag", weekdayPfx + "2": "dinsdag",
	weekdayPfx + "3": "woensdag", weekdayPfx + "4": "donderdag", weekdayPfx + "5": "vrijdag",
	weekdayPfx + "6": "zaterdag",
	monthPfx + "1":   "januari", monthPfx + "2": "februari", monthPfx + "3": "maart",
	monthPfx + "4": "april", monthPfx + "5": "mei", monthPfx + "6": "juni",
	monthPfx + "7": "juli", monthPfx + "8": "augustus", monthPfx + "9": "september",
	monthPfx + "10": "oktober", monthPfx + "11": "november", monthPfx + "12": "december",

	OptMenu: "Opties van de mailbox. Om uw begroeting voor niet beschikbaar op te nemen, druk op 1. Voor de begroeting bij in gesprek, druk op 2. " +
		"Om uw naam op te nemen, druk op 3. Om uw pincode te wijzigen, druk op 4. Om terug te keren naar de standaardbegroeting, druk op 5. " +
		"Om terug te gaan, druk op de ster.",
	RecUnavail:   "Spreek uw begroeting in na de toon. Druk op het hekje als u klaar bent.",
	RecBusy:      "Spreek uw begroeting voor in gesprek in na de toon. Druk op het hekje als u klaar bent.",
	RecName:      "Zeg uw naam na de toon. Druk op het hekje als u klaar bent.",
	RecReview:    "Dit is wat u heeft opgenomen.",
	RecConfirm:   "Druk op 1 om het te bewaren, op 2 om opnieuw op te nemen, of op 3 om te annuleren.",
	RecSaved:     "Uw opname is bewaard.",
	RecCancelled: "Geannuleerd. Er is niets gewijzigd.",
	RecTooShort:  "Die opname was te kort.",
	RecFailed:    "Sorry, uw opname kon niet worden bewaard.",
	RevertDone:   "Uw begroetingen zijn weer de standaardbegroetingen.",

	PINNew:        "Voer uw nieuwe pincode in, minstens vier cijfers, gevolgd door het hekje.",
	PINConfirm:    "Voer de pincode nogmaals in ter bevestiging, gevolgd door het hekje.",
	PINMismatch:   "De twee invoeren kwamen niet overeen. Uw pincode is niet gewijzigd.",
	PINTooShort:   "Een pincode heeft minstens vier cijfers. Uw pincode is niet gewijzigd.",
	PINChanged:    "Uw pincode is gewijzigd.",
	PINChangeFail: "Sorry, uw pincode kon niet worden gewijzigd.",

	GreetUnavailable:     "{who} is niet beschikbaar. Laat een bericht achter na de toon.",
	GreetBusy:            "{who} is in gesprek. Laat een bericht achter na de toon.",
	GreetUnreachable:     "{who} is momenteel niet bereikbaar. Laat een bericht achter na de toon.",
	GreetUnavailableTail: "is niet beschikbaar. Laat een bericht achter na de toon.",
	GreetBusyTail:        "is in gesprek. Laat een bericht achter na de toon.",
	GreetUnreachableTail: "is momenteel niet bereikbaar. Laat een bericht achter na de toon.",

	DirectoryEnter:      "Toets de eerste drie letters van de naam in op het toetsenbord, gevolgd door het hekje.",
	DirectoryConnecting: "U wordt doorverbonden met",
	DirectoryOne:        "Voor {name}, druk op {digit}.",
	DirectoryChoose:     "Er zijn meerdere namen gevonden.",
	DirectoryMore:       "Dat levert te veel namen op. Toets meer letters in.",
	DirectoryNone:       "Sorry, er is geen naam gevonden bij deze letters.",
	DirectoryTooLong:    "Dat zijn te veel letters. Toets minder letters in.",
}
