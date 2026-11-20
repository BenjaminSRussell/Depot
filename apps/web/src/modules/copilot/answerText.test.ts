import { describe, expect, it } from "vitest";
import { mayBePartial, parseAnswerText } from "./answerText";

const citations = [{ sourceRef: "a" }, { sourceRef: "b" }];

describe("parseAnswerText", () => {
  it("returns plain text as one segment", () => {
    expect(parseAnswerText("hello", citations)).toEqual([{ kind: "text", text: "hello" }]);
  });

  it("turns each literal [unverified] into a segment", () => {
    expect(parseAnswerText("x [unverified] y [unverified]", citations)).toEqual([
      { kind: "text", text: "x " },
      { kind: "unverified" },
      { kind: "text", text: " y " },
      { kind: "unverified" },
    ]);
  });

  it("numbers a matching cite marker and strips an unmatched one", () => {
    expect(parseAnswerText("p [[cite:b]] q [[cite:zzz]] r", citations)).toEqual([
      { kind: "text", text: "p " },
      { kind: "cite", n: 2 },
      { kind: "text", text: " q " },
      { kind: "text", text: " r" },
    ]);
  });
});

describe("mayBePartial", () => {
  it("is true at eight tool calls or for the limit message only", () => {
    const calls = (n: number) => Array.from({ length: n }, () => ({ tool: "t", status: "OK" as const }));
    expect(mayBePartial({ answer: "a", toolCalls: calls(7) })).toBe(false);
    expect(mayBePartial({ answer: "a", toolCalls: calls(8) })).toBe(true);
    expect(mayBePartial({ answer: "I could not complete the answer within the tool-call limit.", toolCalls: [] })).toBe(true);
  });
});
