package predictor

import "math"

// FSRS is the DSR memory model (difficulty / stability / retrievability) from
// the open-spaced-repetition project, transcribed from the FSRS-6 reference
// implementation (py-fsrs scheduler.py) — the same scheduler family Anki ships
// as its default. It replaces hand-rolled recency heuristics with a model
// fitted on hundreds of millions of real reviews; the published population
// defaults below outperform fixed heuristics before any per-user data exists
// (#209). Everything here is pure: state in, state out.
//
// Reviews carry an Anki-style rating. tifl maps its signals onto ratings in
// the acquire layer (task grades, reader lookups, explicit word ratings); this
// file knows nothing about where ratings come from.

// FSRSRating is the review outcome scale.
type FSRSRating int

const (
	RatingAgain FSRSRating = 1 // failed recall / strong "don't know" signal
	RatingHard  FSRSRating = 2 // recalled with difficulty / weak negative
	RatingGood  FSRSRating = 3 // recalled
	RatingEasy  FSRSRating = 4 // recalled effortlessly / explicit "well known"
)

// FSRSState is the per-(user, item) memory state.
type FSRSState struct {
	Difficulty float64 // 1..10; higher = harder to stabilize
	Stability  float64 // days until retrievability decays to 90%
	LastReview float64 // Unix seconds of the last rated review; 0 = never
}

// defaultFSRSParameters are the FSRS-6 population-fit weights (py-fsrs
// DEFAULT_PARAMETERS). w[0..3] are initial stabilities per rating; the rest
// parameterize the update formulas below.
var defaultFSRSParameters = [21]float64{
	0.212, 1.2931, 2.3065, 8.2956, 6.4133, 0.8334, 3.0194, 0.001,
	1.8722, 0.1666, 0.796, 1.4835, 0.0614, 0.2629, 1.6483, 0.6014,
	1.8729, 0.5425, 0.0912, 0.0658, 0.1542,
}

const (
	fsrsMinDifficulty = 1.0
	fsrsMaxDifficulty = 10.0
	fsrsMinStability  = 0.001
)

// FSRS evaluates and updates memory states. Zero value is not usable; call
// NewFSRS.
type FSRS struct {
	w      [21]float64
	decay  float64 // -w[20]
	factor float64 // 0.9^(1/decay) - 1, so R(elapsed=S) == 0.9 exactly
}

// NewFSRS returns the model with the published population defaults. Per-user
// or per-language fitted weights can be introduced later without changing any
// call site (the optimizer lives in the FSRS ecosystem; we only evaluate).
func NewFSRS() *FSRS {
	f := &FSRS{w: defaultFSRSParameters}
	f.decay = -f.w[20]
	f.factor = math.Pow(0.9, 1/f.decay) - 1
	return f
}

// Retrievability is P(recall now): the power forgetting curve evaluated at
// now (Unix seconds). An item never reviewed has no memory trace — 0.
func (f *FSRS) Retrievability(st FSRSState, now float64) float64 {
	if st.LastReview == 0 || st.Stability <= 0 {
		return 0
	}
	elapsedDays := math.Max(0, (now-st.LastReview)/86400.0)
	return math.Pow(1+f.factor*elapsedDays/st.Stability, f.decay)
}

// Review applies one rated review at time now and returns the next state.
// A first review initializes the state from the rating alone.
func (f *FSRS) Review(st FSRSState, rating FSRSRating, now float64) FSRSState {
	if st.LastReview == 0 || st.Stability <= 0 {
		return FSRSState{
			Difficulty: f.initialDifficulty(rating),
			Stability:  f.w[rating-1],
			LastReview: now,
		}
	}

	elapsedDays := (now - st.LastReview) / 86400.0
	var nextStability float64
	if elapsedDays < 1 {
		// Same-day re-review: the short-term stability formula, which models
		// cramming without granting a full inter-day stability jump.
		nextStability = f.shortTermStability(st.Stability, rating)
	} else {
		r := f.Retrievability(st, now)
		if rating == RatingAgain {
			nextStability = f.forgetStability(st.Difficulty, st.Stability, r)
		} else {
			nextStability = f.recallStability(st.Difficulty, st.Stability, r, rating)
		}
	}
	return FSRSState{
		Difficulty: f.nextDifficulty(st.Difficulty, rating),
		Stability:  clamp(nextStability, fsrsMinStability, math.Inf(1)),
		LastReview: now,
	}
}

func (f *FSRS) initialDifficulty(rating FSRSRating) float64 {
	d := f.w[4] - math.Exp(f.w[5]*float64(rating-1)) + 1
	return clamp(d, fsrsMinDifficulty, fsrsMaxDifficulty)
}

// initialDifficultyRaw is the unclamped form used as the mean-reversion target.
func (f *FSRS) initialDifficultyRaw(rating FSRSRating) float64 {
	return f.w[4] - math.Exp(f.w[5]*float64(rating-1)) + 1
}

func (f *FSRS) nextDifficulty(d float64, rating FSRSRating) float64 {
	deltaD := -(f.w[6] * float64(rating-3))
	damped := d + (10.0-d)*deltaD/9.0
	reverted := f.w[7]*f.initialDifficultyRaw(RatingEasy) + (1-f.w[7])*damped
	return clamp(reverted, fsrsMinDifficulty, fsrsMaxDifficulty)
}

func (f *FSRS) recallStability(d, s, r float64, rating FSRSRating) float64 {
	hardPenalty := 1.0
	if rating == RatingHard {
		hardPenalty = f.w[15]
	}
	easyBonus := 1.0
	if rating == RatingEasy {
		easyBonus = f.w[16]
	}
	return s * (1 + math.Exp(f.w[8])*(11-d)*math.Pow(s, -f.w[9])*
		(math.Exp((1-r)*f.w[10])-1)*hardPenalty*easyBonus)
}

func (f *FSRS) forgetStability(d, s, r float64) float64 {
	longTerm := f.w[11] * math.Pow(d, -f.w[12]) *
		(math.Pow(s+1, f.w[13]) - 1) * math.Exp((1-r)*f.w[14])
	// A lapse can never leave more stability than a same-day re-learn would.
	ceiling := s / math.Exp(f.w[17]*f.w[18])
	return math.Min(longTerm, ceiling)
}

func (f *FSRS) shortTermStability(s float64, rating FSRSRating) float64 {
	inc := math.Exp(f.w[17]*(float64(rating-3)+f.w[18])) * math.Pow(s, -f.w[19])
	if rating == RatingGood || rating == RatingEasy {
		inc = math.Max(inc, 1.0)
	}
	return s * inc
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
