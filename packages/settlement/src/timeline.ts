import type { EventBlock, Stage } from "@stockastic/config";

export interface TimelineCheck {
  totalMinutes: number;
  minutesByStage: Record<Stage, number>;
  blockCount: number;
}

/** Sec 17 / Appendix A.3: the schedule must sum to exactly the strict event cap. */
export function checkTimeline(timeline: EventBlock[], expectedTotal: number): TimelineCheck {
  const minutesByStage: Record<Stage, number> = { phase1: 0, transition: 0, phase2: 0, closing: 0 };
  let total = 0;
  for (const b of timeline) {
    total += b.durationMin;
    minutesByStage[b.stage] += b.durationMin;
  }
  if (total !== expectedTotal) {
    throw new Error(`timeline sums to ${total} minutes, expected exactly ${expectedTotal}`);
  }
  return { totalMinutes: total, minutesByStage, blockCount: timeline.length };
}
