package el

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"
)

// serializeSkillConstraints is the Greek-language version of
// llm.SerializeSkillConstraints. Prompting in Greek noticeably improves output.
func serializeSkillConstraints(sc *domain.SkillConstraints) string {
	if sc == nil {
		return ""
	}
	var b strings.Builder
	if len(sc.Allowed) > 0 {
		fmt.Fprintf(&b, "Χρησιμοποίησε ελεύθερα: %s.\n", strings.Join(sc.Allowed, ", "))
	}
	if len(sc.Introduce) > 0 {
		fmt.Fprintf(&b, "Εισήγαγε με σαφή συμφραζόμενα: %s.\n", strings.Join(sc.Introduce, ", "))
	}
	if len(sc.Avoid) > 0 {
		fmt.Fprintf(&b, "Απέφυγε εντελώς: %s.\n", strings.Join(sc.Avoid, ", "))
	}
	if sc.VocabRange != "" {
		fmt.Fprintf(&b, "Λεξιλόγιο: περιόρισε σε %s.\n", sc.VocabRange)
	}
	return b.String()
}

// formatItemNew is the Greek-language version of llm.FormatItemNew.
func formatItemNew(it domain.KnowledgeItem) string {
	line := llm.FormatItemCompact(it)
	if ex := it.Metadata["example"]; ex != nil {
		if s, ok := ex.(string); ok && s != "" {
			line += "; παράδειγμα: " + s
		}
	}
	return line
}

// StorySessionDAG returns the Greek-specific generation DAG for the story
// session contract. It implements lang.StoryContractProvider.
func (Greek) StorySessionDAG() llm.GenerationDAG {
	return llm.GenerationDAG{
		Steps: []llm.StepDef{
			storyStep(),
			mcTaskStep(),
			fillTaskStep(),
		},
	}
}

// storyStep builds the Greek story generation step.
// The model writes the story as plain text (no JSON mode) for narrative quality.
// estimated_coverage is computed algorithmically from the background key list;
// no LLM glossary is generated (glossing is handled by the kaikki/Wiktionary path).
func storyStep() llm.StepDef {
	return llm.StepDef{
		ID:         "story",
		OutputKind: llm.OutputStory,
		Deps: []llm.Dep{
			{Kind: llm.DepBackground},
			{Kind: llm.DepTargets},
			{Kind: llm.DepNew},
			{Kind: llm.DepSkills},
			{Kind: llm.DepLevel},
			{Kind: llm.DepTopic},
			{Kind: llm.DepHistory},
		},
		RunFn: func(ctx context.Context, in llm.StepInputs, client llm.Client) (any, error) {
			storyText, err := runStoryCall(ctx, in, client)
			if err != nil {
				return nil, err
			}
			res := llm.StoryResult{
				Story:             storyText,
				EstimatedCoverage: computeCoverage(storyText, in),
			}
			if err := res.Validate(); err != nil {
				return nil, fmt.Errorf("story validate: %w", err)
			}
			return res, nil
		},
	}
}

// computeCoverage returns the fraction (0–100) of background vocabulary keys
// that appear in the story text. Uses simple lowercased substring matching;
// Greek inflection means this is an undercount, but it's deterministic and cheap.
func computeCoverage(story string, in llm.StepInputs) float64 {
	if len(in.Background) == 0 {
		return 0
	}
	lower := strings.ToLower(story)
	hit := 0
	for _, item := range in.Background {
		if strings.Contains(lower, strings.ToLower(item.Key)) {
			hit++
		}
	}
	return float64(hit) / float64(len(in.Background)) * 100
}

