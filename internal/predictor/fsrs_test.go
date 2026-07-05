package predictor

import (
	"math"
	"testing"
)

const day = 86400.0

// TestFSRSRetrievabilityAnchor: by construction of the forgetting curve,
// retrievability is exactly 0.9 when elapsed time equals stability. This is
// the model's defining invariant and pins the decay/factor derivation.
func TestFSRSRetrievabilityAnchor(t *testing.T) {
	f := NewFSRS()
	for _, stabilityDays := range []float64{0.5, 1, 7, 30, 365} {
		st := FSRSState{Difficulty: 5, Stability: stabilityDays, LastReview: 0}
		st.LastReview = 1e9
		now := st.LastReview + stabilityDays*day
		if r := f.Retrievability(st, now); math.Abs(r-0.9) > 1e-9 {
			t.Fatalf("R(elapsed=S=%v) = %v, want exactly 0.9", stabilityDays, r)
		}
	}
}

// TestFSRSInitialState: first review initializes stability to w[rating-1]
// (the published parameter meaning) and a harder first rating yields higher
// difficulty.
func TestFSRSInitialState(t *testing.T) {
	f := NewFSRS()
	now := 1e9
	want := map[FSRSRating]float64{RatingAgain: 0.212, RatingHard: 1.2931, RatingGood: 2.3065, RatingEasy: 8.2956}
	var prevDifficulty = math.Inf(1)
	for _, rating := range []FSRSRating{RatingAgain, RatingHard, RatingGood, RatingEasy} {
		st := f.Review(FSRSState{}, rating, now)
		if math.Abs(st.Stability-want[rating]) > 1e-9 {
			t.Fatalf("initial stability(%d) = %v, want %v", rating, st.Stability, want[rating])
		}
		if st.Difficulty >= prevDifficulty {
			t.Fatalf("difficulty must decrease with better first rating: %v then %v", prevDifficulty, st.Difficulty)
		}
		prevDifficulty = st.Difficulty
	}
}

// TestFSRSProperties: the behavioral shape the selector depends on.
func TestFSRSProperties(t *testing.T) {
	f := NewFSRS()
	now := 1e9

	t.Run("retrievability decays with elapsed time", func(t *testing.T) {
		st := f.Review(FSRSState{}, RatingGood, now)
		r1 := f.Retrievability(st, now+1*day)
		r10 := f.Retrievability(st, now+10*day)
		r100 := f.Retrievability(st, now+100*day)
		if !(r1 > r10 && r10 > r100) {
			t.Fatalf("decay violated: %v %v %v", r1, r10, r100)
		}
	})

	t.Run("successful reviews grow stability", func(t *testing.T) {
		st := f.Review(FSRSState{}, RatingGood, now)
		s0 := st.Stability
		st = f.Review(st, RatingGood, now+3*day)
		s1 := st.Stability
		st = f.Review(st, RatingGood, now+10*day)
		if !(s1 > s0 && st.Stability > s1) {
			t.Fatalf("stability not growing: %v %v %v", s0, s1, st.Stability)
		}
	})

	t.Run("a lapse shrinks stability", func(t *testing.T) {
		st := f.Review(FSRSState{}, RatingGood, now)
		st = f.Review(st, RatingGood, now+5*day)
		before := st.Stability
		st = f.Review(st, RatingAgain, now+15*day)
		if st.Stability >= before {
			t.Fatalf("lapse must shrink stability: %v -> %v", before, st.Stability)
		}
		if st.Stability < fsrsMinStability {
			t.Fatalf("stability below floor: %v", st.Stability)
		}
	})

	t.Run("easy beats good beats hard for the same review", func(t *testing.T) {
		base := f.Review(FSRSState{}, RatingGood, now)
		sHard := f.Review(base, RatingHard, now+5*day).Stability
		sGood := f.Review(base, RatingGood, now+5*day).Stability
		sEasy := f.Review(base, RatingEasy, now+5*day).Stability
		if !(sEasy > sGood && sGood > sHard) {
			t.Fatalf("rating ordering violated: hard=%v good=%v easy=%v", sHard, sGood, sEasy)
		}
	})

	t.Run("failures raise difficulty, successes lower it", func(t *testing.T) {
		st := f.Review(FSRSState{}, RatingGood, now)
		d0 := st.Difficulty
		failed := f.Review(st, RatingAgain, now+2*day)
		if failed.Difficulty <= d0 {
			t.Fatalf("Again must raise difficulty: %v -> %v", d0, failed.Difficulty)
		}
		eased := f.Review(st, RatingEasy, now+2*day)
		if eased.Difficulty >= d0 {
			t.Fatalf("Easy must lower difficulty: %v -> %v", d0, eased.Difficulty)
		}
	})

	t.Run("difficulty stays clamped over adversarial sequences", func(t *testing.T) {
		st := f.Review(FSRSState{}, RatingAgain, now)
		for i := range 50 {
			st = f.Review(st, RatingAgain, now+float64(i+1)*2*day)
		}
		if st.Difficulty > fsrsMaxDifficulty || st.Difficulty < fsrsMinDifficulty {
			t.Fatalf("difficulty out of range: %v", st.Difficulty)
		}
		for i := range 100 {
			st = f.Review(st, RatingEasy, now+float64(100+i)*3*day)
		}
		if st.Difficulty > fsrsMaxDifficulty || st.Difficulty < fsrsMinDifficulty {
			t.Fatalf("difficulty out of range after easies: %v", st.Difficulty)
		}
	})

	t.Run("same-day re-review does not grant a full stability jump", func(t *testing.T) {
		st := f.Review(FSRSState{}, RatingGood, now)
		sameDay := f.Review(st, RatingGood, now+3600) // one hour later
		nextWeek := f.Review(st, RatingGood, now+7*day)
		if sameDay.Stability >= nextWeek.Stability {
			t.Fatalf("cramming beat spaced review: sameDay=%v spaced=%v", sameDay.Stability, nextWeek.Stability)
		}
		if sameDay.Stability < st.Stability {
			t.Fatalf("good same-day review must not shrink stability: %v -> %v", st.Stability, sameDay.Stability)
		}
	})

	t.Run("never-reviewed item has zero retrievability", func(t *testing.T) {
		if r := f.Retrievability(FSRSState{}, now); r != 0 {
			t.Fatalf("unseen item R = %v, want 0", r)
		}
	})
}
