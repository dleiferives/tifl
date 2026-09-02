// Light haptic feedback for touch interactions in the reader.
//
// Uses the Web Vibration API where it exists (Android browsers). iOS Safari and
// the current Capacitor shell have no vibration API, so this is a silent no-op
// there; wiring Capacitor's Haptics plugin for a crisp tap on iOS is a follow-up
// (see mobile/README.md) and would slot in behind this same function.
export function hapticTick(): void {
  try {
    navigator.vibrate?.(8);
  } catch {
    // Vibration is a non-essential enhancement — never let it throw into a gesture.
  }
}