// runStoryCall sends the creative writing prompt (plain text, no JSON mode)
// and returns the raw story string.
func runStoryCall(ctx context.Context, in llm.StepInputs, client llm.Client) (string, error) {
	var sys strings.Builder
	sys.WriteString("Είσαι έμπειρος Έλληνας συγγραφέας που γράφει ιστορίες για μαθητές των ελληνικών. Γράφεις μόνο στα ελληνικά, σε απλό αφηγηματικό κείμενο. Δίνεις μόνο την ιστορία — χωρίς τίτλο, χωρίς markdown, χωρίς μετα-σχόλια.")

	if in.OnboardingHint != "" {
		fmt.Fprintf(&sys, "\n- %s", in.OnboardingHint)
	} else if len(in.Background) == 0 {
		sys.WriteString("\n- Ο μαθητής δεν έχει ακόμα λεξιλόγιο. Γράψε μια πολύ σύντομη παράγραφο (3–4 απλές προτάσεις) χρησιμοποιώντας μόνο τις πιο βασικές ελληνικές λέξεις: βασικές αντωνυμίες, τα ρήματα είμαι και έχω, 1–2 συχνά ουσιαστικά και τα μόρια και, δεν, να.")
	}

	var usr strings.Builder

	if in.OnboardingHint != "" {
		// During onboarding: skip the background word list (it signals complex
		// vocabulary) and use a simple framing that doesn't contradict the hint.
		if in.Topic != "" {
			fmt.Fprintf(&usr, "\nΘέμα: %s\n", in.Topic)
		}
		usr.WriteString("\nΟδηγίες:\n")
	} else {
		if len(in.Background) > 0 {
			usr.WriteString("\nΓΝΩΣΤΕΣ ΛΕΞΕΙΣ ΤΟΥ ΑΝΑΓΝΩΣΤΗ — λέξεις που ο αναγνώστης ήδη γνωρίζει και μπορείς να αντλήσεις ελεύθερα από αυτές (δεν χρειάζεται να τις χρησιμοποιήσεις όλες):\n")
			for _, it := range in.Background {
				fmt.Fprintf(&usr, "- %s\n", llm.FormatItemCompact(it))
			}
		}

		usr.WriteString("\n— — — — — — — — — — — — — — — — — — — — — — — — — — — — — — — — — — — —\n")
		usr.WriteString("\nΤΩΡΑ Η ΕΡΓΑΣΙΑ ΣΟΥ:\n")

		if in.Topic != "" {
			fmt.Fprintf(&usr, "\nΓράψε μια πλήρη, ανεπτυγμένη ιστορία πάνω στο θέμα: %s\n", in.Topic)
		}

		usr.WriteString("\nΟδηγίες:\n- Χώρισε την ιστορία σε τουλάχιστον 6 παραγράφους, η καθεμία με 4 έως 6 προτάσεις.\n")
	}

	if constraints := serializeSkillConstraints(in.Skills); constraints != "" {
		fmt.Fprintf(&usr, "- Γλωσσική πολυπλοκότητα (με βάση τις δεξιότητες που έχει κατακτήσει ο μαθητής): %s\n", constraints)
	} else {
		fmt.Fprintf(&usr, "- Επίπεδο: %s\n", llm.LevelOrDefault(in.Level))
	}

	llm.WriteItemBlock(&usr,
		"Υποχρεωτικό λεξιλόγιο — χρησιμοποίησε ΟΛΕΣ αυτές τις λέξεις-κλειδιά",
		in.Targets,
		llm.FormatItemTarget,
	)

	llm.WriteItemBlock(&usr,
		"Εισήγαγε απαλά και αυτές τις νέες λέξεις, με αρκετά συμφραζόμενα",
		in.New,
		formatItemNew,
	)

	usr.WriteString("- Χρησιμοποίησε μόνο υπαρκτές, κοινές ελληνικές λέξεις· μην εφευρίσκεις λέξεις.\n")
	usr.WriteString("- Γράψε καθαρό κείμενο: χωρίς τίτλους, χωρίς markdown ή αστερίσκους, και χωρίς μετα-σχόλια.\n")

	if topics := llm.RecentTopics(in.History); topics != "" {
		fmt.Fprintf(&usr, "\nΑπέφυγε να επαναλάβεις αυτά τα πρόσφατα θέματα: %s\n", topics)
	}

	resp, err := client.Complete(ctx, "story", llm.LLMRequest{
		System:      sys.String(),
		User:        usr.String(),
		Temperature: 0.8,
		MaxTokens:   2500,
	})
	if err != nil {
		return "", fmt.Errorf("story call: %w", err)
	}
	return strings.TrimSpace(resp.Text), nil
}

