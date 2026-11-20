import type { ChatResponse } from "./api/client";
import { TOOL_LIMIT_MESSAGE, TOOL_LIMIT_CALLS } from "./domain";

export type AnswerSegment =
  | { kind: "text"; text: string }
  | { kind: "unverified" }
  | { kind: "cite"; n: number };

const MARKER = /\[unverified\]|\[\[cite:([^\]]*)\]\]/g;

export function parseAnswerText(text: string, citations: readonly { sourceRef: string }[]): AnswerSegment[] {
  const segments: AnswerSegment[] = [];
  let from = 0;
  const pushText = (end: number) => {
    if (end > from) segments.push({ kind: "text", text: text.slice(from, end) });
  };
  for (const match of text.matchAll(MARKER)) {
    pushText(match.index);
    from = match.index + match[0].length;
    if (match[1] === undefined) {
      segments.push({ kind: "unverified" });
      continue;
    }
    const index = citations.findIndex((c) => c.sourceRef === match[1]);
    if (index !== -1) segments.push({ kind: "cite", n: index + 1 });
  }
  pushText(text.length);
  return segments;
}

export function mayBePartial(response: Pick<ChatResponse, "answer" | "toolCalls">): boolean {
  return response.toolCalls.length >= TOOL_LIMIT_CALLS || response.answer === TOOL_LIMIT_MESSAGE;
}
