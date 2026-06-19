package el

// frequency lists Modern Greek lemmas ordered from most to least common,
// derived from frequency data for contemporary written and spoken Greek.
// Used by the selector when choosing new items to introduce (high-frequency
// unlearned items are preferred). All entries are NFC-normalized lowercase.
var frequency = []string{
	// ── Articles & determiners ────────────────────────────────────────────────
	"ο",  // the (masc. sg.)
	"η",  // the (fem. sg.)
	"το", // the (neut. sg.)
	"οι", // the (pl. masc./fem.)
	"τα", // the (pl. neut.)

	// ── Conjunctions & particles ──────────────────────────────────────────────
	"και",    // and, also
	"να",     // να + subjunctive (to, that, should)
	"δεν",    // not
	"δε",     // not (variant), but (literary)
	"με",     // with; me (acc.)
	"σε",     // in, at, to; you (acc.)
	"ότι",    // that (conjunction)
	"αν",     // if
	"που",    // who, which, that (relative); where (interrogative)
	"για",    // for, about, in order to
	"ή",      // or
	"αλλά",   // but
	"όμως",   // however, but
	"μα",     // but (colloquial)
	"γιατί",  // because; why
	"ενώ",    // while, whereas
	"όταν",   // when
	"πριν",   // before
	"μετά",   // after, then
	"καθώς",  // as, while, since
	"άρα",    // so, therefore
	"λοιπόν", // so, then, well

	// ── Prepositions ──────────────────────────────────────────────────────────
	"από",   // from, of, since
	"στο",   // in/to/at the (contraction σε + το)
	"στη",   // in/to/at the (contraction σε + η)
	"στον",  // in/to/at the (contraction σε + τον)
	"στην",  // in/to/at the (contraction σε + την)
	"σαν",   // like, as
	"χωρίς", // without
	"πάνω",  // on, above, up
	"κάτω",  // under, below, down
	"μέσα",  // inside, in
	"έξω",   // outside, out

	// ── Pronouns ──────────────────────────────────────────────────────────────
	"εγώ",     // I
	"εσύ",     // you (sg.)
	"αυτός",   // he / this (masc.)
	"αυτή",    // she / this (fem.)
	"αυτό",    // it / this (neut.)
	"εμείς",   // we
	"εσείς",   // you (pl.)
	"αυτοί",   // they (masc.)
	"αυτές",   // they (fem.)
	"αυτά",    // they (neut.) / these
	"μου",     // my / of me
	"σου",     // your (sg.) / of you
	"του",     // his / of him / of it
	"της",     // her / of her
	"μας",     // our / of us
	"σας",     // your (pl.) / of you
	"τους",    // their / of them
	"κάτι",    // something
	"κάποιος", // someone
	"τίποτα",  // nothing, anything
	"κανείς",  // nobody, anyone

	// ── Core verbs ────────────────────────────────────────────────────────────
	"είμαι",       // to be
	"έχω",         // to have
	"λέω",         // to say
	"πάω",         // to go
	"έρχομαι",     // to come
	"κάνω",        // to do, make
	"θέλω",        // to want
	"μπορώ",       // to be able to, can
	"ξέρω",        // to know
	"βλέπω",       // to see
	"δίνω",        // to give
	"παίρνω",      // to take, get
	"βάζω",        // to put, place
	"βγαίνω",      // to come/go out
	"μένω",        // to stay, live (reside)
	"πιστεύω",     // to believe, think
	"νομίζω",      // to think, suppose
	"αρέσω",       // to like (indirect: μου αρέσει)
	"χρειάζομαι",  // to need
	"ακούω",       // to hear, listen
	"μιλάω",       // to speak, talk
	"φεύγω",       // to leave, depart
	"βρίσκω",      // to find
	"βοηθάω",      // to help
	"περνάω",      // to pass, spend (time)
	"αγαπάω",      // to love
	"καταλαβαίνω", // to understand
	"ρωτάω",       // to ask
	"απαντάω",     // to answer
	"τρώω",        // to eat
	"πίνω",        // to drink
	"κοιμάμαι",    // to sleep
	"δουλεύω",     // to work
	"αγοράζω",     // to buy
	"πουλάω",      // to sell
	"διαβάζω",     // to read, study
	"γράφω",       // to write
	"παίζω",       // to play
	"γελάω",       // to laugh
	"κλαίω",       // to cry
	"πηγαίνω",     // to go (variant of πάω)
	"ανοίγω",      // to open
	"κλείνω",      // to close

	// ── Common nouns ──────────────────────────────────────────────────────────
	"άνθρωπος",   // person, human
	"άντρας",     // man
	"γυναίκα",    // woman
	"παιδί",      // child
	"οικογένεια", // family
	"φίλος",      // friend (masc.)
	"φίλη",       // friend (fem.)
	"σπίτι",      // house, home
	"δουλειά",    // work, job
	"ζωή",        // life
	"χρόνος",     // time, year
	"μέρα",       // day
	"νύχτα",      // night
	"ώρα",        // hour, time
	"χρόνια",     // years (pl. of χρόνος)
	"πράγμα",     // thing
	"μέρος",      // part, place
	"τρόπος",     // way, manner
	"θέμα",       // topic, issue
	"πρόβλημα",   // problem
	"λόγος",      // word, reason, speech
	"καιρός",     // weather, time
	"χώρα",       // country
	"πόλη",       // city
	"δρόμος",     // road, street
	"νερό",       // water
	"φαγητό",     // food
	"δουλειά",    // work (duplicate intentional — high frequency)
	"σχολείο",    // school
	"γλώσσα",     // language, tongue
	"μάθημα",     // lesson, class
	"βιβλίο",     // book
	"ιστορία",    // story, history

	// ── Common adjectives ─────────────────────────────────────────────────────
	"καλός",      // good
	"κακός",      // bad
	"μεγάλος",    // big, great, old (of people)
	"μικρός",     // small, young
	"νέος",       // new, young
	"παλιός",     // old (things)
	"ωραίος",     // beautiful, nice
	"σωστός",     // correct, right
	"λάθος",      // wrong, mistake
	"εύκολος",    // easy
	"δύσκολος",   // difficult
	"πολύς",      // many, much, a lot
	"λίγος",      // few, a little
	"άλλος",      // other, another
	"ίδιος",      // same
	"πρώτος",     // first
	"τελευταίος", // last
	"ακριβός",    // expensive; exact
	"φτηνός",     // cheap
	"γρήγορος",   // fast
	"αργός",      // slow

	// ── Adverbs ───────────────────────────────────────────────────────────────
	"τώρα",      // now
	"εδώ",       // here
	"εκεί",      // there
	"πολύ",      // very, much
	"λίγο",      // a little
	"πάντα",     // always
	"ποτέ",      // never, ever
	"πάλι",      // again
	"ακόμα",     // still, yet, even
	"ήδη",       // already
	"αμέσως",    // immediately
	"μόνο",      // only
	"μαζί",      // together
	"επίσης",    // also, too
	"σχεδόν",    // almost, nearly
	"πάρα πολύ", // very much, too much
	"αλλού",     // elsewhere

	// ── Question words ────────────────────────────────────────────────────────
	"τι",    // what
	"ποιος", // who, which
	"πού",   // where
	"πότε",  // when
	"πώς",   // how
	"πόσο",  // how much
	"γιατί", // why (duplicate — high frequency)
}