// mcTaskStep builds the Greek multiple-choice comprehension task step.
func mcTaskStep() llm.StepDef {
	return llm.StepDef{
		ID:         "mc_task",
		OutputKind: llm.OutputMCTask,
		Deps: []llm.Dep{
			{Kind: llm.DepStep, StepID: "story"},
			{Kind: llm.DepTargets},
			{Kind: llm.DepLevel},
			{Kind: llm.DepContentSchemas},
			{Kind: llm.DepPriorQuestions},
		},
		Build: func(in llm.StepInputs) llm.LLMRequest {
			var sys strings.Builder
			sys.WriteString("Είσαι εξεταστής για μαθητές ελληνικών. Δημιουργείς ερώτηση κατανόησης πολλαπλής επιλογής (comprehension_mc) πάνω σε μια ιστορία.\n")
			sys.WriteString("\nΚανόνες:\n")
			sys.WriteString("- Η ερώτηση και ΟΛΕς οι επιλογές γράφονται μόνο στα ΕΛΛΗΝΙΚΑ — μόνο ελληνικοί χαρακτήρες, χωρίς λατινικά, κυριλλικά ή άλλα γράμματα.\n")
			sys.WriteString("- Η σωστή απάντηση στηρίζεται αποκλειστικά στο κείμενο — όχι σε γενική γνώση.\n")
			sys.WriteString("- Οι τρεις λανθασμένες επιλογές είναι πιθανές αλλά σαφώς λανθασμένες σε σχέση με την ιστορία — όχι προφανώς άσχετες, όχι παγίδες.\n")
			sys.WriteString("- Χρησιμοποίησε μόνο λεξιλόγιο που εμφανίζεται στην ιστορία — μην εισάγεις άγνωστες λέξεις στις επιλογές.\n")
			sys.WriteString("- Ρύθμιζε τη δυσκολία σύμφωνα με το επίπεδο: αρχάριοι → απλές ερωτήσεις «ποιος/τι/πού», προχωρημένοι → ερωτήσεις συμπερασμού ή «γιατί».")

			if schema, ok := in.ContentSchemas["comprehension_mc"]; ok && schema != "" {
				fmt.Fprintf(&sys, "\nΑπάντησε ακριβώς με αυτό το JSON: %s\nΜόνο JSON, χωρίς κείμενο.", schema)
			}

			if len(in.PriorQuestions) > 0 {
				sys.WriteString("\nΜΗΝ επαναλάβεις καμία από αυτές τις ερωτήσεις που έχουν ήδη δημιουργηθεί:\n")
				for _, q := range in.PriorQuestions {
					fmt.Fprintf(&sys, "- %s\n", q)
				}
			}
			writeRejectedTaskHint(&sys, in.RejectedTask)

			storyText := ""
			if sr, ok := in.Steps["story"].(llm.StoryResult); ok {
				storyText = sr.Story
			}

			var usr strings.Builder
			fmt.Fprintf(&usr, "Επίπεδο μαθητή: %s\n", llm.LevelOrDefault(in.Level))
			usr.WriteString("\nΛέξεις-κλειδιά που ασκεί η ερώτηση:\n")
			llm.WriteItemBlock(&usr, "Λέξεις-κλειδιά", in.Targets, llm.FormatItemTarget)
			usr.WriteString("\nΙστορία:\n")
			usr.WriteString(storyText)

			return llm.LLMRequest{
				System:         sys.String(),
				User:           usr.String(),
				Temperature:    0.6,
				MaxTokens:      10000,
				ResponseFormat: "json",
			}
		},
		Parse: llm.ParseMapResult,
	}
}

