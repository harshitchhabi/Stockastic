import { describe, expect, it } from "vitest";
import { ago, fmtClock, fmtMinSec, fmtUptime } from "./format";

describe("format", () => {
  it("formats the event clock", () => {
    expect(fmtClock(0)).toBe("0:00:00");
    expect(fmtClock(9_665_000)).toBe("2:41:05");
    expect(fmtClock(300 * 60_000)).toBe("5:00:00");
  });
  it("never renders negative or broken values", () => {
    expect(fmtClock(-5000)).toBe("0:00:00");
    expect(fmtClock(NaN)).toBe("0:00:00");
    expect(fmtMinSec(-1)).toBe("0:00");
  });
  it("formats minutes and seconds", () => {
    expect(fmtMinSec(725_000)).toBe("12:05");
  });
  it("formats relative time", () => {
    expect(ago(1000, 4000)).toBe("3 s ago");
    expect(ago(0, 4 * 60_000)).toBe("4 min ago");
    expect(ago(0, 2 * 3600_000)).toBe("2 h ago");
  });
  it("formats uptime", () => {
    expect(fmtUptime(59)).toBe("0m 59s");
    expect(fmtUptime(3 * 3600 + 120)).toBe("3h 2m");
    expect(fmtUptime(90000)).toBe("1d 1h");
  });
});