// fillTaskStep builds the Greek fill-blank task step.
func fillTaskStep() llm.StepDef {
	return llm.StepDef{
		ID:         "fill_task",
		OutputKind: llm.OutputFillTask,
		Deps: []llm.Dep{
			{Kind: llm.DepStep, StepID: "story"},
			{Kind: llm.DepTargets},
			{Kind: llm.DepLevel},
			{Kind: llm.DepContentSchemas},
			{Kind: llm.DepPriorQuestions},
		},
		Build: func(in llm.StepInputs) llm.LLMRequest {
			var sys strings.Builder
			sys.WriteString("Είσαι εξεταστής για μαθητές ελληνικών. Δημιουργείς άσκηση συμπλήρωσης κενού (fill_blank) από μια ιστορία.\n")
			sys.WriteString("\nΚανόνες:\n")
			sys.WriteString("- Επέλεξε μια ΑΥΤΟΥΣΙΑ πρόταση από την ιστορία που περιέχει μία από τις λέξεις-κλειδιά.\n")
			sys.WriteString("- Αντικατάστησε τη λέξη-κλειδί με ___ — κράτα το υπόλοιπο κείμενο ακριβώς όπως στην ιστορία.\n")
			sys.WriteString("- Στο acceptable_forms: βάλε ΠΡΩΤΑ την ακριβή μορφή που εμφανίζεται στην ιστορία, μετά οποιεσδήποτε εναλλακτικές ορθογραφίες που πρέπει να γίνονται δεκτές (π.χ. τονισμός). Συνήθως η σωστή απάντηση είναι μία — μην προσθέτεις λανθασμένες γραμματικές μορφές.\n")
			sys.WriteString("- Το κενό να μην είναι προφανές από τη γραμματική μόνο — να απαιτεί κατανόηση.\n")
			sys.WriteString("- Μην αλλάζεις τη σειρά των λέξεων ή άλλες λέξεις της πρότασης.")

			if schema, ok := in.ContentSchemas["fill_blank"]; ok && schema != "" {
				fmt.Fprintf(&sys, "\nΑπάντησε ακριβώς με αυτό το JSON: %s\nΜόνο JSON, χωρίς κείμενο.", schema)
			}
			if len(in.PriorQuestions) > 0 {
				sys.WriteString("\nΜΗΝ επαναλάβεις καμία από αυτές τις ασκήσεις που έχουν ήδη δημιουργηθεί:\n")
				for _, q := range in.PriorQuestions {
					fmt.Fprintf(&sys, "- %s\n", q)
				}
			}
			writeRejectedTaskHint(&sys, in.RejectedTask)

			storyText := ""
			if sr, ok := in.Steps["story"].(llm.StoryResult); ok {
				storyText = sr.Story
			}

			var usr strings.Builder
			fmt.Fprintf(&usr, "Επίπεδο μαθητή: %s\n", llm.LevelOrDefault(in.Level))
			usr.WriteString("\nΛέξεις-κλειδιά για το κενό:\n")
			llm.WriteItemBlock(&usr, "Λέξεις-κλειδιά", in.Targets, llm.FormatItemTarget)
			usr.WriteString("\nΙστορία:\n")
			usr.WriteString(storyText)

			return llm.LLMRequest{
				System:         sys.String(),
				User:           usr.String(),
				Temperature:    0.6,
				MaxTokens:      10000,
				ResponseFormat: "json",
			}
		},
		Parse: llm.ParseMapResult,
	}
}

func writeRejectedTaskHint(sys *strings.Builder, rejected *llm.TaskRejectedExample) {
	if rejected == nil {
		return
	}
	sys.WriteString("\nΟ μαθητής ανέφερε την παρακάτω άσκηση ως προβληματική. ΜΗΝ δημιουργήσεις ξανά την ίδια άσκηση και απόφυγε το ίδιο ελάττωμα.\n")
	if rejected.Reason != "" {
		fmt.Fprintf(sys, "Κατηγορία αναφοράς: %s\n", rejected.Reason)
	}
	if rejected.Note != "" {
		fmt.Fprintf(sys, "Σημείωση μαθητή: %s\n", rejected.Note)
	}
	fmt.Fprintf(sys, "Προβληματικό περιεχόμενο: %s\n", compactPromptJSON(rejected.Content))
}

func compactPromptJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(out)
}
